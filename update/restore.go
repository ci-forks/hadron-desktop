package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const restoreUsage = `Usage: isotope restore [SNAPSHOT] [flags]

Restore a backup onto this machine. SNAPSHOT is an ID from "isotope backup
list"; the default is the latest. Runs as root through sudo.

By default the whole machine comes back: the users and their passwords,
the hostname, every home, the SSH keys, the system extensions and the
settings, then the machine restarts. Files that are only on this machine
stay. Do it right after installing Isotope, before you use the new system.
The disk must be encrypted the same way as the one that was backed up.

With --only, only some parts, into the users this machine already has:
  homes                the files of each user, into the home of the local
                       user of the same name (or --user)
  apps                 the Flatpak apps, installed again from Flathub
  extensions           the system extensions, installed again

Flags:
  --from TARGET        restore from TARGET instead of this machine's backup
                       target: a directory, a restic URL, or a file that
                       "isotope backup --export" wrote; asks for its password
  --only LIST          only some of homes,apps,extensions
  --user OLD=NEW       with --only homes: restore the files of user OLD into
                       NEW's home; may be given more than once
  --host NAME          the machine whose latest backup to restore, when
                       several back up to the same target
  --yes                do not ask for confirmation
`

// restoreParts are what a restore with --only can do, in the order it does
// them.
var restoreParts = []string{"homes", "apps", "extensions"}

// userMap is the --user flag.
type userMap map[string]string

func (m userMap) String() string { return "" }
func (m userMap) Set(s string) error {
	old, nw, ok := strings.Cut(s, "=")
	if !ok || old == "" || nw == "" {
		return fmt.Errorf("%q is not OLD=NEW", s)
	}
	m[old] = nw
	return nil
}

func cmdRestore(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, restoreUsage)
		return exitOK
	}
	if !exists(restic()) {
		fmt.Fprintln(stderr, "isotope: restore is not available on this edition (no restic).")
		return exitError
	}
	if needRoot() {
		if err := asRoot(append([]string{"restore"}, args...)...).Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			fmt.Fprintln(stderr, "isotope:", err)
			return exitError
		}
		return exitOK
	}
	fs := newFlags("restore", stderr)
	fs.Usage = func() { fmt.Fprint(stderr, restoreUsage) }
	from := fs.String("from", "", "")
	only := fs.String("only", "", "")
	yes := fs.Bool("yes", false, "")
	host := fs.String("host", "", "")
	finish := fs.Bool("finish", false, "") // isotope-restore-finish.service; not in the usage
	users := userMap{}
	fs.Var(users, "user", "")
	rest, err := parseInterspersed(fs, args)
	if err != nil || len(rest) > 1 {
		return exitUsage
	}
	snap := "latest"
	if len(rest) == 1 {
		snap = rest[0]
	}
	var parts map[string]bool // nil: the whole machine
	if *only != "" {
		parts = map[string]bool{}
		for _, x := range strings.Split(*only, ",") {
			if !contains(restoreParts, x) {
				fmt.Fprintf(stderr, "isotope restore: --only: %q is not one of %s\n", x, strings.Join(restoreParts, ","))
				return exitUsage
			}
			parts[x] = true
		}
	}
	if len(users) > 0 && !parts["homes"] && !parts["apps"] {
		fmt.Fprintln(stderr, "isotope restore: --user goes with --only homes (a whole-machine restore brings the users back as they were)")
		return exitUsage
	}
	r := restorer{p: defaultBackupPaths(), snap: snap, host: *host, users: users, parts: parts,
		in: bufio.NewReader(stdin), stdout: stdout, stderr: stderr}
	if *finish {
		if err := r.finish(); err != nil {
			fmt.Fprintln(stderr, "isotope: restore --finish:", err)
			return exitError
		}
		return exitOK
	}
	if err := r.run(*from, *yes); err != nil {
		fmt.Fprintln(stderr, "isotope: restore:", err)
		return exitError
	}
	return exitOK
}

type restorer struct {
	p              backupPaths
	target, pass   string
	snap, host     string
	users          userMap
	parts          map[string]bool // nil: the whole machine
	in             *bufio.Reader
	stdout, stderr io.Writer
	failed         []string // what could not be restored, said at the end
}

func (r *restorer) whole() bool { return r.parts == nil }

func (r *restorer) restic(args ...string) error {
	cmd, err := resticCmd(r.p, r.target, r.pass, args...)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	return cmd.Run()
}

func (r *restorer) ask(q string) bool {
	fmt.Fprintf(r.stdout, "%s [y/N] ", q)
	line, _ := r.in.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// plan is a restore read from a snapshot's manifest.
type plan struct {
	release, hostname string
	layout            string            // the backed-up disk's diskLayout
	users             []string          // every user the backup has
	homes             map[string]string // backed-up user -> local user (--only homes)
	skipped           []string          // backed-up users with no local user
	apps              []appLine
	exts              []extLine
}

type appLine struct{ user, id, origin string } // user "" is a system app
type extLine struct {
	file, source string
	enabled      bool
}

func (r *restorer) run(from string, yes bool) error {
	cfg, err := loadBackupConfig(r.p)
	if err != nil {
		return err
	}
	r.target, r.pass = cfg.Target, r.p.password()
	// The temporary password, an unpacked export and the staged files go,
	// whatever ends the restore: an error, or Ctrl-C during a long restic
	// run.
	var cleanup []func()
	stop := onInterrupt(func() {
		for _, f := range cleanup {
			f()
		}
	})
	defer stop()
	defer func() {
		for _, f := range cleanup {
			f()
		}
	}()
	if from != "" {
		if from, err = normalizeTarget(from); err != nil {
			return err
		}
		r.target = from
		fmt.Fprintf(r.stdout, "Password of the backup in %s: ", from)
		pw, err := readSecretLine(r.in)
		if err != nil {
			return err
		}
		// Under /run: a tmpfs, so even a killed restore leaves no password
		// on the disk.
		r.pass = filepath.Join(r.p.Run, "password.restore")
		if err := writeSecret(r.pass, pw); err != nil {
			return err
		}
		pass := r.pass
		cleanup = append(cleanup, func() { _ = os.Remove(pass) })
		// A file from `isotope backup --export`: unpack it into a directory
		// only root can enter, next to it when that is possible.
		if fi, err := os.Stat(from); err == nil && fi.Mode().IsRegular() {
			// Under /usr/local/isotope-backup, which only root can change:
			// next to the file, a user who owns that directory could swap
			// ours for a link while root writes into it.
			if err := os.MkdirAll(r.p.State, 0o755); err != nil {
				return err
			}
			dir, err := os.MkdirTemp(r.p.State, "work-")
			if err != nil {
				return err
			}
			cleanup = append(cleanup, func() { _ = os.RemoveAll(dir) })
			fmt.Fprintf(r.stdout, "Unpacking %s...\n", from)
			if err := unpackFile(from, dir); err != nil {
				return err
			}
			r.target = dir
		}
	}
	if r.target == "" {
		return errors.New("there is no backup target; give one with --from")
	}
	if !targetPresent(r.target) {
		return fmt.Errorf("there is no backup in %s. Is the disk plugged in?", r.target)
	}

	stage := r.p.stage()
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	cleanup = append(cleanup, func() { _ = os.RemoveAll(stage) })

	// "latest" is resolved once, so the plan and the files come from the
	// same snapshot, and from this machine's backups when others share the
	// target.
	if r.snap, err = r.resolveSnapshot(); err != nil {
		return err
	}
	if err := r.restic("restore", r.snap, "--target", stage, "--include", r.p.manifest()); err != nil {
		return fmt.Errorf("could not read the backup (wrong password?): %w", err)
	}
	pl, err := r.readPlan(filepath.Join(stage, r.p.manifest()))
	if err != nil {
		return err
	}
	if r.whole() {
		return r.runWhole(stage, pl, cfg, yes)
	}
	return r.runParts(stage, pl, yes)
}

// runWhole puts the backed-up /usr/local and /oem back, then restarts.
func (r *restorer) runWhole(stage string, pl plan, cfg backupConfig, yes bool) error {
	if here := diskLayout(r.p); pl.layout != here {
		return fmt.Errorf("the backup is of a disk set up as %q, and this one is %q. "+
			"Its /oem would not boot or unlock this disk. Install with the same choices "+
			"(encryption, Trusted Boot), or restore only the files with "+
			"--only homes,apps,extensions", pl.layout, here)
	}
	if pl.release != "" {
		fmt.Fprintf(r.stdout, "This backup was made on %s", pl.release)
		if pl.hostname != "" {
			fmt.Fprintf(r.stdout, ", on %s", pl.hostname)
		}
		fmt.Fprintln(r.stdout, ".")
	}
	fmt.Fprintln(r.stdout, "It brings this whole machine back as it was:")
	fmt.Fprintf(r.stdout, "  the users (%s) and their passwords, and the hostname\n", strings.Join(pl.users, ", "))
	fmt.Fprintln(r.stdout, "  every home, the SSH keys and the settings")
	if n := len(pl.apps) + len(pl.exts); n > 0 {
		fmt.Fprintf(r.stdout, "  %d Flatpak apps and %d system extensions, installed again after the restart\n", len(pl.apps), len(pl.exts))
	}
	fmt.Fprintln(r.stdout, "The users and settings of this new install are replaced. A file in the backup")
	fmt.Fprintln(r.stdout, "replaces the same file here; other files stay. The machine restarts at the end.")
	if !yes && !r.ask("Restore it?") {
		fmt.Fprintln(r.stdout, "Nothing changed.")
		return nil
	}
	args := []string{"restore", r.snap, "--target", stage, "--include", r.p.Persist}
	args = append(args, "--include", r.p.OEM)
	if err := r.restic(args...); err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	src := filepath.Join(stage, r.p.Persist)
	// The extensions come back through `isotope ext install` after the
	// restart, which checks each one against this system's /usr; as files
	// they would skip that check.
	if err := os.RemoveAll(filepath.Join(src, r.p.extOnPersist())); err != nil {
		return err
	}
	// This machine's backup settings stay: a target set up here keeps its
	// own password. Read before the merge replaces them.
	keepCfg, keepPass := cfg, readFileString(r.p.password())

	// The apps and extensions need the restored users and the network: an
	// isotope-restore-finish.service run after the restart installs them.
	// Queued from the stage, before the merge moves the manifest. The queue
	// is under State, which the merge does not replace: the backup has no
	// pending/ (it is excluded).
	if len(pl.apps) > 0 || len(pl.exts) > 0 {
		if err := r.queuePending(filepath.Join(stage, r.p.manifest())); err != nil {
			r.failed = append(r.failed, "the list of apps and extensions to install: "+err.Error())
		}
	}
	// Docker writes under its storage while it runs.
	_ = sysCommand("systemctl", "stop", "docker.socket", "docker.service").Run()
	// The persistent partition: one file system with the stage, so the
	// files are renamed into place, with descriptors that never follow a
	// link (a user's home is on it). The owners are the backup's: /oem
	// brings back the same users with the same IDs at the restart.
	if exists(src) {
		if err := mergeMove(src, r.p.Persist); err != nil {
			r.failed = append(r.failed, "some files of "+r.p.Persist+": "+err.Error())
		}
	}
	// /oem: exactly the backup's cloud-configs. One of this install's that
	// the backup does not have would still run at boot and create its user
	// first, with the IDs the backed-up users had. They are kept aside.
	if from := filepath.Join(stage, r.p.OEM); exists(from) {
		aside := filepath.Join(r.p.State, "oem-before-restore", backupNow().Format("20060102-150405"))
		if err := replaceOEM(from, r.p.OEM, aside); err != nil {
			r.failed = append(r.failed, r.p.OEM+": "+err.Error())
		}
	}
	if err := r.keepBackupSettings(keepCfg, keepPass); err != nil {
		r.failed = append(r.failed, "the backup settings: "+err.Error())
	}
	// A part that failed does not cancel the restart: /usr/local and /oem
	// are already the backup's, and only a restart makes them whole.
	_ = r.report()
	if yes || r.ask("Restart now? The users and passwords come back with the restart.") {
		if err := rebootCommand().Run(); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(r.stdout, "Restart before you use the machine.")
	}
	if len(r.failed) > 0 {
		return errors.New("some parts were not restored")
	}
	return nil
}

// keepBackupSettings decides the backup settings after a whole restore,
// which brought the old machine's config. When this machine had a target,
// its config and password come back. Otherwise the restored config stays,
// with the password typed for this backup when it has none: the old
// machine's password, which an export also uses.
func (r *restorer) keepBackupSettings(cfg backupConfig, pass string) error {
	if cfg.Target != "" {
		if err := saveBackupConfig(r.p, cfg); err != nil {
			return err
		}
		return writeSecret(r.p.password(), strings.TrimSpace(pass))
	}
	restored, err := loadBackupConfig(r.p)
	if err != nil || restored.Target == "" || r.pass == r.p.password() {
		return err
	}
	b, err := os.ReadFile(r.pass)
	if err != nil {
		return err
	}
	return writeSecret(r.p.password(), strings.TrimSpace(string(b)))
}

// replaceOEM makes the cloud-configs in dst the ones in src: the others are
// moved into aside first. Other files of dst stay.
func replaceOEM(src, dst, aside string) error {
	isConfig := func(n string) bool { return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") }
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isConfig(e.Name()) || exists(filepath.Join(src, e.Name())) {
			continue
		}
		if err := os.MkdirAll(aside, 0o700); err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(dst, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(aside, e.Name()), b, 0o600); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return copyTree(src, dst)
}

// queuePending keeps the manifest's app and extension lists for
// `isotope restore --finish`, which runs after the restart.
func (r *restorer) queuePending(manifest string) error {
	pending := r.p.pending()
	if err := os.RemoveAll(pending); err != nil {
		return err
	}
	if err := os.MkdirAll(pending, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(manifest)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if n != "users" && n != "extensions" && !strings.HasPrefix(n, "flatpak-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(manifest, n))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pending, n), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// finish is `isotope restore --finish`: isotope-restore-finish.service runs
// it at the first boot after a whole restore, when the users exist and the
// network is up. What failed goes to the journal and to `isotope backup
// status`.
func (r *restorer) finish() error {
	pending := r.p.pending()
	if !exists(pending) {
		return nil
	}
	r.parts = map[string]bool{"apps": true, "extensions": true}
	pl, err := r.readPlan(pending)
	if err != nil {
		return err
	}
	for _, a := range pl.apps {
		r.restoreApp(a)
	}
	for _, x := range pl.exts {
		r.restoreExt(x)
	}
	result := "ok"
	if len(r.failed) > 0 {
		result = "could not install: " + strings.Join(r.failed, "; ")
	}
	if err := os.WriteFile(r.p.finishResult(), []byte(result+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(pending); err != nil {
		return err
	}
	return r.report()
}

// runParts restores the parts --only names, into this machine's users.
func (r *restorer) runParts(stage string, pl plan, yes bool) error {
	r.showPlan(pl)
	if len(pl.homes) == 0 && len(pl.apps) == 0 && len(pl.exts) == 0 {
		fmt.Fprintln(r.stdout, "Nothing to restore.")
		return nil
	}
	if !yes && !r.ask("Restore it?") {
		fmt.Fprintln(r.stdout, "Nothing changed.")
		return nil
	}
	if len(pl.homes) > 0 {
		args := []string{"restore", r.snap, "--target", stage}
		for old := range pl.homes {
			args = append(args, "--include", filepath.Join(r.p.homeState(), old))
		}
		if err := r.restic(args...); err != nil {
			return fmt.Errorf("restic restore: %w", err)
		}
	}
	for _, old := range sortedKeys(pl.homes) {
		if err := r.restoreHome(stage, old, pl.homes[old]); err != nil {
			r.failed = append(r.failed, fmt.Sprintf("the files of %s: %v", old, err))
		}
	}
	for _, a := range pl.apps {
		r.restoreApp(a)
	}
	for _, x := range pl.exts {
		r.restoreExt(x)
	}
	return r.report()
}

func (r *restorer) report() error {
	if len(r.failed) == 0 {
		fmt.Fprintln(r.stdout, "Restored.")
		return nil
	}
	fmt.Fprintln(r.stdout, "Restored, except:")
	for _, f := range r.failed {
		fmt.Fprintln(r.stdout, "  -", f)
	}
	return errors.New("some parts were not restored")
}

// copyTree copies the files under src over the same paths in dst, keeping
// their modes. For /oem, which only root can write.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, info.Mode().Perm())
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tmp := to + ".isotope-restore"
			if err := os.WriteFile(tmp, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Rename(tmp, to)
		}
		return nil
	})
}

func readSecretLine(in *bufio.Reader) (string, error) {
	// No echo when stdin is a terminal.
	if old, err := unix.IoctlGetTermios(0, unix.TCGETS); err == nil {
		quiet := *old
		quiet.Lflag &^= unix.ECHO
		if unix.IoctlSetTermios(0, unix.TCSETS, &quiet) == nil {
			defer func() { _ = unix.IoctlSetTermios(0, unix.TCSETS, old); fmt.Println() }()
		}
	}
	line, err := in.ReadString('\n')
	pw := strings.TrimSpace(line)
	if pw == "" {
		if err == nil {
			err = errors.New("the password is empty")
		}
		return "", err
	}
	return pw, nil
}

func (r *restorer) readPlan(dir string) (plan, error) {
	pl := plan{homes: map[string]string{}}
	if rel, err := LoadRelease(filepath.Join(dir, "release")); err == nil {
		pl.release = "Isotope " + rel.Version + " (" + rel.Flavor + ")"
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	pl.hostname = read("hostname")
	pl.layout = read("layout")
	b, err := os.ReadFile(filepath.Join(dir, "users"))
	if err != nil {
		return pl, fmt.Errorf("the backup has no list of users: %w", err)
	}
	// Into which local user each backed-up user's files and apps go: the
	// same name, or --user. A whole-machine restore brings the users back
	// themselves.
	local := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		old := f[0]
		pl.users = append(pl.users, old)
		if r.whole() {
			local[old] = old
			continue
		}
		nw := old
		if m, ok := r.users[old]; ok {
			nw = m
		}
		if _, _, err := lookupUser(nw); err != nil {
			pl.skipped = append(pl.skipped, old)
			continue
		}
		local[old] = nw
	}
	for old := range r.users {
		if !contains(pl.users, old) {
			return pl, fmt.Errorf("--user %s=...: the backup has no user %s", old, old)
		}
	}
	if r.parts["homes"] {
		pl.homes = local
	}
	if r.whole() || r.parts["apps"] {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			u := ""
			switch {
			case name == "flatpak-system":
			case strings.HasPrefix(name, "flatpak-user-"):
				nw, ok := local[strings.TrimPrefix(name, "flatpak-user-")]
				if !ok {
					continue // its user is not restored
				}
				u = nw
			default:
				continue
			}
			for _, line := range strings.Split(read(name), "\n") {
				if f := strings.Fields(line); len(f) == 2 {
					pl.apps = append(pl.apps, appLine{user: u, id: f[0], origin: f[1]})
				}
			}
		}
	}
	if r.whole() || r.parts["extensions"] {
		for _, line := range strings.Split(read("extensions"), "\n") {
			if f := strings.Fields(line); len(f) == 3 {
				pl.exts = append(pl.exts, extLine{file: f[0], source: f[1], enabled: f[2] == "true"})
			}
		}
	}
	return pl, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (r *restorer) showPlan(pl plan) {
	if pl.release != "" {
		fmt.Fprintf(r.stdout, "This backup was made on %s.\n", pl.release)
	}
	for _, old := range sortedKeys(pl.homes) {
		if nw := pl.homes[old]; nw == old {
			fmt.Fprintf(r.stdout, "  the files of %s\n", old)
		} else {
			fmt.Fprintf(r.stdout, "  the files of %s, into the home of %s\n", old, nw)
		}
	}
	for _, s := range pl.skipped {
		fmt.Fprintf(r.stdout, "  not the files of %s: there is no user %s here (use --user %s=NAME)\n", s, s, s)
	}
	if len(pl.apps) > 0 {
		fmt.Fprintf(r.stdout, "  %d apps, installed again from Flathub\n", len(pl.apps))
	}
	if len(pl.exts) > 0 {
		fmt.Fprintf(r.stdout, "  %d system extensions, installed again\n", len(pl.exts))
	}
	fmt.Fprintln(r.stdout, "A restored file replaces a file at the same path; other files stay.")
}

// restoreHome moves the restored files of user old into the home of user nw.
//
// The user owns the home and may be logged in, so root must not follow a
// path there that the user can change midway: a directory swapped for a
// link to /etc would send a rename or a chown into /etc. So the owners are
// set first, in the stage, which is root's and closed to everyone; then the
// files are moved with directory file descriptors opened without following
// links, and nothing in the home is changed after the move.
func (r *restorer) restoreHome(stage, old, nw string) error {
	uid, gid, err := lookupUser(nw)
	if err != nil {
		return err
	}
	src := filepath.Join(stage, r.p.homeState(), old)
	if !exists(src) {
		return errors.New("not in the backup")
	}
	if err := chownTree(src, uid, gid); err != nil {
		return err
	}
	dst := filepath.Join(r.p.homeState(), nw)
	if err := os.Mkdir(dst, 0o700); err == nil {
		if err := os.Lchown(dst, uid, gid); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return mergeMove(src, dst)
}

// openDir opens a directory without following a link at its last
// component.
func openDir(dirfd int, name string) (int, error) {
	return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// mergeMove moves everything in src into dst. A directory that is in both
// is merged; anything else at the same path in dst is swapped into src, so
// it goes when the stage is removed. It goes on after an error and returns
// them all.
func mergeMove(src, dst string) error {
	sfd, err := openDir(unix.AT_FDCWD, src)
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	dfd, err := openDir(unix.AT_FDCWD, dst)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	return mergeAt(sfd, dfd, dst)
}

func mergeAt(sfd, dfd int, where string) error {
	names, err := readDirNames(sfd)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(sfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			errs = append(errs, err)
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			// Merge into a real directory; a link or a file of that name is
			// replaced below.
			if cd, err := openDir(dfd, name); err == nil {
				cs, err := openDir(sfd, name)
				if err == nil {
					err = mergeAt(cs, cd, filepath.Join(where, name))
					unix.Close(cs)
				}
				unix.Close(cd)
				if err != nil {
					errs = append(errs, err)
				}
				continue
			}
		}
		err := unix.Renameat2(sfd, name, dfd, name, unix.RENAME_NOREPLACE)
		if errors.Is(err, unix.EEXIST) {
			err = unix.Renameat2(sfd, name, dfd, name, unix.RENAME_EXCHANGE)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(where, name), err))
		}
	}
	return errors.Join(errs...)
}

// readDirNames lists a directory through its descriptor.
func readDirNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "")
	defer f.Close()
	return f.Readdirnames(-1)
}

// resolveSnapshot turns "latest" into a snapshot ID: the newest backup of
// this machine's kind (tag isotope), from the one host that made them, or
// the host --host names.
func (r *restorer) resolveSnapshot() (string, error) {
	if r.snap != "latest" {
		return r.snap, nil
	}
	args := []string{"snapshots", "--json", "--tag", "isotope", "--latest", "1"}
	if r.host != "" {
		args = append(args, "--host", r.host)
	}
	cmd, err := resticCmd(r.p, r.target, r.pass, args...)
	if err != nil {
		return "", err
	}
	cmd.Stderr = r.stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not read the backup (wrong password?): %w", err)
	}
	var snaps []struct {
		ID       string    `json:"id"`
		Hostname string    `json:"hostname"`
		Time     time.Time `json:"time"`
	}
	if err := json.Unmarshal(out, &snaps); err != nil {
		return "", fmt.Errorf("restic snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return "", errors.New("there is no backup there")
	}
	hosts := map[string]string{}
	for _, s := range snaps {
		hosts[s.Hostname] = s.ID
	}
	if len(hosts) > 1 {
		return "", fmt.Errorf("the target has the backups of several machines (%s); choose one with --host",
			strings.Join(sortedKeys(hosts), ", "))
	}
	// One host, but maybe more than one group of paths: the newest wins.
	newest := snaps[0]
	for _, s := range snaps[1:] {
		if s.Time.After(newest.Time) {
			newest = s
		}
	}
	return newest.ID, nil
}

// flathubRepo is where a user's flathub remote comes from when it is missing.
const flathubRepo = "https://dl.flathub.org/repo/flathub.flatpakrepo"

func (r *restorer) restoreApp(a appLine) {
	var cmd *exec.Cmd
	if a.user == "" {
		cmd = sysCommand("flatpak", "install", "--system", "--noninteractive", "-y", a.origin, a.id)
	} else {
		if a.origin == "flathub" {
			_ = sysCommand("runuser", "-u", a.user, "--", "flatpak", "remote-add", "--user", "--if-not-exists", "flathub", flathubRepo).Run()
		}
		cmd = sysCommand("runuser", "-u", a.user, "--", "flatpak", "install", "--user", "--noninteractive", "-y", a.origin, a.id)
	}
	fmt.Fprintf(r.stdout, "Installing %s...\n", a.id)
	cmd.Stdout, cmd.Stderr = io.Discard, r.stderr
	if err := cmd.Run(); err != nil {
		who := "for everyone"
		if a.user != "" {
			who = "for " + a.user
		}
		r.failed = append(r.failed, fmt.Sprintf("the app %s %s: %v", a.id, who, err))
	}
}

// restoreExt installs an extension again through `isotope ext`, which checks
// it the same way as when it was first installed.
func (r *restorer) restoreExt(x extLine) {
	self, err := os.Executable()
	if err != nil {
		self = "/usr/bin/isotope"
	}
	cmd := sysCommand(self, "ext", "install", x.source, "--yes")
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	if err := cmd.Run(); err != nil {
		r.failed = append(r.failed, fmt.Sprintf("the extension %s (from %s): %v", extDisplayName(x.file), x.source, err))
		return
	}
	if !x.enabled {
		cmd = sysCommand(self, "ext", "disable", extDisplayName(x.file))
		cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
		_ = cmd.Run()
	}
}
