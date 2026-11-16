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

Restore a backup onto this machine: the files of each user, the saved
network connections, the Flatpak apps and the system extensions. SNAPSHOT is
an ID from "isotope backup list"; the default is the latest. Runs as root
through sudo.

Flags:
  --from TARGET        restore from TARGET (a directory or a restic URL)
                       instead of this machine's backup target; asks for
                       its password
  --only LIST          only some of homes,network,apps,extensions
  --user OLD=NEW       restore the files of user OLD into NEW's home; may
                       be given more than once. Without it each user goes
                       to the local user of the same name
  --host NAME          the machine whose latest backup to restore, when
                       several back up to the same target
  --yes                do not ask for confirmation
`

// restoreParts are what a restore can do, in the order it does them.
var restoreParts = []string{"homes", "network", "apps", "extensions"}

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
	parts := map[string]bool{}
	for _, x := range restoreParts {
		parts[x] = *only == ""
	}
	for _, x := range strings.Split(*only, ",") {
		if x == "" {
			continue
		}
		if _, ok := parts[x]; !ok {
			fmt.Fprintf(stderr, "isotope restore: --only: %q is not one of %s\n", x, strings.Join(restoreParts, ","))
			return exitUsage
		}
		parts[x] = true
	}
	r := restorer{p: defaultBackupPaths(), snap: snap, host: *host, users: users, parts: parts,
		in: bufio.NewReader(stdin), stdout: stdout, stderr: stderr}
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
	parts          map[string]bool
	in             *bufio.Reader
	stdout, stderr io.Writer
	failed         []string // what could not be restored, said at the end
}

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
	release string
	homes   map[string]string // backed-up user -> local user
	skipped []string          // backed-up users with no local user
	conns   int
	apps    []appLine
	exts    []extLine
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
	// The temporary password and the staged files go, whatever ends the
	// restore: an error, or Ctrl-C during a long restic run.
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
	}
	if r.target == "" {
		return errors.New("there is no backup target; give one with --from")
	}
	if !targetPresent(r.target) {
		return fmt.Errorf("there is no backup in %s. Is the disk plugged in?", r.target)
	}

	// The restore is staged under /home, on the same file system as the
	// homes, so moving the files into place is a rename and not a copy.
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
	r.showPlan(pl)
	if len(pl.homes) == 0 && pl.conns == 0 && len(pl.apps) == 0 && len(pl.exts) == 0 {
		fmt.Fprintln(r.stdout, "Nothing to restore.")
		return nil
	}
	if !yes && !r.ask("Restore it?") {
		fmt.Fprintln(r.stdout, "Nothing changed.")
		return nil
	}

	if len(pl.homes) > 0 || pl.conns > 0 {
		args := []string{"restore", r.snap, "--target", stage}
		for old := range pl.homes {
			args = append(args, "--include", filepath.Join(r.p.Home, old))
		}
		if pl.conns > 0 {
			args = append(args, "--include", r.p.NetConn)
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
	if pl.conns > 0 {
		r.restoreNetwork(stage)
	}
	for _, a := range pl.apps {
		r.restoreApp(a)
	}
	for _, x := range pl.exts {
		r.restoreExt(x)
	}

	if len(r.failed) > 0 {
		fmt.Fprintln(r.stdout, "Restored, except:")
		for _, f := range r.failed {
			fmt.Fprintln(r.stdout, "  -", f)
		}
	} else {
		fmt.Fprintln(r.stdout, "Restored.")
	}
	if from != "" && from != cfg.Target && r.ask("Back up this machine to "+from+" from now on?") {
		b, err := os.ReadFile(r.pass)
		if err == nil {
			err = writeSecret(r.p.password(), strings.TrimSpace(string(b)))
		}
		if err != nil {
			return err
		}
		cfg.Target, cfg.Last, cfg.Result = from, 0, ""
		if err := saveBackupConfig(r.p, cfg); err != nil {
			return err
		}
		fmt.Fprintln(r.stdout, "Done. Run `isotope backup schedule daily` to back up every day.")
	}
	if len(r.failed) > 0 {
		return errors.New("some parts were not restored")
	}
	return nil
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
	// The users are matched for the apps too: a user's apps go to the same
	// local user as their files, even with --only apps.
	local := map[string]string{}
	if r.parts["homes"] || r.parts["apps"] {
		b, err := os.ReadFile(filepath.Join(dir, "users"))
		if err != nil {
			return pl, fmt.Errorf("the backup has no list of users: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			old := f[0]
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
			if _, ok := local[old]; !ok && !contains(pl.skipped, old) {
				return pl, fmt.Errorf("--user %s=...: the backup has no user %s", old, old)
			}
		}
		if r.parts["homes"] {
			pl.homes = local
		}
	}
	if r.parts["network"] {
		// The manifest lists the connection files, so the plan can count
		// them before the files are restored.
		if b, err := os.ReadFile(filepath.Join(dir, "connections")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) != "" {
					pl.conns++
				}
			}
		}
	}
	if r.parts["apps"] {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			u := ""
			switch {
			case name == "flatpak-system":
			case strings.HasPrefix(name, "flatpak-user-"):
				u = strings.TrimPrefix(name, "flatpak-user-")
				nw, ok := local[u]
				if !ok {
					continue // its user is not restored
				}
				u = nw
			default:
				continue
			}
			b, _ := os.ReadFile(filepath.Join(dir, name))
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) == 2 {
					pl.apps = append(pl.apps, appLine{user: u, id: f[0], origin: f[1]})
				}
			}
		}
	}
	if r.parts["extensions"] {
		b, _ := os.ReadFile(filepath.Join(dir, "extensions"))
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 3 {
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
	if pl.conns > 0 {
		fmt.Fprintf(r.stdout, "  %d network connections\n", pl.conns)
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
	src := filepath.Join(stage, r.p.Home, old)
	if !exists(src) {
		return errors.New("not in the backup")
	}
	if err := chownTree(src, uid, gid); err != nil {
		return err
	}
	dst := filepath.Join(r.p.Home, nw)
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

func (r *restorer) restoreNetwork(stage string) {
	src := filepath.Join(stage, r.p.NetConn)
	entries, err := os.ReadDir(src)
	if err != nil {
		r.failed = append(r.failed, "the network connections: "+err.Error())
		return
	}
	if err := os.MkdirAll(r.p.NetConn, 0o700); err != nil {
		r.failed = append(r.failed, "the network connections: "+err.Error())
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err == nil {
			// A new file, so it is root's and 0600, as NetworkManager wants.
			err = writeSecret(filepath.Join(r.p.NetConn, e.Name()), strings.TrimSuffix(string(b), "\n"))
		}
		if err != nil {
			r.failed = append(r.failed, "the connection "+e.Name()+": "+err.Error())
		}
	}
	_ = sysCommand("nmcli", "connection", "reload").Run()
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
