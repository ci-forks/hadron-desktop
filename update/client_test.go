package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeDaemon is daemonClient without a bus. Apply feeds script to the watcher.
type fakeDaemon struct {
	status     Status
	releases   []string
	info       InspectInfo
	inspectEr  []error // returned in order before success
	applyErr   error
	watchErr   error
	closeAfter bool // close the channel after the script, as when the daemon dies
	script     []Event

	calls   []string
	events  chan Event
	applied [3]string
}

func (f *fakeDaemon) rec(s string) { f.calls = append(f.calls, s) }
func (f *fakeDaemon) called(s string) bool {
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}
func (f *fakeDaemon) Status() (Status, error) { f.rec("Status"); return f.status, nil }
func (f *fakeDaemon) Check() error            { f.rec("Check"); return nil }
func (f *fakeDaemon) Releases() ([]string, error) {
	f.rec("Releases")
	return f.releases, nil
}
func (f *fakeDaemon) Inspect(ref string, adv bool) (InspectInfo, error) {
	f.rec("Inspect")
	if len(f.inspectEr) > 0 {
		e := f.inspectEr[0]
		f.inspectEr = f.inspectEr[1:]
		return InspectInfo{}, e
	}
	i := f.info
	i.Ref = ref
	return i, nil
}
func (f *fakeDaemon) Apply(ref, digest string, adv bool) error {
	f.rec("Apply")
	f.applied = [3]string{ref, digest, map[bool]string{true: "adv", false: ""}[adv]}
	if f.applyErr != nil {
		return f.applyErr
	}
	for _, e := range f.script {
		f.events <- e
	}
	if f.closeAfter {
		close(f.events)
	}
	return nil
}
func (f *fakeDaemon) Cancel() error { f.rec("Cancel"); return nil }
func (f *fakeDaemon) Reboot() error { f.rec("Reboot"); return nil }
func (f *fakeDaemon) Watch() (<-chan Event, func(), error) {
	if f.watchErr != nil {
		return nil, nil, f.watchErr
	}
	f.events = make(chan Event, 16)
	return f.events, func() {}, nil
}
func (f *fakeDaemon) Close() {}

func useDaemon(t *testing.T, f *fakeDaemon) {
	t.Helper()
	old, oldWait := dialDaemon, busyWait
	dialDaemon = func() (daemonClient, error) { return f, nil }
	busyWait = 0
	t.Cleanup(func() { dialDaemon, busyWait = old, oldWait })
}

const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func okDaemon() *fakeDaemon {
	return &fakeDaemon{
		status:   Status{Advanced: true},
		releases: []string{"v0.1.0", "v0.2.0"},
		info:     InspectInfo{Digest: digestA, Flavor: "sway", Version: "v0.2.0", Trusted: true},
		script: []Event{
			{Line: "pulling"}, {Status: &Status{State: StateInspecting}}, {Status: &Status{State: StateApplying}},
			{Status: &Status{State: StateRebootPending}},
		},
	}
}

func TestUpgradeThroughTheDaemon(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	code, out, errOut := runCLI("y\ny\n", "upgrade")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if f.applied[0] != "ghcr.io/kairos-community/hadron-desktop:sway-v0.2.0" || f.applied[1] != digestA || f.applied[2] != "" {
		t.Fatalf("Apply got %v", f.applied)
	}
	for _, want := range []string{"ghcr.io/kairos-community/hadron-desktop", digestA, "sway", "v0.2.0", "trusted", "pulling", "Reboot now?"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
	if !f.called("Reboot") {
		t.Fatal("answered yes to Reboot now? but no Reboot call")
	}
}

func TestUpgradeDaemonRebootDefaultsToNo(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	if code, _, _ := runCLI("", "upgrade", "--yes"); code != exitOK || f.called("Reboot") {
		t.Fatalf("code %d, calls %v", code, f.calls)
	}
	f = okDaemon()
	useDaemon(t, f)
	if code, _, _ := runCLI("", "upgrade", "--yes", "--reboot"); code != exitOK || !f.called("Reboot") {
		t.Fatalf("--reboot: code %d, calls %v", code, f.calls)
	}
}

func TestUpgradeDaemonUpToDate(t *testing.T) {
	setup(t, "v0.2.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	code, out, _ := runCLI("", "upgrade", "--yes")
	if code != exitOK || !strings.Contains(out, "up to date") || f.called("Apply") {
		t.Fatalf("code %d, out %q, calls %v", code, out, f.calls)
	}
}

func TestUpgradeDaemonVersion(t *testing.T) {
	setup(t, "v0.2.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	if code, _, e := runCLI("", "upgrade", "--yes", "--version", "v0.1.0"); code != exitOK {
		t.Fatalf("code %d %s", code, e)
	}
	if !strings.HasSuffix(f.applied[0], ":sway-v0.1.0") {
		t.Fatalf("Apply ref %q", f.applied[0])
	}
}

func TestUpgradeImageNeedsAdvancedMode(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.status.Advanced = false
	useDaemon(t, f)
	code, _, errOut := runCLI("", "upgrade", "--yes", "--image", "ghcr.io/x/y:t")
	if code != exitUsage || !strings.Contains(errOut, "advanced mode is off") || f.called("Inspect") {
		t.Fatalf("code %d, stderr %q, calls %v", code, errOut, f.calls)
	}
}

func TestUpgradeImageIsAdvanced(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	if code, _, e := runCLI("", "upgrade", "--yes", "--image", "ghcr.io/x/y:t"); code != exitOK {
		t.Fatalf("code %d %s", code, e)
	}
	if f.applied[0] != "ghcr.io/x/y:t" || f.applied[2] != "adv" {
		t.Fatalf("Apply got %v", f.applied)
	}
}

func TestUnlabelledImageAsksASecondQuestionDefaultingToNo(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.info = InspectInfo{Digest: digestA, Refusal: "not a Hadron Desktop image", Overridable: true}
	useDaemon(t, f)
	// --yes must not answer the second question; EOF means the default: no.
	code, out, _ := runCLI("", "upgrade", "--yes", "--image", "ghcr.io/x/y:t")
	if code != exitOK || f.called("Apply") || !strings.Contains(out, "This is not a Hadron Desktop image. Install it anyway? [y/N]") {
		t.Fatalf("code %d, out %q, calls %v", code, out, f.calls)
	}
	// An explicit yes goes through.
	f = okDaemon()
	f.info = InspectInfo{Digest: digestA, Refusal: "not a Hadron Desktop image", Overridable: true}
	useDaemon(t, f)
	if code, _, _ := runCLI("y\n", "upgrade", "--yes", "--image", "ghcr.io/x/y:t"); code != exitOK || !f.called("Apply") {
		t.Fatalf("code %d, calls %v", code, f.calls)
	}
}

func TestNonOverridableRefusalExits1(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.info = InspectInfo{Digest: digestA, Refusal: "that is the agent image"}
	useDaemon(t, f)
	code, _, errOut := runCLI("y\ny\n", "upgrade", "--yes")
	if code != exitError || !strings.Contains(errOut, "agent image") || f.called("Apply") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestSwitchValidation(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	for _, a := range []string{"agent", "bogus", "sway"} {
		if code, _, _ := runCLI("", "switch", a, "--yes"); code != exitUsage {
			t.Errorf("switch %s: code %d, want 2", a, code)
		}
	}
	if f.called("Apply") {
		t.Fatal("applied an invalid switch")
	}
}

func TestSwitchWarnsAndAsks(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	code, out, _ := runCLI("n\n", "switch", "niri")
	if code != exitOK || f.called("Apply") || !strings.Contains(out, "Your per-user desktop settings may not carry over, and the login screen may change.") {
		t.Fatalf("code %d, out %q", code, out)
	}
	// Default is No.
	if _, _, _ = runCLI("", "switch", "niri"); f.called("Apply") {
		t.Fatal("EOF answered the switch question")
	}
	f = okDaemon()
	useDaemon(t, f)
	if code, _, e := runCLI("y\nn\n", "switch", "niri"); code != exitOK {
		t.Fatalf("code %d %s", code, e)
	}
	if !strings.HasSuffix(f.applied[0], ":niri-v0.2.0") {
		t.Fatalf("Apply ref %q", f.applied[0])
	}
}

func TestDaemonErrorMapping(t *testing.T) {
	for name, want := range map[string]string{
		"NotAuthorized": "Only members of the admin group can install updates.",
		"RebootPending": "An update is already installed; restart first.",
		"Busy":          "Another update is running.",
	} {
		setup(t, "v0.1.0", "v0.2.0")
		f := okDaemon()
		f.applyErr = &daemonError{Name: name, Msg: "raw"}
		useDaemon(t, f)
		code, _, errOut := runCLI("", "upgrade", "--yes")
		if code != exitError || !strings.Contains(errOut, want) {
			t.Errorf("%s: code %d, stderr %q", name, code, errOut)
		}
	}
}

func TestInspectBusyRetriesThenGivesUp(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	busy := &daemonError{Name: "Busy", Msg: "busy"}
	f := okDaemon()
	f.inspectEr = []error{busy, busy}
	useDaemon(t, f)
	if code, _, e := runCLI("", "upgrade", "--yes"); code != exitOK {
		t.Fatalf("code %d %s", code, e)
	}
	f = okDaemon()
	f.inspectEr = []error{busy, busy, busy, busy, busy, busy, busy}
	useDaemon(t, f)
	code, _, errOut := runCLI("", "upgrade", "--yes")
	n := 0
	for _, c := range f.calls {
		if c == "Inspect" {
			n++
		}
	}
	if code != exitError || n != 6 || !strings.Contains(errOut, "busy") {
		t.Fatalf("code %d, inspect calls %d, stderr %q", code, n, errOut)
	}
}

func TestInstallErrorStateExits1(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.script = []Event{{Status: &Status{State: StateApplying}}, {Status: &Status{State: StateError, Detail: "disk full"}}}
	useDaemon(t, f)
	code, _, errOut := runCLI("", "upgrade", "--yes", "--reboot")
	if code != exitError || !strings.Contains(errOut, "disk full") || f.called("Reboot") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestMissingDaemonIsNotAFallbackToSudo(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	agent, argv := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)
	old := dialDaemon
	dialDaemon = func() (daemonClient, error) { return nil, errors.New("no bus") }
	t.Cleanup(func() { dialDaemon = old })
	code, _, errOut := runCLI("", "upgrade", "--yes")
	if code != exitError || !strings.Contains(errOut, "The update service is not available (hadron-updated).") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("fell back to kairos-agent")
	}
}

func TestAgentFlavorNeverDialsTheDaemon(t *testing.T) {
	setupAgent(t, "v0.1.0", "v0.2.0")
	agent, _ := fakeTool(t, 0)
	t.Setenv("HADRON_UPDATE_AGENT", agent)
	old := dialDaemon
	dialDaemon = func() (daemonClient, error) { t.Fatal("dialled the daemon"); return nil, nil }
	t.Cleanup(func() { dialDaemon = old })
	if code, _, e := runCLI("", "upgrade", "--yes"); code != exitOK {
		t.Fatalf("code %d %s", code, e)
	}
	runCLI("", "status")
}

func TestRebootSubcommand(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	if code, _, _ := runCLI("", "reboot"); code != exitOK || !f.called("Reboot") {
		t.Fatalf("code %d, calls %v", code, f.calls)
	}
}

func TestStatusPrintsThePin(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.status.Pin = "ghcr.io/x/y:t"
	useDaemon(t, f)
	if _, out, _ := runCLI("", "status"); !strings.Contains(out, "Pin:        ghcr.io/x/y:t") {
		t.Fatalf("out %q", out)
	}
	f.status.Pin = ""
	if _, out, _ := runCLI("", "status"); !strings.Contains(out, "Pin:        none") {
		t.Fatalf("out %q", out)
	}
}

func TestLostConnectionExits1(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.script = []Event{{Status: &Status{State: StateApplying}}}
	f.closeAfter = true
	useDaemon(t, f)
	code, _, errOut := runCLI("", "upgrade", "--yes", "--reboot")
	if code != exitError || !strings.Contains(errOut, "lost the connection") || f.called("Reboot") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestWatchErrorStopsBeforeApply(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.watchErr = errors.New("no match")
	useDaemon(t, f)
	if code, _, _ := runCLI("", "upgrade", "--yes"); code != exitError || f.called("Apply") {
		t.Fatalf("code %d, calls %v", code, f.calls)
	}
}

// A failed Check between Watch and Apply leaves StateError; it is not this job.
func TestStaleErrorBeforeTheJobStartsIsIgnored(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	f.script = []Event{
		{Status: &Status{State: StateError, Detail: "github down"}},
		{Status: &Status{State: StateInspecting}}, {Status: &Status{State: StateApplying}},
		{Status: &Status{State: StateRebootPending}},
	}
	useDaemon(t, f)
	if code, _, e := runCLI("", "upgrade", "--yes"); code != exitOK {
		t.Fatalf("code %d, stderr %q", code, e)
	}
}

func TestWrapBusErr(t *testing.T) {
	name := busInterface + ".Error.Busy"
	for label, err := range map[string]error{
		"value":   dbus.Error{Name: name, Body: []any{"slot taken"}},
		"pointer": &dbus.Error{Name: name, Body: []any{"slot taken"}},
	} {
		got := wrapBusErr(err)
		if daemonErrName(got) != "Busy" || got.Error() != "slot taken" {
			t.Errorf("%s: %v (%q)", label, got, daemonErrName(got))
		}
	}
	if got := wrapBusErr(dbus.Error{Name: name}); daemonErrName(got) != "Busy" || got.Error() != name {
		t.Errorf("missing body: %v", got)
	}
	other := dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"x"}}
	if got := wrapBusErr(other); daemonErrName(got) != "" {
		t.Errorf("foreign error mapped: %v", got)
	}
	plain := errors.New("plain")
	if wrapBusErr(plain) != plain {
		t.Error("plain error changed")
	}
}

func TestStatusFromDict(t *testing.T) {
	st := statusFromDict(map[string]dbus.Variant{
		"state": dbus.MakeVariant("applying"), "installed": dbus.MakeVariant("v1"),
		"latest": dbus.MakeVariant("v2"), "pin": dbus.MakeVariant("p"),
		"detail": dbus.MakeVariant("d"), "advanced": dbus.MakeVariant(true),
	})
	want := Status{State: StateApplying, Installed: "v1", Latest: "v2", Pin: "p", Detail: "d", Advanced: true}
	if st != want {
		t.Fatalf("%+v", st)
	}
	if statusFromDict(nil) != (Status{}) {
		t.Fatal("nil dict")
	}
}

func TestEventFromSignal(t *testing.T) {
	d := map[string]dbus.Variant{"state": dbus.MakeVariant("error"), "detail": dbus.MakeVariant("boom")}
	ev, ok := eventFromSignal(&dbus.Signal{Name: busInterface + ".StateChanged", Body: []any{d}})
	if !ok || ev.Status == nil || ev.Status.State != StateError || ev.Status.Detail != "boom" {
		t.Fatalf("%+v %v", ev, ok)
	}
	ev, ok = eventFromSignal(&dbus.Signal{Name: busInterface + ".Progress", Body: []any{"line 1"}})
	if !ok || ev.Line != "line 1" || ev.Status != nil {
		t.Fatalf("%+v %v", ev, ok)
	}
	for _, s := range []*dbus.Signal{
		{Name: busInterface + ".Progress", Body: []any{1}},
		{Name: busInterface + ".StateChanged", Body: []any{"x"}},
		{Name: busInterface + ".StateChanged"},
		{Name: "other.Signal", Body: []any{"x"}},
	} {
		if _, ok := eventFromSignal(s); ok {
			t.Errorf("decoded %+v", s)
		}
	}
}

func TestOwnerLost(t *testing.T) {
	n := "org.freedesktop.DBus.NameOwnerChanged"
	if !ownerLost(&dbus.Signal{Name: n, Body: []any{busName, ":1.5", ""}}) {
		t.Error("owner loss missed")
	}
	if ownerLost(&dbus.Signal{Name: n, Body: []any{busName, "", ":1.6"}}) {
		t.Error("a new owner is not a loss")
	}
	if ownerLost(&dbus.Signal{Name: n, Body: []any{"other.Name", ":1.5", ""}}) {
		t.Error("another name")
	}
}

// fakeObj is a BusObject whose Call answers from a script.
type fakeObj struct {
	dbus.BusObject
	results []*dbus.Call
	n       int
}

func (o *fakeObj) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	c := o.results[o.n]
	o.n++
	return c
}

func TestCallRetriesOnceOnExiting(t *testing.T) {
	exiting := &dbus.Call{Err: dbus.Error{Name: busInterface + ".Error.Exiting", Body: []any{"exiting"}}}
	obj := &fakeObj{results: []*dbus.Call{exiting, {Body: []any{[]string{"v1"}}}}}
	c := &client{obj: obj}
	tags, err := c.Releases()
	if err != nil || len(tags) != 1 || obj.n != 2 {
		t.Fatalf("tags %v err %v calls %d", tags, err, obj.n)
	}
	obj = &fakeObj{results: []*dbus.Call{exiting, exiting, {}}}
	c = &client{obj: obj}
	if err := c.Check(); daemonErrName(err) != "Exiting" || obj.n != 2 {
		t.Fatalf("err %v calls %d: must retry exactly once", err, obj.n)
	}
	obj = &fakeObj{results: []*dbus.Call{{Err: dbus.Error{Name: busInterface + ".Error.Refused"}}, {}}}
	c = &client{obj: obj}
	if err := c.Check(); daemonErrName(err) != "Refused" || obj.n != 1 {
		t.Fatalf("err %v calls %d: other errors are not retried", err, obj.n)
	}
}

// flagObj answers every call with an idle Status and records the flags.
type flagObj struct {
	dbus.BusObject
	mu    sync.Mutex
	flags []dbus.Flags
}

func (o *flagObj) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	o.mu.Lock()
	o.flags = append(o.flags, flags)
	o.mu.Unlock()
	return &dbus.Call{Body: []any{map[string]dbus.Variant{"state": dbus.MakeVariant("idle")}}}
}

// The backstop poll must not start the service: after a crash, a watcher
// that re-activates it would hide the crash and defeat the idle exit.
func TestWatchPollDoesNotAutoStart(t *testing.T) {
	conn := busConn(t, privateBus(t))
	obj := &flagObj{}
	c := &client{conn: conn, obj: obj}
	old := statusPoll
	statusPoll = 10 * time.Millisecond
	t.Cleanup(func() { statusPoll = old })
	events, stop, err := c.Watch()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case ev := <-events:
		if ev.Status == nil || ev.Status.State != StateIdle {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no poll")
	}
	// Stop the watcher and wait for its goroutine, so no poll runs below.
	stop()
	for range events {
	}
	obj.mu.Lock()
	for _, f := range obj.flags {
		if f&dbus.FlagNoAutoStart == 0 {
			obj.mu.Unlock()
			t.Fatalf("poll flags %v lack FlagNoAutoStart", f)
		}
	}
	// An explicit Status still activates the service.
	obj.flags = nil
	obj.mu.Unlock()
	if _, err := c.Status(); err != nil {
		t.Fatal(err)
	}
	obj.mu.Lock()
	defer obj.mu.Unlock()
	if len(obj.flags) != 1 || obj.flags[0]&dbus.FlagNoAutoStart != 0 {
		t.Fatalf("Status flags %v", obj.flags)
	}
}

// After the job started, a state no job passes through (cancelled from the
// tray, or a restarted daemon) means it is gone: do not wait forever.
func TestLostJobExits1(t *testing.T) {
	for _, s := range []State{StateIdle, StateUpToDate, StateUpdateAvailable} {
		setup(t, "v0.1.0", "v0.2.0")
		f := okDaemon()
		f.script = []Event{{Status: &Status{State: StateInspecting}}, {Status: &Status{State: s}},
			{Status: &Status{State: StateRebootPending}}}
		useDaemon(t, f)
		code, _, errOut := runCLI("", "upgrade", "--yes", "--reboot")
		if code != exitError || !strings.Contains(errOut, "lost the update job") || f.called("Reboot") {
			t.Errorf("%s: code %d, stderr %q", s, code, errOut)
		}
	}
}

func TestUpgradeImageFromStdin(t *testing.T) {
	setup(t, "v0.1.0", "v0.2.0")
	f := okDaemon()
	useDaemon(t, f)
	code, out, errOut := runCLI("ghcr.io/me/img:1\ny\n", "upgrade", "--image", "-")
	if code != exitOK || !strings.Contains(out, "Image reference:") || f.applied[0] != "ghcr.io/me/img:1" || f.applied[2] != "adv" {
		t.Fatalf("code %d, out %q, stderr %q, applied %v", code, out, errOut, f.applied)
	}

	f = okDaemon()
	useDaemon(t, f)
	if code, out, _ := runCLI("\n", "upgrade", "--image", "-"); code != exitOK || !strings.Contains(out, "Cancelled.") || f.called("Inspect") {
		t.Fatalf("empty answer: code %d, out %q, calls %v", code, out, f.calls)
	}

	f = okDaemon()
	f.status.Advanced = false
	useDaemon(t, f)
	if code, out, _ := runCLI("ghcr.io/me/img:1\n", "upgrade", "--image", "-"); code != exitUsage || strings.Contains(out, "Image reference:") {
		t.Fatalf("advanced off: code %d, out %q; must refuse before asking", code, out)
	}
}
