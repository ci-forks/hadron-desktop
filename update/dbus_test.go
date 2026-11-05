package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of its own and returns its address. The
// golang:alpine build stage has no dbus-daemon, so there the bus tests skip.
func privateBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the bus address: %v", err)
	}
	return strings.TrimSpace(addr)
}

func busConn(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// fakeAuth answers every question the same way and records them.
type fakeAuth struct {
	mu    sync.Mutex
	allow bool
	err   error
	asked []string // "sender action"
}

func (a *fakeAuth) Allowed(sender dbus.Sender, action string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, string(sender)+" "+action)
	return a.allow, a.err
}

func (a *fakeAuth) questions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// busFixture runs serve for d on a private bus and returns a client
// connection and the service object as the client sees it.
type busFixture struct {
	addr   string
	client *dbus.Conn
	obj    dbus.BusObject
	done   chan error    // receives serve's result
	exited chan struct{} // closed when serve has returned
}

func startService(t *testing.T, d *Daemon, a Authorizer) *busFixture {
	t.Helper()
	addr := privateBus(t)
	server := busConn(t, addr)
	client := busConn(t, addr)
	f := &busFixture{addr: addr, client: client, obj: client.Object(busName, busPath), done: make(chan error, 1), exited: make(chan struct{})}
	go func() {
		f.done <- serve(server, d, a)
		close(f.exited)
	}()
	// serve reads the idle variables; it must be gone before the next test
	// changes them.
	t.Cleanup(func() {
		server.Close()
		select {
		case <-f.exited:
		case <-time.After(5 * time.Second):
			t.Error("serve did not return after its connection closed")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var has bool
		err := client.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, busName).Store(&has)
		if err == nil && has {
			return f
		}
		select {
		case err := <-f.done:
			t.Fatalf("serve returned before owning the name: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the service never owned its name")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *busFixture) call(method string, args ...any) *dbus.Call {
	return f.obj.Call(busInterface+"."+method, 0, args...)
}

func errName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	var dp *dbus.Error
	if errors.As(err, &dp) {
		return dp.Name
	}
	return fmt.Sprintf("not a D-Bus error: %v", err)
}

func (f *busFixture) status(t *testing.T) map[string]dbus.Variant {
	t.Helper()
	var st map[string]dbus.Variant
	if err := f.call("Status").Store(&st); err != nil {
		t.Fatalf("Status: %v", err)
	}
	return st
}

func (f *busFixture) waitBusState(t *testing.T, want State) map[string]dbus.Variant {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := f.status(t)
		if st["state"].Value() == string(want) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %v (detail %v), want %q", st["state"], st["detail"], want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBusRefusesUnauthorized(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	auth := &fakeAuth{allow: false}
	f := startService(t, NewDaemon(b), auth)

	ref := ghcrRepo + ":sway-v0.0.6"
	err := f.call("Apply", ref, dg[ref], false).Err
	if got := errName(err); got != "io.kairos.HadronUpdate1.Error.NotAuthorized" {
		t.Fatalf("Apply error %q (%v), want NotAuthorized", got, err)
	}
	for _, m := range []string{"Cancel", "Reboot", "RebootPrevious"} {
		if got := errName(f.call(m).Err); got != "io.kairos.HadronUpdate1.Error.NotAuthorized" {
			t.Fatalf("%s error %q, want NotAuthorized", m, got)
		}
	}
	me := f.client.Names()[0]
	want := []string{me + " " + actionApply, me + " " + actionApply, me + " " + actionReboot, me + " " + actionReboot}
	if got := auth.questions(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("asked %q, want %q", got, want)
	}

	// Read-only calls need no authorization.
	st := f.status(t)
	if st["state"].Value() != "idle" || st["installed"].Value() != "v0.0.5" || st["advanced"].Value() != true {
		t.Fatalf("status %v", st)
	}
	for _, k := range []string{"latest", "pin", "detail"} {
		if _, ok := st[k].Value().(string); !ok {
			t.Fatalf("status key %q is %v, want a string", k, st[k])
		}
	}
	if len(b.calls()) != 0 || b.rebootCount() != 0 || b.previousCount() != 0 {
		t.Fatal("the backend was called for an unauthorized client")
	}
	if n := len(auth.questions()); n != 4 {
		t.Fatalf("Status asked polkit: %d questions", n)
	}
}

func TestBusAuthorizerErrorIsARefusal(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	// A broken authorizer that also says "yes" must still be a no.
	f := startService(t, NewDaemon(b), &fakeAuth{allow: true, err: errors.New("polkit is down")})
	ref := ghcrRepo + ":sway-v0.0.6"
	if got := errName(f.call("Apply", ref, dg[ref], false).Err); got != "io.kairos.HadronUpdate1.Error.NotAuthorized" {
		t.Fatalf("Apply error %q, want NotAuthorized", got)
	}
	if len(b.calls()) != 0 {
		t.Fatal("backend called")
	}
}

func TestBusAppliesWhenAuthorized(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{lines: []string{"pulling", "done"}}
	f := startService(t, NewDaemon(b), &fakeAuth{allow: true})

	sigs := make(chan *dbus.Signal, 64)
	f.client.Signal(sigs)
	if err := f.client.AddMatchSignal(dbus.WithMatchInterface(busInterface)); err != nil {
		t.Fatal(err)
	}

	ref := ghcrRepo + ":sway-v0.0.6"
	if err := f.call("Apply", ref, dg[ref], false).Err; err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st := f.waitBusState(t, StateRebootPending)
	if got, want := b.calls(), []string{"oci:" + ghcrRepo + "@" + dg[ref]}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("backend got %q, want %q", got, want)
	}
	if st["latest"].Value() != "v0.0.6" {
		t.Fatalf("status %v", st)
	}

	// The signals tell the same story: progress lines, then reboot-pending.
	var lines []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-sigs:
			switch s.Name {
			case busInterface + ".Progress":
				lines = append(lines, s.Body[0].(string))
			case busInterface + ".StateChanged":
				d := s.Body[0].(map[string]dbus.Variant)
				if d["state"].Value() == string(StateRebootPending) {
					if strings.Join(lines, ",") != "pulling,done" {
						t.Fatalf("progress %q", lines)
					}
					return
				}
			}
		case <-deadline:
			t.Fatalf("no StateChanged(reboot-pending); progress %q", lines)
		}
	}
}

func TestBusErrorNames(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	b := &fakeBackend{block: block, started: started}
	f := startService(t, NewDaemon(b), &fakeAuth{allow: true})
	ref := ghcrRepo + ":sway-v0.0.6"

	if err := f.call("Apply", ref, dg[ref], false).Err; err != nil {
		t.Fatal(err)
	}
	<-started
	if got := errName(f.call("Apply", ref, dg[ref], false).Err); got != "io.kairos.HadronUpdate1.Error.Busy" {
		t.Fatalf("second Apply: %q, want Busy", got)
	}
	if got := errName(f.call("Cancel").Err); got != "io.kairos.HadronUpdate1.Error.NotCancellable" {
		t.Fatalf("Cancel while applying: %q, want NotCancellable", got)
	}
	if got := errName(f.call("Check").Err); got != "io.kairos.HadronUpdate1.Error.Busy" {
		t.Fatalf("Check while applying: %q, want Busy", got)
	}
	close(block)
	f.waitBusState(t, StateRebootPending)
	if got := errName(f.call("Apply", ref, dg[ref], false).Err); got != "io.kairos.HadronUpdate1.Error.RebootPending" {
		t.Fatalf("Apply while reboot-pending: %q, want RebootPending", got)
	}
	if got := errName(f.call("Apply", "not a ref", dg[ref], false).Err); got != "io.kairos.HadronUpdate1.Error.Failed" {
		t.Fatalf("Apply of a bad ref: %q, want Failed", got)
	}
}

func TestBusErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		name string
	}{
		{ErrBusy, "Busy"},
		{fmt.Errorf("wrapped: %w", ErrRebootPending), "RebootPending"},
		{ErrNotCancellable, "NotCancellable"},
		{fmt.Errorf("x: %w", ErrRebooting), "Rebooting"},
		{ErrAgentImage, "Refused"},
		{ErrNotHadron, "Refused"},
		{ErrNotRelease, "Refused"},
		{errUnfinishedPending, "RebootPending"},
		{ErrNeedsCredentials, "Refused"},
		{ErrWrongPlatform, "Refused"},
		{errUnmanaged, "Refused"},
		{errors.New("network down"), "Failed"},
	} {
		got := busError(tc.err)
		if got == nil || got.Name != busInterface+".Error."+tc.name {
			t.Errorf("busError(%v) = %v, want %s", tc.err, got, tc.name)
			continue
		}
		if len(got.Body) != 1 || got.Body[0] != tc.err.Error() {
			t.Errorf("busError(%v) body %v", tc.err, got.Body)
		}
	}
	if busError(nil) != nil {
		t.Error("busError(nil) is not nil")
	}
}

func TestBusCheckAndReleases(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	auth := &fakeAuth{allow: false}
	f := startService(t, NewDaemon(&fakeBackend{}), auth)
	if err := f.call("Check").Err; err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st := f.status(t); st["state"].Value() != string(StateUpdateAvailable) || st["latest"].Value() != "v0.0.6" {
		t.Fatalf("status %v", st)
	}
	var tags []string
	if err := f.call("Releases").Store(&tags); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "v0.0.6,v0.0.5,v0.0.4" {
		t.Fatalf("releases %q", tags)
	}
	if n := len(auth.questions()); n != 0 {
		t.Fatalf("read-only calls asked polkit %d times", n)
	}
}

func inspect(t *testing.T, f *busFixture, ref string, advanced bool) map[string]dbus.Variant {
	t.Helper()
	var info map[string]dbus.Variant
	if err := f.call("Inspect", ref, advanced).Store(&info); err != nil {
		t.Fatalf("Inspect(%s, %v): %v", ref, advanced, err)
	}
	return info
}

func TestBusInspect(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	auth := &fakeAuth{allow: false}
	f := startService(t, NewDaemon(&fakeBackend{}), auth)

	release := ghcrRepo + ":sway-v0.0.6"
	info := inspect(t, f, release, false)
	for k, want := range map[string]any{
		"ref": release, "digest": dg[release], "os": "linux", "arch": "amd64",
		"flavor": "sway", "version": "v0.0.6", "trusted": true, "hadron": true,
		"refusal": "", "overridable": false,
	} {
		if info[k].Value() != want {
			t.Errorf("release %s = %v, want %v", k, info[k], want)
		}
	}

	// An unlabelled third-party image is refused, but advanced mode may
	// install it; the client asks again with advanced=true.
	info = inspect(t, f, thirdParty, false)
	if info["refusal"].Value() == "" || info["overridable"].Value() != true ||
		info["hadron"].Value() != false || info["trusted"].Value() != false || info["digest"].Value() != dg[thirdParty] {
		t.Fatalf("third party %v", info)
	}
	info = inspect(t, f, thirdParty, true)
	if info["refusal"].Value() != "" || info["hadron"].Value() != false {
		t.Fatalf("third party, advanced %v", info)
	}

	// A labelled image outside the release repository says what it is, but
	// it is not a published release: advanced mode only.
	info = inspect(t, f, labelledThirdParty, false)
	if info["refusal"].Value() != ErrNotRelease.Error() || info["overridable"].Value() != true ||
		info["hadron"].Value() != true || info["trusted"].Value() != false || info["flavor"].Value() != "sway" {
		t.Fatalf("labelled third party %v", info)
	}
	if info = inspect(t, f, labelledThirdParty, true); info["refusal"].Value() != "" || info["hadron"].Value() != true {
		t.Fatalf("labelled third party, advanced %v", info)
	}

	// The agent image is refused whatever the caller asks.
	agent := ghcrRepo + ":agent-v0.0.6"
	info = inspect(t, f, agent, true)
	if info["refusal"].Value() == "" || info["overridable"].Value() != false || info["digest"].Value() != dg[agent] {
		t.Fatalf("agent %v", info)
	}

	// With advanced mode off, nothing is overridable.
	if err := os.WriteFile(os.Getenv("HADRON_UPDATE_CONF"), []byte("advanced=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, adv := range []bool{false, true} {
		info = inspect(t, f, thirdParty, adv)
		if info["refusal"].Value() == "" || info["overridable"].Value() != false {
			t.Fatalf("third party, advanced off, asked %v: %v", adv, info)
		}
	}

	// A reference that does not parse is an error, not a refusal.
	if got := errName(f.call("Inspect", "not a ref", false).Err); got != "io.kairos.HadronUpdate1.Error.Failed" {
		t.Fatalf("bad ref: %q", got)
	}
	if n := len(auth.questions()); n != 0 {
		t.Fatalf("Inspect asked polkit %d times", n)
	}
}

func TestBusRefusesASecondInstance(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	f := startService(t, NewDaemon(&fakeBackend{}), &fakeAuth{})
	// Two daemons would run two jobs at once; the second must fail, not queue.
	err := serve(busConn(t, f.addr), NewDaemon(&fakeBackend{}), &fakeAuth{})
	if err == nil {
		t.Fatal("a second serve on the same bus succeeded")
	}
}

func TestServeExitsWhenIdle(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	old, oldTick := idleTimeout, idleTick
	idleTimeout, idleTick = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { idleTimeout, idleTick = old, oldTick })

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	b := &fakeBackend{block: block, started: started}
	f := startService(t, NewDaemon(b), &fakeAuth{allow: true})
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := f.call("Apply", ref, dg[ref], false).Err; err != nil {
		t.Fatal(err)
	}
	<-started

	// A running job keeps the service up however long it takes.
	select {
	case err := <-f.done:
		t.Fatalf("serve exited while a job ran: %v", err)
	case <-time.After(4 * idleTimeout):
	}
	close(block)

	select {
	case err := <-f.done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve never exited when idle")
	}
	var has bool
	if err := f.client.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, busName).Store(&has); err != nil || has {
		t.Fatalf("name still owned after exit (%v)", err)
	}
}

// fakePolkit is org.freedesktop.PolicyKit1.Authority on a private bus.
type fakePolkit struct {
	mu        sync.Mutex
	result    polkitResult
	err       *dbus.Error
	subject   polkitSubject
	action    string
	details   map[string]string
	flags     uint32
	cancelID  string
	callCount int
}

func (p *fakePolkit) CheckAuthorization(subject polkitSubject, action string, details map[string]string, flags uint32, cancelID string) (polkitResult, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCount++
	p.subject, p.action, p.details, p.flags, p.cancelID = subject, action, details, flags, cancelID
	if p.err != nil {
		return polkitResult{}, p.err
	}
	return p.result, nil
}

func TestPolkitAuthorizer(t *testing.T) {
	addr := privateBus(t)
	pk := busConn(t, addr)
	fake := &fakePolkit{}
	if err := pk.Export(fake, "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority"); err != nil {
		t.Fatal(err)
	}
	if _, err := pk.RequestName("org.freedesktop.PolicyKit1", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	a := polkitAuthorizer{conn: busConn(t, addr)}

	for _, tc := range []struct {
		name   string
		result polkitResult
		err    *dbus.Error
		want   bool
	}{
		{"authorized", polkitResult{IsAuthorized: true, Details: map[string]string{}}, nil, true},
		{"denied", polkitResult{Details: map[string]string{}}, nil, false},
		{"challenge", polkitResult{IsChallenge: true, Details: map[string]string{}}, nil, false},
		{"authorized and challenge", polkitResult{IsAuthorized: true, IsChallenge: true, Details: map[string]string{}}, nil, false},
		{"polkit error", polkitResult{}, dbus.NewError("org.freedesktop.PolicyKit1.Error.Failed", []any{"boom"}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake.mu.Lock()
			fake.result, fake.err = tc.result, tc.err
			fake.mu.Unlock()
			ok, err := a.Allowed(":1.42", actionApply)
			if ok != tc.want {
				t.Fatalf("Allowed = %v (%v), want %v", ok, err, tc.want)
			}
			if (tc.err != nil) != (err != nil) {
				t.Fatalf("err %v", err)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.subject.Kind != "system-bus-name" || fake.subject.Details["name"].Value() != ":1.42" ||
				len(fake.subject.Details) != 1 || fake.action != actionApply ||
				len(fake.details) != 0 || fake.flags != 0 || fake.cancelID != "" {
				t.Fatalf("polkit asked %+v %q %v %d %q", fake.subject, fake.action, fake.details, fake.flags, fake.cancelID)
			}
		})
	}

	// No polkit on the bus at all is a refusal too.
	pk.ReleaseName("org.freedesktop.PolicyKit1")
	if ok, err := a.Allowed(":1.42", actionReboot); ok || err == nil {
		t.Fatalf("no polkit: Allowed = %v, %v", ok, err)
	}
}

func TestDaemonCommandNeedsRoot(t *testing.T) {
	old := needRoot
	needRoot = func() bool { return true }
	t.Cleanup(func() { needRoot = old })
	code, _, stderr := runCLI("", "daemon")
	if code != exitError || !strings.Contains(stderr, "root") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCLI("", "daemon", "--nonsense"); code != exitUsage {
		t.Fatalf("daemon --nonsense: exit %d, want %d", code, exitUsage)
	}
	if _, stdout, _ := runCLI("", "help"); !strings.Contains(stdout, "daemon") {
		t.Fatal("usage does not mention daemon")
	}
}

func TestServeOutlivesALostBusWhileAJobRuns(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	old := idleTick
	idleTick = 10 * time.Millisecond
	t.Cleanup(func() { idleTick = old })

	addr := privateBus(t)
	server, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	d := NewDaemon(&fakeBackend{block: block, started: started})
	done := make(chan error, 1)
	go func() { done <- serve(server, d, &fakeAuth{allow: true}) }()

	client := busConn(t, addr)
	obj := client.Object(busName, busPath)
	ref := ghcrRepo + ":sway-v0.0.6"
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := obj.Call(busInterface+".Apply", 0, ref, dg[ref], false).Err
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Apply: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-started

	// The bus goes away under a running kairos-agent: serve must wait.
	server.Close()
	select {
	case err := <-done:
		t.Fatalf("serve returned while a job ran: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(block)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve returned nil after losing the bus")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve never returned")
	}
}

func TestClosingServiceRefusesCalls(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	s := newService(NewDaemon(&fakeBackend{}), &fakeAuth{allow: true})
	done, err := s.enter()
	if err != nil {
		t.Fatal(err)
	}
	if s.tryClose(0) {
		t.Fatal("closed with a call in progress")
	}
	done()
	if s.tryClose(time.Hour) {
		t.Fatal("closed before the quiet time")
	}
	if !s.tryClose(0) {
		t.Fatal("did not close when idle")
	}
	if _, err := s.enter(); err == nil {
		t.Fatal("a call entered a closing service")
	}
	api := busAPI{s}
	if err := api.Apply(":1.1", ghcrRepo+":sway-v0.0.6", "sha256:"+hex64, false); err == nil || s.d.Busy() {
		t.Fatalf("Apply on a closing service: %v, busy %v", err, s.d.Busy())
	}
}

func TestBusAdmitsOneNetworkCallAtATime(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	hit := make(chan struct{}, 16)
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		<-unblock
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	t.Setenv("HADRON_UPDATE_REGISTRY", srv.URL)
	f := startService(t, NewDaemon(&fakeBackend{}), &fakeAuth{})

	first := make(chan error, 1)
	go func() { first <- f.call("Inspect", thirdParty, false).Err }()
	<-hit // the first Inspect is inside the registry fetch

	for _, c := range []struct {
		method string
		args   []any
	}{
		{"Inspect", []any{thirdParty, false}},
		{"Releases", nil},
		{"Check", nil},
	} {
		if got := errName(f.call(c.method, c.args...).Err); got != "io.kairos.HadronUpdate1.Error.Busy" {
			t.Fatalf("concurrent %s: %q, want Busy", c.method, got)
		}
	}
	// Status reads no network and is always answered.
	f.status(t)

	close(unblock)
	if err := <-first; err == nil {
		t.Fatal("the first Inspect succeeded against a registry that 404s")
	}
	// The slot is free again.
	if err := f.call("Releases").Err; err != nil {
		t.Fatalf("Releases after the first call ended: %v", err)
	}
}

func TestBusReleasesHitGitHubOncePerMinute(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	hits := countingGitHub(t)
	f := startService(t, NewDaemon(&fakeBackend{}), &fakeAuth{})
	for i := 0; i < 2; i++ {
		var tags []string
		if err := f.call("Releases").Store(&tags); err != nil || len(tags) != 3 {
			t.Fatalf("Releases: %q, %v", tags, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d GitHub requests, want 1", n)
	}
}

func TestBusRejectsALongRef(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_REGISTRY", srv.URL)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
	b := &fakeBackend{}
	f := startService(t, NewDaemon(b), &fakeAuth{allow: true})

	long := "ghcr.io/kairos-community/hadron-desktop:" + strings.Repeat("a", 512)
	if got := errName(f.call("Inspect", long, false).Err); got != "io.kairos.HadronUpdate1.Error.Failed" {
		t.Fatalf("Inspect of a long ref: %q", got)
	}
	if got := errName(f.call("Apply", long, dg[ghcrRepo+":sway-v0.0.6"], false).Err); got != "io.kairos.HadronUpdate1.Error.Failed" {
		t.Fatalf("Apply of a long ref: %q", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d network requests for a refused ref", n)
	}
	if st := f.status(t); st["state"].Value() != string(StateIdle) || len(b.calls()) != 0 {
		t.Fatalf("a long ref started something: %v", st)
	}
}

func TestExitingErrorName(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	s := newService(NewDaemon(&fakeBackend{}), &fakeAuth{})
	if !s.tryClose(0) {
		t.Fatal("did not close")
	}
	_, err := s.enter()
	if err == nil || err.Name != "io.kairos.HadronUpdate1.Error.Exiting" {
		t.Fatalf("enter on a closing service: %v", err)
	}
}

// fakeLogind is org.freedesktop.login1.Manager on a private bus.
type fakeLogind struct {
	mu   sync.Mutex
	args []string
}

func (l *fakeLogind) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	l.mu.Lock()
	l.args = []string{what, who, why, mode}
	l.mu.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	w.Close()
	// godbus sends a duplicate; this end stays open in the test process,
	// which is fine for a test.
	return dbus.UnixFD(r.Fd()), nil
}

func TestLogindInhibitor(t *testing.T) {
	addr := privateBus(t)
	lc := busConn(t, addr)
	fake := &fakeLogind{}
	if err := lc.Export(fake, "/org/freedesktop/login1", "org.freedesktop.login1.Manager"); err != nil {
		t.Fatal(err)
	}
	if _, err := lc.RequestName("org.freedesktop.login1", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	inh := logindInhibitor{conn: busConn(t, addr)}
	release, err := inh.Inhibit()
	if err != nil {
		t.Fatalf("Inhibit: %v", err)
	}
	fake.mu.Lock()
	got := strings.Join(fake.args, "|")
	fake.mu.Unlock()
	if want := "shutdown:sleep|Hadron Desktop update|An update is being written to disk|block"; got != want {
		t.Fatalf("Inhibit(%s), want (%s)", got, want)
	}
	release()

	lc.ReleaseName("org.freedesktop.login1")
	if _, err := inh.Inhibit(); err == nil {
		t.Fatal("Inhibit without logind succeeded")
	}
}

func TestBusRebootPrevious(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	auth := &fakeAuth{allow: true}
	f := startService(t, NewDaemon(b), auth)
	if err := f.call("RebootPrevious").Err; err != nil {
		t.Fatalf("RebootPrevious: %v", err)
	}
	if b.previousCount() != 1 || b.rebootCount() != 0 {
		t.Fatalf("RebootPrevious %d, Reboot %d", b.previousCount(), b.rebootCount())
	}
	me := f.client.Names()[0]
	if got := auth.questions(); len(got) != 1 || got[0] != me+" "+actionReboot {
		t.Fatalf("asked %q, want %s", got, actionReboot)
	}
	if got := errName(f.call("RebootPrevious").Err); got != "io.kairos.HadronUpdate1.Error.Rebooting" {
		t.Fatalf("second RebootPrevious: %q, want Rebooting", got)
	}

	// After an install, "previous" is ambiguous: refused by name.
	b2 := &fakeBackend{}
	d2 := NewDaemon(b2)
	f2 := startService(t, d2, &fakeAuth{allow: true})
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d2.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d2, StateRebootPending)
	if got := errName(f2.call("RebootPrevious").Err); got != "io.kairos.HadronUpdate1.Error.RebootPending" {
		t.Fatalf("RebootPrevious while reboot-pending: %q, want RebootPending", got)
	}
	if b2.previousCount() != 0 {
		t.Fatal("backend asked to boot the previous system")
	}
}
