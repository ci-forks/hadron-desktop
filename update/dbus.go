package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// The bus API. The names are part of the interface: the tray, the CLI, the
// D-Bus policy, the activation file and the polkit files all spell them.
const (
	busName      = "io.kairos.HadronUpdate1"
	busPath      = dbus.ObjectPath("/io/kairos/HadronUpdate1")
	busInterface = "io.kairos.HadronUpdate1"

	actionApply  = "io.kairos.hadron-update.apply"
	actionReboot = "io.kairos.hadron-update.reboot"
)

// busIntrospection describes the interface for busctl, d-feet and friends.
const busIntrospection = introspect.IntrospectDeclarationString + `<node>
  <interface name="io.kairos.HadronUpdate1">
    <method name="Status"><arg name="status" type="a{sv}" direction="out"/></method>
    <method name="Check"/>
    <method name="Releases"><arg name="tags" type="as" direction="out"/></method>
    <method name="Inspect">
      <arg name="ref" type="s" direction="in"/>
      <arg name="advanced" type="b" direction="in"/>
      <arg name="info" type="a{sv}" direction="out"/>
    </method>
    <method name="Apply">
      <arg name="ref" type="s" direction="in"/>
      <arg name="digest" type="s" direction="in"/>
      <arg name="advanced" type="b" direction="in"/>
    </method>
    <method name="Cancel"/>
    <method name="Reboot"/>
    <method name="RebootPrevious"/>
    <signal name="StateChanged"><arg name="status" type="a{sv}"/></signal>
    <signal name="Progress"><arg name="line" type="s"/></signal>
  </interface>` + introspect.IntrospectDataString + `</node>`

// idleTimeout is how long the service stays up with no method call, no
// state change and no job. D-Bus activation starts it again on the next
// call, so a desktop that never updates does not keep a root process around.
// Variables so tests can shorten them.
var (
	idleTimeout = 10 * time.Minute
	idleTick    = 30 * time.Second
)

// Authorizer decides whether the bus client sender may do action. Any error
// is a refusal: the caller never treats an error as a yes.
type Authorizer interface {
	Allowed(sender dbus.Sender, action string) (bool, error)
}

// polkitSubject is polkit's Subject, (sa{sv}).
type polkitSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}

// polkitResult is polkit's AuthorizationResult, (bba{ss}).
type polkitResult struct {
	IsAuthorized bool
	IsChallenge  bool
	Details      map[string]string
}

// polkitAuthorizer asks polkit over the system bus.
type polkitAuthorizer struct{ conn *dbus.Conn }

// polkitTimeout bounds a CheckAuthorization call. Without interaction polkit
// answers at once; a hung polkit must not hold a method call forever.
const polkitTimeout = 25 * time.Second

// Allowed checks action for the bus name sender. polkit looks the name up
// itself, so the uid and process are the ones the bus vouches for, not ones
// the client claims. Flags are 0: no authentication dialog, since the rules
// file answers yes or no for every action and no agent runs. A challenge
// ("would be allowed after authenticating") is therefore a no.
func (p polkitAuthorizer) Allowed(sender dbus.Sender, action string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), polkitTimeout)
	defer cancel()
	subject := polkitSubject{
		Kind:    "system-bus-name",
		Details: map[string]dbus.Variant{"name": dbus.MakeVariant(string(sender))},
	}
	var res polkitResult
	err := p.conn.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority").
		CallWithContext(ctx, "org.freedesktop.PolicyKit1.Authority.CheckAuthorization", 0,
			subject, action, map[string]string{}, uint32(0), "").
		Store(&res)
	if err != nil {
		return false, fmt.Errorf("polkit: %w", err)
	}
	return res.IsAuthorized && !res.IsChallenge, nil
}

// logindInhibitor takes a logind "block" inhibitor lock on shutdown and
// sleep. logind keeps it until the returned fd is closed, so a reboot asked
// from the session waits for kairos-agent instead of cutting the write.
type logindInhibitor struct{ conn *dbus.Conn }

func (l logindInhibitor) Inhibit() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), polkitTimeout)
	defer cancel()
	var fd dbus.UnixFD
	err := l.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		CallWithContext(ctx, "org.freedesktop.login1.Manager.Inhibit", 0,
			"shutdown:sleep", "Hadron Desktop update", "An update is being written to disk", "block").
		Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("logind: %w", err)
	}
	f := os.NewFile(uintptr(fd), "logind-inhibitor")
	if f == nil {
		return nil, fmt.Errorf("logind returned an invalid descriptor %d", fd)
	}
	return func() { f.Close() }, nil
}

// busError maps a daemon error to a D-Bus error whose name a client can
// switch on; the message is the error's own sentence.
func busError(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	var name string
	switch {
	case errors.Is(err, ErrBusy):
		name = "Busy"
	case errors.Is(err, ErrRebootPending):
		name = "RebootPending"
	case errors.Is(err, ErrNotCancellable):
		name = "NotCancellable"
	case errors.Is(err, ErrRebooting):
		name = "Rebooting"
	case errors.Is(err, ErrAgentImage), errors.Is(err, ErrNotHadron),
		errors.Is(err, ErrNeedsCredentials), errors.Is(err, ErrWrongPlatform),
		errors.Is(err, errUnmanaged):
		name = "Refused"
	default:
		name = "Failed"
	}
	return dbus.NewError(busInterface+".Error."+name, []any{err.Error()})
}

var errNotAuthorized = dbus.NewError(busInterface+".Error.NotAuthorized",
	[]any{"not authorized: only members of the admin group may install updates or reboot"})

func statusDict(s Status) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"state":     dbus.MakeVariant(string(s.State)),
		"installed": dbus.MakeVariant(s.Installed),
		"latest":    dbus.MakeVariant(s.Latest),
		"pin":       dbus.MakeVariant(s.Pin),
		"detail":    dbus.MakeVariant(s.Detail),
		"advanced":  dbus.MakeVariant(s.Advanced),
	}
}

// service is the state serve keeps besides the daemon: who is talking to it
// and when anything last happened, for the idle exit.
type service struct {
	d    *Daemon
	auth Authorizer

	// netSlot admits one network-bound call (Check, Releases, Inspect) at
	// a time. They are open to every local user, so without it any caller
	// could make root run fetches without limit and spend the machine's
	// GitHub quota.
	netSlot chan struct{}

	mu      sync.Mutex
	active  int       // method calls in progress
	last    time.Time // end of the last call or the last daemon event
	closing bool      // decided to exit; no call may start anything now
}

// errExiting is its own name so a client can retry exactly once on it: the
// next call activates a new instance.
var errExiting = dbus.NewError(busInterface+".Error.Exiting",
	[]any{"the update service is exiting; call again to start a new one"})

var errNetBusy = dbus.NewError(busInterface+".Error.Busy",
	[]any{"another check or inspection is running; try again when it ends"})

// maxRefLen bounds a reference before any work; real ones are far shorter.
const maxRefLen = 512

func checkRef(ref string) *dbus.Error {
	if len(ref) > maxRefLen {
		return busError(fmt.Errorf("the image reference is longer than %d bytes", maxRefLen))
	}
	return nil
}

// network takes the network slot, or refuses at once: a caller waiting in
// line would still hold a goroutine and a bus call open.
func (s *service) network() (func(), *dbus.Error) {
	select {
	case s.netSlot <- struct{}{}:
		return func() { <-s.netSlot }, nil
	default:
		return nil, errNetBusy
	}
}

func (s *service) touch() {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
}

// enter marks a method call in progress; the returned func ends it. Once the
// service has decided to exit it refuses, so no job starts in a process that
// is about to go away.
func (s *service) enter() (func(), *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, errExiting
	}
	s.active++
	return func() {
		s.mu.Lock()
		s.active--
		s.last = time.Now()
		s.mu.Unlock()
	}, nil
}

// tryClose decides to exit when nothing happened for quiet, no call runs and
// no job runs. The decision and the checks happen under one lock, so a call
// either counted as active or is refused by enter.
func (s *service) tryClose(quiet time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 || time.Since(s.last) < quiet || s.d.Busy() {
		return false
	}
	s.closing = true
	return true
}

func newService(d *Daemon, a Authorizer) *service {
	return &service{d: d, auth: a, last: time.Now(), netSlot: make(chan struct{}, 1)}
}

// authorize asks before any argument is looked at, so an unauthorized
// client learns nothing about the system from a mutating call.
func (s *service) authorize(sender dbus.Sender, action string) *dbus.Error {
	ok, err := s.auth.Allowed(sender, action)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hadron-update: refusing %s for %s: %v\n", action, sender, err)
		return errNotAuthorized
	}
	if !ok {
		return errNotAuthorized
	}
	return nil
}

// busAPI is the exported object. Every exported method is a D-Bus method,
// so it has no others.
type busAPI struct{ s *service }

func (a busAPI) Status() (map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	return statusDict(a.s.d.Status()), nil
}

// Check is unauthenticated: it only refreshes the state, and the tray calls
// it on its own schedule.
func (a busAPI) Check() *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	free, derr := a.s.network()
	if derr != nil {
		return derr
	}
	defer free()
	return busError(a.s.d.Check(context.Background()))
}

func (a busAPI) Releases() ([]string, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	free, derr := a.s.network()
	if derr != nil {
		return nil, derr
	}
	defer free()
	rels, err := a.s.d.Releases(context.Background())
	if err != nil {
		return nil, busError(err)
	}
	tags := make([]string, 0, len(rels))
	for _, r := range rels {
		tags = append(tags, r.Tag)
	}
	return tags, nil
}

// Inspect reports a policy refusal in the reply, not as an error, so the
// client can show what the image is and why it would be refused before it
// asks to Apply. "overridable" says whether asking again with advanced=true
// would make it installable. Everything else (a bad reference, the network,
// a private image, a wrong platform) is an error.
func (a busAPI) Inspect(ref string, advanced bool) (map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	if derr := checkRef(ref); derr != nil {
		return nil, derr
	}
	free, derr := a.s.network()
	if derr != nil {
		return nil, derr
	}
	defer free()
	info, v, err := a.s.d.Inspect(context.Background(), ref, advanced)
	refusal, overridable := "", false
	switch {
	case errors.Is(err, ErrNotHadron):
		refusal, overridable = ErrNotHadron.Error(), AdvancedEnabled()
	case errors.Is(err, ErrAgentImage):
		refusal = ErrAgentImage.Error()
	case err != nil:
		return nil, busError(err)
	}
	return map[string]dbus.Variant{
		"ref":         dbus.MakeVariant(info.Ref.String()),
		"digest":      dbus.MakeVariant(info.Digest),
		"os":          dbus.MakeVariant(info.OS),
		"arch":        dbus.MakeVariant(info.Arch),
		"flavor":      dbus.MakeVariant(v.Flavor),
		"version":     dbus.MakeVariant(v.Version),
		"trusted":     dbus.MakeVariant(v.Trusted),
		"hadron":      dbus.MakeVariant(v.Hadron),
		"refusal":     dbus.MakeVariant(refusal),
		"overridable": dbus.MakeVariant(overridable),
	}, nil
}

func (a busAPI) Apply(sender dbus.Sender, ref, digest string, advanced bool) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionApply); err != nil {
		return err
	}
	if derr := checkRef(ref); derr != nil {
		return derr
	}
	return busError(a.s.d.Apply(context.Background(), ref, digest, advanced))
}

// Cancel shares Apply's action: whoever may start an install may stop one.
func (a busAPI) Cancel(sender dbus.Sender) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionApply); err != nil {
		return err
	}
	return busError(a.s.d.Cancel())
}

func (a busAPI) Reboot(sender dbus.Sender) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionReboot); err != nil {
		return err
	}
	return busError(a.s.d.Reboot())
}

// RebootPrevious shares Reboot's action: it is a restart, into the other
// installed system, and installs nothing.
func (a busAPI) RebootPrevious(sender dbus.Sender) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionReboot); err != nil {
		return err
	}
	return busError(a.s.d.RebootPrevious())
}

// serve exports d on conn as io.kairos.HadronUpdate1 and returns when the
// service has been idle for idleTimeout, or with an error when the name is
// taken or the bus goes away. It never returns while a job runs.
func serve(conn *dbus.Conn, d *Daemon, a Authorizer) error {
	s := newService(d, a)
	d.OnChange(func(st Status) {
		s.touch()
		if err := conn.Emit(busPath, busInterface+".StateChanged", statusDict(st)); err != nil {
			fmt.Fprintln(os.Stderr, "hadron-update: emitting StateChanged:", err)
		}
	})
	d.OnProgress(func(line string) {
		s.touch()
		if err := conn.Emit(busPath, busInterface+".Progress", line); err != nil {
			fmt.Fprintln(os.Stderr, "hadron-update: emitting Progress:", err)
		}
	})

	// Export before taking the name: the bus delivers an activating call as
	// soon as the name is owned.
	if err := conn.Export(busAPI{s}, busPath, busInterface); err != nil {
		return err
	}
	if err := conn.Export(introspect.Introspectable(busIntrospection), busPath,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already owned by another process", busName)
	}

	tick := time.NewTicker(idleTick)
	defer tick.Stop()
	for {
		select {
		case <-conn.Context().Done():
			// Returning ends the process, and systemd then kills what is
			// left in the unit's cgroup: kairos-agent too. Let the job end.
			for d.Busy() {
				time.Sleep(idleTick)
			}
			return errors.New("lost the connection to the bus")
		case <-tick.C:
		}
		if s.tryClose(idleTimeout) {
			// Release the name before exiting, so the next call activates a
			// new instance. Calls that still reach this one are refused.
			if _, err := conn.ReleaseName(busName); err != nil {
				return fmt.Errorf("releasing %s: %w", busName, err)
			}
			return nil
		}
	}
}

// cmdDaemon is hadron-updated: the root service that owns the update job.
func cmdDaemon(args []string, stderr io.Writer) int {
	fs := newFlags("daemon", stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hadron-update daemon: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	// It runs kairos-agent and reboots; as any other user it could do
	// neither, and would only mislead clients by owning the name.
	if needRoot() {
		fmt.Fprintln(stderr, "hadron-update daemon: must run as root; D-Bus activation starts it through hadron-updated.service.")
		return exitError
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintln(stderr, "hadron-update daemon: cannot connect to the system bus:", err)
		return exitError
	}
	defer conn.Close()
	d := NewDaemon(kairosBackend{})
	d.inhibitor = logindInhibitor{conn: conn}
	if err := serve(conn, d, polkitAuthorizer{conn: conn}); err != nil {
		fmt.Fprintln(stderr, "hadron-update daemon:", err)
		return exitError
	}
	return exitOK
}
