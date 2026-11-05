package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeDaemon is daemonClient without a bus. Apply feeds script to the watcher.
type fakeDaemon struct {
	status    Status
	releases  []string
	info      InspectInfo
	inspectEr []error // returned in order before success
	applyErr  error
	script    []Event

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
	return nil
}
func (f *fakeDaemon) Cancel() error { f.rec("Cancel"); return nil }
func (f *fakeDaemon) Reboot() error { f.rec("Reboot"); return nil }
func (f *fakeDaemon) Watch() (<-chan Event, func()) {
	f.events = make(chan Event, 16)
	return f.events, func() {}
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
			{Line: "pulling"}, {Status: &Status{State: StateApplying}},
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
	if code, _, e := runCLI("y\ny\nn\n", "switch", "niri"); code != exitOK {
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
	f.script = []Event{{Status: &Status{State: StateError, Detail: "disk full"}}}
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
