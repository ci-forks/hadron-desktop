package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	ghcrRepo = "ghcr.io/kairos-community/hadron-desktop"
	// thirdParty is served by the same fake registry, but its host makes it a
	// repository other than the release one, so it is never trusted by name.
	thirdParty = "quay.io/kairos-community/hadron-desktop:custom"
	// daemonReleases is the published list: newest v0.0.6.
	daemonReleases = `[{"tag_name":"v0.0.5"},{"tag_name":"v0.0.6"},{"tag_name":"v0.0.4"},{"tag_name":"v0.0.7-rc1","prerelease":true}]`
)

// fakeBackend records what the daemon asks of kairos-agent.
type fakeBackend struct {
	mu      sync.Mutex
	sources []string
	pinSeen []string // the pin file's content when Upgrade was called
	reboots int
	// previous counts RebootPrevious calls; previousErr is what it returns.
	previous    int
	previousErr error

	lines     []string      // progress lines Upgrade prints
	err       error         // what Upgrade returns
	during    func()        // when set, runs inside Upgrade
	rebootErr error         // what Reboot returns
	block     chan struct{} // when set, Upgrade waits for it to close
	started   chan struct{} // when set, receives once per Upgrade call
}

func (b *fakeBackend) Upgrade(ctx context.Context, source string, progress func(string)) error {
	pin, _ := LoadPin()
	b.mu.Lock()
	b.sources = append(b.sources, source)
	b.pinSeen = append(b.pinSeen, pin)
	b.mu.Unlock()
	if b.started != nil {
		b.started <- struct{}{}
	}
	for _, l := range b.lines {
		progress(l)
	}
	if b.during != nil {
		b.during()
	}
	if b.block != nil {
		<-b.block
	}
	return b.err
}

func (b *fakeBackend) Reboot() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reboots++
	return b.rebootErr
}

func (b *fakeBackend) RebootPrevious() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.previous++
	return b.previousErr
}

func (b *fakeBackend) previousCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.previous
}

func (b *fakeBackend) rebootCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reboots
}

func (b *fakeBackend) calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sources...)
}

// daemonDigests are the digests the fake registry serves, by ref.
type daemonDigests map[string]string

// daemonEnv points every seam at temp files and fakes: release file with the
// installed version, pin, update.conf, run dir, GitHub (releases) and a
// registry holding the release images plus an unlabelled third-party image.
func daemonEnv(t *testing.T, installed string) daemonDigests {
	t.Helper()
	t.Setenv("HADRON_UPDATE_RELEASE_FILE",
		writeRelease(t, strings.Replace(goodRelease, `"v0.1.0"`, installed, 1)))
	dir := t.TempDir()
	t.Setenv("HADRON_UPDATE_PIN_FILE", filepath.Join(dir, "pin"))
	t.Setenv("HADRON_UPDATE_CONF", filepath.Join(dir, "update.conf"))
	t.Setenv("HADRON_UPDATE_RUN_DIR", filepath.Join(dir, "run"))
	fakeGitHubReleases(t, "v0.0.6", daemonReleases)

	reg := newFakeRegistry(t, false)
	d := daemonDigests{}
	for _, tag := range []string{"sway-v0.0.6", "sway-v0.0.5", "niri-v0.0.6", "agent-v0.0.6"} {
		d[ghcrRepo+":"+tag] = reg.image(t, "hadron-desktop", tag, "amd64", nil, false)
	}
	d[thirdParty] = reg.image(t, "hadron-desktop", "custom", "amd64", map[string]string{"x": "y"}, false)
	return d
}

func waitState(t *testing.T, d *Daemon, want State) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := d.Status()
		if s.State == want {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %q (detail %q), want %q", s.State, s.Detail, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCheckFindsUpdate(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	d := NewDaemon(&fakeBackend{})
	if s := d.Status(); s.State != StateIdle || s.Installed != "v0.0.5" || !s.Advanced {
		t.Fatalf("initial status %+v", s)
	}
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := d.Status(); s.State != StateUpdateAvailable || s.Latest != "v0.0.6" {
		t.Fatalf("status %+v", s)
	}
}

func TestCheckUpToDate(t *testing.T) {
	daemonEnv(t, "v0.0.6")
	d := NewDaemon(&fakeBackend{})
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := d.Status(); s.State != StateUpToDate || s.Latest != "v0.0.6" {
		t.Fatalf("status %+v", s)
	}
}

func TestCheckPinnedStaysQuiet(t *testing.T) {
	daemonEnv(t, "v0.0.4")
	if err := SavePin(ghcrRepo + ":sway-v0.0.4"); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(&fakeBackend{})
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := d.Status()
	if s.State != StateUpToDate || s.Latest != "v0.0.6" || s.Pin != ghcrRepo+":sway-v0.0.4" {
		t.Fatalf("status %+v", s)
	}
}

func TestUnmanagedWhenNoReleaseFile(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
	t.Setenv("HADRON_UPDATE_REGISTRY", srv.URL)
	t.Setenv("HADRON_UPDATE_RELEASE_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("HADRON_UPDATE_RUN_DIR", t.TempDir())

	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if s := d.Status(); s.State != StateUnmanaged {
		t.Fatalf("status %+v", s)
	}
	if err := d.Apply(context.Background(), ghcrRepo+":sway-v0.0.6", "sha256:"+hex64, false); err == nil {
		t.Fatal("Apply on an unmanaged system succeeded")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d HTTP calls, want 0", n)
	}
	if len(b.calls()) != 0 {
		t.Fatal("backend called")
	}
}

func TestApplyUsesTheDigest(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{lines: []string{"pulling", "done"}}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateRebootPending)
	want := "oci:ghcr.io/kairos-community/hadron-desktop@" + dg[ref]
	if got := b.calls(); len(got) != 1 || got[0] != want {
		t.Fatalf("sources %q, want [%q]", got, want)
	}
	if s.Detail != "done" {
		t.Fatalf("detail %q", s.Detail)
	}
}

func TestApplyRefusesWhileBusy(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{block: make(chan struct{})}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateApplying)
	if err := d.Apply(context.Background(), ref, dg[ref], false); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Apply: %v, want ErrBusy", err)
	}
	if err := d.Check(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("Check while applying: %v, want ErrBusy", err)
	}
	if err := d.Reboot(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reboot while applying: %v, want ErrBusy", err)
	}
	close(b.block)
	waitState(t, d, StateRebootPending)
	if n := len(b.calls()); n != 1 {
		t.Fatalf("%d upgrades, want 1", n)
	}
}

func TestApplyRefusesWhileRebootPending(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRebootPending)
	if err := d.Apply(context.Background(), ref, dg[ref], false); !errors.Is(err, ErrRebootPending) {
		t.Fatalf("Apply: %v, want ErrRebootPending", err)
	}
	// A check still learns about releases but does not leave reboot-pending.
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := d.Status(); s.State != StateRebootPending {
		t.Fatalf("after Check: %+v", s)
	}

	// The daemon exits when idle; the next one must still refuse.
	d2 := NewDaemon(b)
	if s := d2.Status(); s.State != StateRebootPending {
		t.Fatalf("restarted daemon: %+v", s)
	}
	if err := d2.Apply(context.Background(), ref, dg[ref], false); !errors.Is(err, ErrRebootPending) {
		t.Fatalf("restarted Apply: %v, want ErrRebootPending", err)
	}
	if n := len(b.calls()); n != 1 {
		t.Fatalf("%d upgrades, want 1", n)
	}
	if err := d2.Reboot(); err != nil || b.reboots != 1 {
		t.Fatalf("Reboot: %v, %d reboots", err, b.reboots)
	}
}

func TestApplyDigestMismatch(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.Apply(context.Background(), ghcrRepo+":sway-v0.0.6", "sha256:"+hex64, false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateError)
	if !strings.Contains(s.Detail, "digest") {
		t.Fatalf("detail %q does not mention the digest", s.Detail)
	}
	d.wg.Wait()
	if len(b.calls()) != 0 {
		t.Fatal("backend called on a digest mismatch")
	}
}

func TestApplyRejectsMalformedInput(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	for _, c := range []struct{ ref, digest string }{
		{"not a ref", dg[ref]},
		{ref, ""},
		{ref, "sha256:short"},
		{ref, "sha512:" + hex64},
	} {
		if err := d.Apply(context.Background(), c.ref, c.digest, false); err == nil {
			t.Errorf("Apply(%q, %q) accepted", c.ref, c.digest)
		}
	}
	if s := d.Status(); s.State != StateIdle {
		t.Fatalf("status %+v", s)
	}
}

func TestApplyPinRules(t *testing.T) {
	for _, c := range []struct {
		name     string
		ref      string
		advanced bool
		fail     bool
		wantPin  string // "" means cleared; "old" means untouched
	}{
		{"latest clears", ghcrRepo + ":sway-v0.0.6", false, false, ""},
		{"older pins", ghcrRepo + ":sway-v0.0.5", false, false, ghcrRepo + ":sway-v0.0.5"},
		{"advanced pins", thirdParty, true, false, thirdParty},
		{"other flavor latest clears", ghcrRepo + ":niri-v0.0.6", false, false, ""},
		{"failure leaves the pin", ghcrRepo + ":sway-v0.0.5", false, true, "old"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dg := daemonEnv(t, "v0.0.5")
			if err := SavePin("old"); err != nil {
				t.Fatal(err)
			}
			b := &fakeBackend{}
			if c.fail {
				b.err = errors.New("exit status 1")
			}
			d := NewDaemon(b)
			if err := d.Apply(context.Background(), c.ref, dg[c.ref], c.advanced); err != nil {
				t.Fatal(err)
			}
			want := StateRebootPending
			if c.fail {
				want = StateError
			}
			waitState(t, d, want)
			if b.pinSeen[0] != "old" {
				t.Fatalf("pin changed before the upgrade ran: %q", b.pinSeen[0])
			}
			got, err := LoadPin()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.wantPin {
				t.Fatalf("pin %q, want %q", got, c.wantPin)
			}
			if s := d.Status(); s.Pin != c.wantPin {
				t.Fatalf("Status().Pin %q, want %q", s.Pin, c.wantPin)
			}
		})
	}
}

func TestAdvancedOverridesOnlyNotHadron(t *testing.T) {
	agent := ghcrRepo + ":agent-v0.0.6"
	for _, c := range []struct {
		name     string
		ref      string
		advanced bool
		conf     string
		refusal  error // nil: installed
	}{
		{"unlabelled third-party, advanced", thirdParty, true, "", nil},
		{"unlabelled third-party, not advanced", thirdParty, false, "", ErrNotHadron},
		{"agent image, advanced", agent, true, "", ErrAgentImage},
		{"advanced turned off", thirdParty, true, "advanced=false\n", ErrNotHadron},
	} {
		t.Run(c.name, func(t *testing.T) {
			dg := daemonEnv(t, "v0.0.5")
			if c.conf != "" {
				if err := os.WriteFile(os.Getenv("HADRON_UPDATE_CONF"), []byte(c.conf), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			b := &fakeBackend{}
			d := NewDaemon(b)

			_, _, ierr := d.Inspect(context.Background(), c.ref, c.advanced)
			if !errors.Is(ierr, c.refusal) {
				t.Fatalf("Inspect: %v, want %v", ierr, c.refusal)
			}

			if err := d.Apply(context.Background(), c.ref, dg[c.ref], c.advanced); err != nil {
				t.Fatal(err)
			}
			if c.refusal == nil {
				waitState(t, d, StateRebootPending)
				want := "oci:quay.io/kairos-community/hadron-desktop@" + dg[c.ref]
				if got := b.calls(); len(got) != 1 || got[0] != want {
					t.Fatalf("sources %q, want [%q]", got, want)
				}
				return
			}
			s := waitState(t, d, StateError)
			if !strings.Contains(s.Detail, c.refusal.Error()) {
				t.Fatalf("detail %q, want %q", s.Detail, c.refusal)
			}
			d.wg.Wait()
			if len(b.calls()) != 0 {
				t.Fatal("backend called for a refused image")
			}
		})
	}
}

func TestInspectReturnsTheVerdict(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	d := NewDaemon(&fakeBackend{})
	ref := ghcrRepo + ":niri-v0.0.6"
	info, v, err := d.Inspect(context.Background(), ref, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Digest != dg[ref] || v != (Verdict{Flavor: "niri", Version: "v0.0.6", Trusted: true, Hadron: true}) {
		t.Fatalf("got %+v, %+v", info, v)
	}
	if s := d.Status(); s.State != StateIdle {
		t.Fatalf("Inspect changed the state: %+v", s)
	}
}

func TestReleases(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	d := NewDaemon(&fakeBackend{})
	rels, err := d.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, r := range rels {
		tags = append(tags, r.Tag)
	}
	if got := strings.Join(tags, ","); got != "v0.0.6,v0.0.5,v0.0.4" {
		t.Fatalf("releases %s", got)
	}
}

// blockingGitHub answers nothing until the client gives up, so a job sits in
// its checking or inspecting phase until it is cancelled. arrived receives
// once the request is being served.
func blockingGitHub(t *testing.T) (arrived chan struct{}) {
	t.Helper()
	arrived = make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
	return arrived
}

func TestCancelOnlyBeforeUpgrade(t *testing.T) {
	t.Run("inspecting", func(t *testing.T) {
		dg := daemonEnv(t, "v0.0.5")
		b := &fakeBackend{}
		d := NewDaemon(b)
		if err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		// The job must reach GitHub again to block there, not reuse the
		// list Check fetched.
		noReleaseCache(t)
		arrived := blockingGitHub(t)
		ref := ghcrRepo + ":sway-v0.0.6"
		if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
			t.Fatal(err)
		}
		<-arrived // the job is blocked listing releases
		if s := d.Status(); s.State != StateInspecting {
			t.Fatalf("status %+v", s)
		}
		if err := d.Cancel(); err != nil {
			t.Fatal(err)
		}
		if s := d.Status(); s.State != StateUpdateAvailable {
			t.Fatalf("after cancel: %+v", s)
		}
		d.wg.Wait() // returns only because the cancel reached the request
		if s := d.Status(); s.State != StateUpdateAvailable {
			t.Fatalf("after the job ended: %+v", s)
		}
		if len(b.calls()) != 0 {
			t.Fatal("backend called after cancel")
		}
		if err := d.Cancel(); err == nil {
			t.Fatal("Cancel with no job succeeded")
		}
	})

	t.Run("checking", func(t *testing.T) {
		daemonEnv(t, "v0.0.5")
		arrived := blockingGitHub(t)
		d := NewDaemon(&fakeBackend{})
		done := make(chan error, 1)
		go func() { done <- d.Check(context.Background()) }()
		<-arrived
		if s := d.Status(); s.State != StateChecking {
			t.Fatalf("status %+v", s)
		}
		if err := d.Cancel(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err == nil {
			t.Fatal("cancelled Check returned nil")
		}
		if s := d.Status(); s.State != StateIdle {
			t.Fatalf("after cancel: %+v", s)
		}
	})

	t.Run("applying", func(t *testing.T) {
		dg := daemonEnv(t, "v0.0.5")
		b := &fakeBackend{block: make(chan struct{})}
		d := NewDaemon(b)
		ref := ghcrRepo + ":sway-v0.0.6"
		if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
			t.Fatal(err)
		}
		waitState(t, d, StateApplying)
		if err := d.Cancel(); !errors.Is(err, ErrNotCancellable) {
			t.Fatalf("Cancel: %v, want ErrNotCancellable", err)
		}
		close(b.block)
		waitState(t, d, StateRebootPending)
	})
}

func TestBackendFailureGoesToError(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{lines: []string{"pulling image", "error: no space left on device"}, err: errors.New("exit status 1")}
	d := NewDaemon(b)
	var mu sync.Mutex
	var progress []string
	d.OnProgress(func(l string) { mu.Lock(); progress = append(progress, l); mu.Unlock() })
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateError)
	if s.Detail != "error: no space left on device" {
		t.Fatalf("detail %q", s.Detail)
	}
	// A failed upgrade leaves no reboot-pending marker, and may be retried.
	if s := NewDaemon(b).Status(); s.State != StateIdle {
		t.Fatalf("restarted daemon: %+v", s)
	}
	b.err = nil
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitState(t, d, StateRebootPending)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(progress)
		mu.Unlock()
		if n == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress lines %q", progress)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCallbacksMayCallTheDaemon(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	d := NewDaemon(&fakeBackend{lines: []string{"a"}})
	var mu sync.Mutex
	var states []State
	d.OnChange(func(s Status) {
		_ = d.Status() // would deadlock if called under the daemon's lock
		mu.Lock()
		states = append(states, s.State)
		mu.Unlock()
	})
	d.OnProgress(func(string) { _ = d.Status() })
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRebootPending)
	want := fmt.Sprint([]State{StateInspecting, StateApplying, StateRebootPending})
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := fmt.Sprint(states)
		mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("states %s, want %s", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestKairosBackendMergesOutput runs a stand-in for kairos-agent: its stdout
// and stderr arrive as one stream of lines, \r redraws included.
func TestKairosBackendMergesOutput(t *testing.T) {
	var _ Backend = kairosBackend{}
	dir := t.TempDir()
	agent := filepath.Join(dir, "kairos-agent")
	script := "#!/bin/sh\necho \"$@\"\necho oops >&2\nprintf '10%%\\r20%%\\n'\nexit ${CODE:-0}\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HADRON_UPDATE_AGENT", agent)

	var lines []string
	err := kairosBackend{}.Upgrade(context.Background(), "oci:x@sha256:"+hex64, func(l string) { lines = append(lines, l) })
	want := "upgrade --source oci:x@sha256:" + hex64 + "|oops|10%|20%"
	if err != nil || strings.Join(lines, "|") != want {
		t.Fatalf("got %q, %v; want %q", lines, err, want)
	}

	t.Setenv("CODE", "3")
	if err := (kairosBackend{}).Upgrade(context.Background(), "oci:x", func(string) {}); err == nil {
		t.Fatal("a failing kairos-agent returned nil")
	}
}

func TestApplyRefusesWithoutAMarker(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	// A dangling symlink: the marker reads as absent, so the daemon starts
	// idle, but the directory cannot be created, even by root.
	run := filepath.Join(t.TempDir(), "run")
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "run"), run); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HADRON_UPDATE_RUN_DIR", run)
	b := &fakeBackend{}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateError)
	if !strings.Contains(s.Detail, "pending install") {
		t.Fatalf("detail %q", s.Detail)
	}
	d.wg.Wait()
	if len(b.calls()) != 0 {
		t.Fatal("backend called without a reboot-pending marker")
	}
}

func TestMarkerIsWrittenBeforeTheUpgrade(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{block: make(chan struct{})}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateApplying)

	// The daemon dies mid-install and a new one starts.
	d2 := NewDaemon(b)
	if s := d2.Status(); s.State != StateRebootPending {
		t.Fatalf("daemon started mid-install: %+v", s)
	}
	if err := d2.Apply(context.Background(), ref, dg[ref], false); !errors.Is(err, ErrRebootPending) {
		t.Fatalf("Apply mid-install: %v, want ErrRebootPending", err)
	}
	got, err := os.ReadFile(filepath.Join(os.Getenv("HADRON_UPDATE_RUN_DIR"), "reboot-pending"))
	if want := "oci:" + ghcrRepo + "@" + dg[ref] + "\n"; err != nil || string(got) != want {
		t.Fatalf("marker %q, %v; want %q", got, err, want)
	}
	close(b.block)
	waitState(t, d, StateRebootPending)
	if n := len(b.calls()); n != 1 {
		t.Fatalf("%d upgrades, want 1", n)
	}
}

func TestFailedUpgradeKeepsAMarkerItCannotRemove(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	marker := filepath.Join(os.Getenv("HADRON_UPDATE_RUN_DIR"), "reboot-pending")
	b := &fakeBackend{err: errors.New("exit status 1"), during: func() {
		// A non-empty directory in its place makes os.Remove fail.
		os.Remove(marker)
		os.MkdirAll(filepath.Join(marker, "x"), 0o755)
	}}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateRebootPending)
	if !strings.Contains(s.Detail, "could not be removed") {
		t.Fatalf("detail %q", s.Detail)
	}
	if s := NewDaemon(b).Status(); s.State != StateRebootPending {
		t.Fatalf("restarted daemon: %+v", s)
	}
}

func TestRebootRefusedWhileAJobRuns(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	arrived := blockingGitHub(t)
	b := &fakeBackend{}
	d := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	<-arrived
	if s := d.Status(); s.State != StateInspecting {
		t.Fatalf("status %+v", s)
	}
	if err := d.Reboot(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reboot while inspecting: %v, want ErrBusy", err)
	}
	if n := b.rebootCount(); n != 0 {
		t.Fatalf("%d reboots, want 0", n)
	}
	if err := d.Cancel(); err != nil {
		t.Fatal(err)
	}
	d.wg.Wait()
	if len(b.calls()) != 0 {
		t.Fatal("backend upgraded")
	}
}

func TestNoApplyAfterReboot(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	ref := ghcrRepo + ":sway-v0.0.6"

	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.Reboot(); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), ref, dg[ref], false); err == nil {
		t.Fatal("Apply after Reboot accepted")
	}
	if len(b.calls()) != 0 || b.rebootCount() != 1 {
		t.Fatalf("upgrades %q, reboots %d", b.calls(), b.rebootCount())
	}

	// A reboot that fails does not lock installs out.
	b2 := &fakeBackend{rebootErr: errors.New("systemctl reboot: exit status 1")}
	d2 := NewDaemon(b2)
	if err := d2.Reboot(); err == nil {
		t.Fatal("failed Reboot returned nil")
	}
	if err := d2.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatalf("Apply after a failed Reboot: %v", err)
	}
	waitState(t, d2, StateRebootPending)
}

func TestKairosBackendReportsAnUnreadableLine(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "kairos-agent")
	script := "#!/bin/sh\nhead -c 2000000 /dev/zero | tr '\\0' a\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HADRON_UPDATE_AGENT", agent)
	var lines []string
	err := kairosBackend{}.Upgrade(context.Background(), "oci:x", func(l string) { lines = append(lines, l) })
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "stopped reading") {
		t.Fatalf("got %q, %v", lines, err)
	}
}

// noReleaseCache makes every release listing go to GitHub.
func noReleaseCache(t *testing.T) {
	t.Helper()
	old := releasesTTL
	releasesTTL = 0
	t.Cleanup(func() { releasesTTL = old })
}

// countingGitHub serves daemonReleases and counts the list requests.
func countingGitHub(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kairos-community/hadron-desktop/releases" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, daemonReleases)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
	return &hits
}

func TestReleasesAreCachedForAMinute(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	hits := countingGitHub(t)
	d := NewDaemon(&fakeBackend{})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := d.Releases(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Inspect(ctx, ghcrRepo+":sway-v0.0.6", false); err != nil {
		t.Fatal(err)
	}
	_ = dg
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d GitHub requests within the cache time, want 1", n)
	}

	noReleaseCache(t)
	if _, err := d.Releases(ctx); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d GitHub requests after the cache expired, want 2", n)
	}
}

func TestReleasesErrorIsNotCached(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	var fail atomic.Bool
	fail.Store(true)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, daemonReleases)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HADRON_UPDATE_API", srv.URL)
	d := NewDaemon(&fakeBackend{})
	if _, err := d.Releases(context.Background()); err == nil {
		t.Fatal("no error from a failing GitHub")
	}
	fail.Store(false)
	if rels, err := d.Releases(context.Background()); err != nil || len(rels) == 0 {
		t.Fatalf("after recovery: %v, %v", rels, err)
	}
}

// fakeInhibitor records whether a shutdown lock is held.
type fakeInhibitor struct {
	mu                 sync.Mutex
	held               bool
	acquired, released int
	err                error
}

func (f *fakeInhibitor) Inhibit() (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.held = true
	f.acquired++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.held = false
		f.released++
	}, nil
}

func (f *fakeInhibitor) state() (held bool, acquired, released int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held, f.acquired, f.released
}

func TestApplyHoldsAShutdownInhibitorWhileWriting(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("upgrade fails %v", fail), func(t *testing.T) {
			dg := daemonEnv(t, "v0.0.5")
			inh := &fakeInhibitor{}
			var heldDuring atomic.Bool
			b := &fakeBackend{during: func() {
				h, _, _ := inh.state()
				heldDuring.Store(h)
			}}
			if fail {
				b.err = errors.New("disk full")
			}
			d := NewDaemon(b)
			d.inhibitor = inh
			ref := ghcrRepo + ":sway-v0.0.6"
			if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
				t.Fatal(err)
			}
			d.wg.Wait()
			if !heldDuring.Load() {
				t.Fatal("no inhibitor held while kairos-agent ran")
			}
			if held, acq, rel := inh.state(); held || acq != 1 || rel != 1 {
				t.Fatalf("held %v, acquired %d, released %d", held, acq, rel)
			}
		})
	}
}

func TestApplyRunsWithoutAnInhibitor(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	d.inhibitor = &fakeInhibitor{err: errors.New("no logind")}
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRebootPending)
	if len(b.calls()) != 1 {
		t.Fatal("a missing inhibitor stopped the upgrade")
	}
}

func TestRebootPreviousCallsTheBackendOnce(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	ref := ghcrRepo + ":sway-v0.0.6"
	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.RebootPrevious(); err != nil {
		t.Fatal(err)
	}
	if b.previousCount() != 1 || b.rebootCount() != 0 {
		t.Fatalf("RebootPrevious %d, Reboot %d; want 1, 0", b.previousCount(), b.rebootCount())
	}
	// The system is going down: nothing may start kairos-agent, and a second
	// rollback is not a second selection.
	if err := d.Apply(context.Background(), ref, dg[ref], false); !errors.Is(err, ErrRebooting) {
		t.Fatalf("Apply after RebootPrevious: %v, want ErrRebooting", err)
	}
	if err := d.RebootPrevious(); !errors.Is(err, ErrRebooting) {
		t.Fatalf("second RebootPrevious: %v, want ErrRebooting", err)
	}
	if b.previousCount() != 1 || len(b.calls()) != 0 {
		t.Fatalf("RebootPrevious %d, upgrades %q", b.previousCount(), b.calls())
	}
}

func TestRebootPreviousRefusedAfterReboot(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.Reboot(); err != nil {
		t.Fatal(err)
	}
	// A plain restart is under way; selecting the passive entry now would
	// change what it boots behind the user's back.
	if err := d.RebootPrevious(); !errors.Is(err, ErrRebooting) {
		t.Fatalf("RebootPrevious after Reboot: %v, want ErrRebooting", err)
	}
	if b.previousCount() != 0 {
		t.Fatal("backend asked to boot the previous system")
	}
}

func TestRebootPreviousRefusedWhileAJobRuns(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	ref := ghcrRepo + ":sway-v0.0.6"

	// Inspecting: the job could still start kairos-agent.
	arrived := blockingGitHub(t)
	b := &fakeBackend{block: make(chan struct{})}
	d := NewDaemon(b)
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	<-arrived
	if err := d.RebootPrevious(); !errors.Is(err, ErrBusy) {
		t.Fatalf("RebootPrevious while inspecting: %v, want ErrBusy", err)
	}
	if err := d.Cancel(); err != nil {
		t.Fatal(err)
	}
	d.wg.Wait()

	// Applying: kairos-agent writes the passive slot.
	fakeGitHubReleases(t, "v0.0.6", daemonReleases)
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateApplying)
	if err := d.RebootPrevious(); !errors.Is(err, ErrBusy) {
		t.Fatalf("RebootPrevious while applying: %v, want ErrBusy", err)
	}
	close(b.block)
	waitState(t, d, StateRebootPending)
	if b.previousCount() != 0 {
		t.Fatal("backend asked to boot the previous system")
	}
}

func TestRebootPreviousRefusedWhileRebootPending(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	ref := ghcrRepo + ":sway-v0.0.6"
	b := &fakeBackend{}
	d := NewDaemon(b)
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRebootPending)
	err := d.RebootPrevious()
	if !errors.Is(err, ErrRebootPending) || !strings.Contains(err.Error(), "restart into it first") {
		t.Fatalf("RebootPrevious while reboot-pending: %v, want ErrRebootPending with a reason", err)
	}
	// The marker outlives the daemon, and so does the refusal.
	if err := NewDaemon(b).RebootPrevious(); !errors.Is(err, ErrRebootPending) {
		t.Fatalf("restarted daemon: %v, want ErrRebootPending", err)
	}
	if b.previousCount() != 0 {
		t.Fatal("backend asked to boot the previous system")
	}
	// A plain restart into the update is still allowed.
	if err := d.Reboot(); err != nil || b.rebootCount() != 1 {
		t.Fatalf("Reboot: %v, %d reboots", err, b.rebootCount())
	}
}

func TestFailedRebootPreviousAllowsInstalls(t *testing.T) {
	dg := daemonEnv(t, "v0.0.5")
	ref := ghcrRepo + ":sway-v0.0.6"
	b := &fakeBackend{previousErr: errors.New("kairos-agent bootentry: exit status 1")}
	d := NewDaemon(b)
	if err := d.RebootPrevious(); err == nil {
		t.Fatal("a failed RebootPrevious returned nil")
	}
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatalf("Apply after a failed RebootPrevious: %v", err)
	}
	waitState(t, d, StateRebootPending)
}

// fakeRebootTools puts a kairos-agent (through HADRON_UPDATE_AGENT) and a
// systemctl (first on PATH) in place that append their argv to one log, so
// a test sees both commands and their order. agentCode is kairos-agent's exit
// status. cosDir is the boot-mode sentinel directory, empty.
func fakeRebootTools(t *testing.T, agentCode int) (logFile, cosDir string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "log")
	agent := filepath.Join(dir, "kairos-agent")
	script := fmt.Sprintf("#!/bin/sh\necho \"kairos-agent $*\" >> %s\nexit %d\n", logFile, agentCode)
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := fmt.Sprintf("#!/bin/sh\necho \"systemctl $*\" >> %s\n", logFile)
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(sc), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HADRON_UPDATE_AGENT", agent)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cosDir = filepath.Join(dir, "cos")
	if err := os.Mkdir(cosDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := bootModeDir
	bootModeDir = cosDir
	t.Cleanup(func() { bootModeDir = old })
	return logFile, cosDir
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestKairosBackendRebootPrevious pins the command line: kairos-agent's own
// one-shot selection of the "fallback" (passive) entry, then the reboot.
func TestKairosBackendRebootPrevious(t *testing.T) {
	logFile, _ := fakeRebootTools(t, 0)
	if err := (kairosBackend{}).RebootPrevious(); err != nil {
		t.Fatal(err)
	}
	want := "kairos-agent bootentry --select fallback\nsystemctl reboot\n"
	if got := readLog(t, logFile); got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

func TestKairosBackendRebootPreviousStopsWhenTheSelectionFails(t *testing.T) {
	logFile, _ := fakeRebootTools(t, 1)
	err := (kairosBackend{}).RebootPrevious()
	if err == nil || !strings.Contains(err.Error(), "bootentry") {
		t.Fatalf("err %v, want the bootentry failure", err)
	}
	// No reboot: it would only boot the same system again.
	if got := readLog(t, logFile); got != "kairos-agent bootentry --select fallback\n" {
		t.Fatalf("ran %q", got)
	}
}

func TestKairosBackendRebootPreviousRefusesOnThePassiveSystem(t *testing.T) {
	logFile, cosDir := fakeRebootTools(t, 0)
	if err := os.WriteFile(filepath.Join(cosDir, "passive_mode"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := (kairosBackend{}).RebootPrevious()
	if err == nil || !strings.Contains(err.Error(), "already runs the previous system") {
		t.Fatalf("err %v", err)
	}
	if got := readLog(t, logFile); got != "" {
		t.Fatalf("ran %q, want nothing", got)
	}
}
