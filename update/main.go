// hadron-update keeps a Hadron Desktop install current.
//
// It looks up the newest hadron-desktop release on GitHub, compares it with
// /etc/hadron-desktop/release, and upgrades by handing the matching OCI image to
// `kairos-agent upgrade`. The image is A/B: the running system is untouched
// until the reboot, and the previous one stays selectable in the boot menu.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Exit codes. `check` exits exitUpdate when an update is available, so a script
// can tell "nothing to do" from "failed to look".
const (
	exitOK     = 0
	exitError  = 1
	exitUsage  = 2
	exitUpdate = 100
)

const usage = `Usage: hadron-update [command] [flags]

Commands:
  status            show the installed and the latest release (default)
  check             like status; exits 100 when an update is available
  upgrade           install the latest release, then offer to reboot
      --version vX.Y.Z   install this release instead of the latest
      --reboot           reboot when the upgrade finishes, without asking
      --yes              do not ask for confirmation
  notify            run in a desktop session: check now and every few hours,
                    and show a notification when a new release is out
      --once             check once and exit
  daemon            run the root update service on the system bus; started
                    by D-Bus activation, not by hand
`

// kairosAgent is the upgrade backend. HADRON_UPDATE_AGENT replaces it, so a
// test can assert what would have been run without touching a disk.
func kairosAgent() string {
	if p := os.Getenv("HADRON_UPDATE_AGENT"); p != "" {
		return p
	}
	return "/usr/bin/kairos-agent"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "status", "check":
		return cmdStatus(cmd, args, stdout, stderr)
	case "upgrade":
		return cmdUpgrade(args, stdin, stdout, stderr)
	case "notify":
		return cmdNotify(args, stderr)
	case "daemon":
		return cmdDaemon(args, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "hadron-update: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("hadron-update "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

func cmdStatus(name string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags(name, stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rel, err := LoadRelease(releaseFile())
	if err != nil {
		fmt.Fprintln(stderr, "hadron-update:", err)
		return exitError
	}

	fmt.Fprintf(stdout, "Installed:  Hadron Desktop %s (%s, Hadron %s)\n",
		rel.Version, rel.Flavor, orUnknown(rel.HadronVersion))

	latest, err := FetchLatest(context.Background(), rel.GitHubRepo)
	if err != nil {
		fmt.Fprintln(stderr, "hadron-update: could not check for updates:", err)
		return exitError
	}
	if !Newer(latest.Tag, rel.Version) {
		fmt.Fprintf(stdout, "Latest:     %s, you are up to date.\n", latest.Tag)
		return exitOK
	}
	fmt.Fprintf(stdout, "Latest:     %s is available.\n", latest.Tag)
	fmt.Fprintf(stdout, "            What's new: %s\n", latest.URL)
	fmt.Fprintln(stdout, "            Run `hadron-update upgrade` to install it.")
	if name == "check" {
		return exitUpdate
	}
	return exitOK
}

func cmdUpgrade(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlags("upgrade", stderr)
	version := fs.String("version", "", "release to install")
	reboot := fs.Bool("reboot", false, "reboot when done")
	yes := fs.Bool("yes", false, "do not ask")
	// --as-root marks the sudo child of an interactive upgrade: the parent has
	// already shown the plan, asked, and checked the image, so the child only
	// runs kairos-agent. Not in the usage text.
	asRootChild := fs.Bool("as-root", false, "")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// One reader for both questions: a second bufio.Reader over the same
	// stdin would lose whatever the first one had already buffered.
	in := bufio.NewReader(stdin)

	rel, err := LoadRelease(releaseFile())
	if err != nil {
		fmt.Fprintln(stderr, "hadron-update:", err)
		return exitError
	}

	tag := *version
	if tag == "" {
		latest, err := FetchLatest(context.Background(), rel.GitHubRepo)
		if err != nil {
			fmt.Fprintln(stderr, "hadron-update: could not check for updates:", err)
			return exitError
		}
		if !Newer(latest.Tag, rel.Version) {
			fmt.Fprintf(stdout, "Hadron Desktop %s is the latest release, nothing to do.\n", rel.Version)
			return exitOK
		}
		tag = latest.Tag
	} else if _, ok := parseSemver(tag); !ok {
		fmt.Fprintf(stderr, "hadron-update: %q is not a release tag like v1.2.3\n", tag)
		return exitUsage
	}

	image := rel.ImageFor(tag)
	if !*asRootChild {
		fmt.Fprintf(stdout, "Upgrading Hadron Desktop %s -> %s\n  image: %s\n", rel.Version, tag, image)
		fmt.Fprintln(stdout, "The running system is not touched; the new one is used from the next boot.")

		// Ask before escalating, so the question is asked once, as the user.
		if !*yes && !confirm(in, stdout, "Continue?", true) {
			fmt.Fprintln(stdout, "Cancelled.")
			return exitOK
		}

		if err := ImagePullable(context.Background(), image); err != nil {
			fmt.Fprintf(stderr, "hadron-update: cannot pull the %s image: %v\n", tag, err)
			fmt.Fprintln(stderr, "Releases before upgrade images were published have none; otherwise try again later.")
			return exitError
		}
	}

	if needRoot() {
		// The answer was already given, so the root copy runs with --yes and a
		// pinned --version: it must install exactly what the user agreed to,
		// even if a newer release lands in between. It never reboots; that
		// question is asked below, as the user.
		if err := asRoot("upgrade", "--as-root", "--yes", "--version", tag).Run(); err != nil {
			// The child already said what went wrong.
			return exitError
		}
	} else {
		agent := exec.Command(kairosAgent(), "upgrade", "--source", "oci:"+image)
		agent.Stdout, agent.Stderr = stdout, stderr
		if err := agent.Run(); err != nil {
			fmt.Fprintln(stderr, "hadron-update: the upgrade failed:", err)
			fmt.Fprintln(stderr, "Nothing changed on the running system; you can retry safely.")
			return exitError
		}
		fmt.Fprintf(stdout, "\nHadron Desktop %s is installed and starts on the next boot.\n", tag)
	}

	if *reboot || (!*yes && confirm(in, stdout, "Reboot now?", false)) {
		if err := rebootCommand().Run(); err != nil {
			fmt.Fprintln(stderr, "hadron-update: reboot failed:", err)
			return exitError
		}
	}
	return exitOK
}

// needRoot is a variable so tests can run the root path as an ordinary user.
var needRoot = func() bool { return os.Geteuid() != 0 }

// asRoot runs this same binary under sudo, wired to the caller's terminal so
// sudo can ask for the password and kairos-agent's progress stays visible.
func asRoot(args ...string) *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		self = "/usr/bin/hadron-update"
	}
	c := exec.Command("sudo", append([]string{self}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c
}

// rebootCommand is a variable so tests can observe a reboot instead of doing one.
var rebootCommand = func() *exec.Cmd {
	if needRoot() {
		return exec.Command("sudo", "systemctl", "reboot")
	}
	return exec.Command("systemctl", "reboot")
}

func confirm(in *bufio.Reader, stdout io.Writer, question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(stdout, "%s %s ", question, hint)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(stdout)
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

// notifyInterval is how often a session re-checks. GitHub allows 60
// unauthenticated API calls an hour per address; four a day is nothing.
const notifyInterval = 6 * time.Hour

// notifyDelay keeps the first check off the login path: the network may not be
// up yet, and nobody reads a notification in the first seconds of a session.
const notifyDelay = 2 * time.Minute

func cmdNotify(args []string, stderr io.Writer) int {
	fs := newFlags("notify", stderr)
	once := fs.Bool("once", false, "check once and exit")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *once {
		if err := notifyOnce(); err != nil {
			fmt.Fprintln(stderr, "hadron-update:", err)
			return exitError
		}
		return exitOK
	}

	time.Sleep(notifyDelay)
	for {
		// A failed check (offline laptop) is not worth a notification; try again
		// at the next tick.
		if err := notifyOnce(); err != nil {
			fmt.Fprintln(stderr, "hadron-update:", err)
		}
		time.Sleep(notifyInterval)
	}
}

func notifyOnce() error {
	rel, err := LoadRelease(releaseFile())
	if err != nil {
		return err
	}
	latest, err := FetchLatest(context.Background(), rel.GitHubRepo)
	if err != nil {
		return err
	}
	if !Newer(latest.Tag, rel.Version) || alreadyNotified(latest.Tag) {
		return nil
	}
	// Do not announce an upgrade that cannot run: a release from before upgrade
	// images existed, or one whose package is not public yet. The next tick
	// retries, so an image that appears later is still announced.
	if err := ImagePullable(context.Background(), rel.ImageFor(latest.Tag)); err != nil {
		return fmt.Errorf("%s is out but not installable yet: %w", latest.Tag, err)
	}
	if err := Notify(
		fmt.Sprintf("Hadron Desktop %s is available", latest.Tag),
		fmt.Sprintf("You are on %s. Open a terminal and run: hadron-update upgrade", rel.Version),
	); err != nil {
		return err
	}
	return markNotified(latest.Tag)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
