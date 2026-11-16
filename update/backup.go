package main

// Backup and restore of a whole machine: the homes, the saved network
// connections, and lists of the Flatpak apps and system extensions to
// install again. The backups are restic repositories. See
// docs/superpowers/specs/2026-11-16-isotope-cli-backup-design.md.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// backupPaths are the places a backup reads and a restore writes. A struct,
// so tests can point every one of them into a temporary directory.
type backupPaths struct {
	State   string // /var/lib/isotope-backup: config, password, env, manifest
	Run     string // /run/isotope-backup: a restore's typed password (tmpfs)
	Home    string // /home
	NetConn string // NetworkManager's saved connections
	Release string // /etc/isotope/release
	ExtDir  string // where each extension came from (extStateDir)
}

// defaultBackupPaths is a variable so tests can move every path.
var defaultBackupPaths = func() backupPaths {
	return backupPaths{
		State:   "/var/lib/isotope-backup",
		Run:     "/run/isotope-backup",
		Home:    "/home",
		NetConn: "/etc/NetworkManager/system-connections",
		Release: releaseFile(),
		ExtDir:  extStateDir(),
	}
}

func (p backupPaths) config() string   { return filepath.Join(p.State, "config") }
func (p backupPaths) password() string { return filepath.Join(p.State, "password") }
func (p backupPaths) envFile() string  { return filepath.Join(p.State, "env") }
func (p backupPaths) manifest() string { return filepath.Join(p.State, "manifest") }

// stage is where a restore puts the files first: under /home, on the same
// file system as the homes, so moving them into place is a rename.
func (p backupPaths) stage() string { return filepath.Join(p.Home, ".isotope-restore") }

// restic is the backup engine. ISOTOPE_RESTIC replaces it, so a test can
// record what would have been run.
func restic() string {
	if p := os.Getenv("ISOTOPE_RESTIC"); p != "" {
		return p
	}
	return "/usr/bin/restic"
}

// sysCommand builds every command a backup or restore runs (restic,
// flatpak, nmcli, runuser). A variable so tests can see them.
var sysCommand = exec.Command

// flatpakPresent says whether this edition has Flatpak (the server has not).
var flatpakPresent = func() bool { _, err := exec.LookPath("flatpak"); return err == nil }

// backupNow is the clock of the schedule. A variable for the tests.
var backupNow = time.Now

// Snapshots older than this many backups are forgotten after each backup.
var keepPolicy = []string{"--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "12"}

// The parts of /home that are caches or can be downloaded again.
var homeExcludes = []string{
	".cache",
	".local/share/Trash",
	".local/share/flatpak",
	".local/share/containers",
	".var/app/*/cache",
}

// backupConfig is /var/lib/isotope-backup/config. It holds no secret, so
// `isotope status` can read it as any user.
type backupConfig struct {
	Target   string
	Schedule string // off, daily, weekly
	Last     int64  // Unix time of the last successful backup
	Result   string // ok, or the error of the last attempt
}

func loadBackupConfig(p backupPaths) (backupConfig, error) {
	c := backupConfig{Schedule: "off"}
	b, err := os.ReadFile(p.config())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "target":
			c.Target = v
		case "schedule":
			if v == "daily" || v == "weekly" || v == "off" {
				c.Schedule = v
			}
		case "last":
			c.Last, _ = strconv.ParseInt(v, 10, 64)
		case "result":
			c.Result = v
		}
	}
	return c, nil
}

func saveBackupConfig(p backupPaths, c backupConfig) error {
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		return err
	}
	s := fmt.Sprintf("target=%s\nschedule=%s\nlast=%d\nresult=%s\n",
		c.Target, c.Schedule, c.Last, oneLine(c.Result))
	tmp := p.config() + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.config())
}

// The restic repository schemes a target may use besides a directory.
var urlSchemes = []string{"sftp:", "rest:", "s3:", "b2:", "azure:", "gs:", "swift:", "rclone:"}

// isURLTarget says whether a target is a restic URL rather than a directory.
func isURLTarget(t string) bool {
	for _, sc := range urlSchemes {
		if strings.HasPrefix(t, sc) {
			return true
		}
	}
	return false
}

// normalizeTarget checks a target the user typed: an absolute directory
// (also written local:/path) or a restic URL. A relative path is refused:
// restic would put the backup in the current directory, on the internal
// disk.
func normalizeTarget(t string) (string, error) {
	t = strings.TrimPrefix(t, "local:")
	if isURLTarget(t) {
		return t, nil
	}
	if !filepath.IsAbs(t) {
		return "", fmt.Errorf("%q is not an absolute path or a restic URL (%s...)", t, strings.Join(urlSchemes, ", "))
	}
	return filepath.Clean(t), nil
}

// onTmpfs says whether a directory is on a tmpfs, such as /run, which is
// empty again after a reboot. A variable for the tests.
var onTmpfs = func(dir string) bool {
	var st unix.Statfs_t
	return unix.Statfs(dir, &st) == nil && st.Type == unix.TMPFS_MAGIC
}

// onInterrupt runs fn and exits on SIGINT or SIGTERM, until the returned
// stop is called. restic gets the signal too, as it is in the same process
// group, and stops.
func onInterrupt(fn func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			fn()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

// resticCmd runs restic against target with the password in passFile.
func resticCmd(p backupPaths, target, passFile string, args ...string) (*exec.Cmd, error) {
	c := sysCommand(restic(), args...)
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+target,
		"RESTIC_PASSWORD_FILE="+passFile,
		"RESTIC_CACHE_DIR="+filepath.Join(p.State, "cache"))
	b, err := os.ReadFile(p.envFile())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if k, _, ok := strings.Cut(line, "="); ok && k != "" && !strings.HasPrefix(line, "#") {
			env = append(env, line)
		}
	}
	c.Env = env
	return c, nil
}

const backupUsage = `Usage: isotope backup [command]

Back up this machine: every home in /home (without caches), the saved
network connections, and the lists of Flatpak apps and system extensions.
The backup is encrypted. Runs as root through sudo; only members of the
admin group may use it.

Commands:
  now                    back up now (default)
  status                 the target, the schedule and the last backup
  init TARGET            back up to TARGET from now on: a directory, such as
                         a folder on a USB disk, or a restic URL (sftp:...,
                         rest:..., s3:...). Creates the backup there with a
                         new password, which is shown once
      --password-stdin   read the password instead: for a backup that
                         already exists, or a password of your own
  list                   the backups in the target
  schedule daily|weekly|off
                         back up automatically. A USB disk that is not
                         plugged in is skipped, not an error

Restore with "isotope restore".
`

func cmdBackup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := "now"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" || sub == "-h" || sub == "--help" {
		fmt.Fprint(stdout, backupUsage)
		return exitOK
	}
	p := defaultBackupPaths()
	if sub == "status" {
		return backupStatus(p, stdout, stderr)
	}
	if !exists(restic()) {
		fmt.Fprintln(stderr, "isotope: backup is not available on this edition (no restic).")
		return exitError
	}
	if needRoot() {
		if err := asRoot(append([]string{"backup", sub}, args...)...).Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			fmt.Fprintln(stderr, "isotope:", err)
			return exitError
		}
		return exitOK
	}
	fs := newFlags("backup "+sub, stderr)
	passStdin := fs.Bool("password-stdin", false, "")
	scheduled := fs.Bool("scheduled", false, "")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	in := bufio.NewReader(stdin)
	switch sub {
	case "now":
		if len(rest) != 0 {
			break
		}
		return backupRun(p, *scheduled, stdout, stderr)
	case "init":
		if len(rest) != 1 {
			break
		}
		return backupInit(p, rest[0], *passStdin, in, stdout, stderr)
	case "list":
		if len(rest) != 0 {
			break
		}
		return backupList(p, stdout, stderr)
	case "schedule":
		if len(rest) != 1 {
			break
		}
		return backupSchedule(p, rest[0], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "isotope backup: unknown command %q\n\n%s", sub, backupUsage)
		return exitUsage
	}
	fmt.Fprint(stderr, backupUsage)
	return exitUsage
}

func backupStatus(p backupPaths, stdout, stderr io.Writer) int {
	c, err := loadBackupConfig(p)
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if c.Target == "" {
		fmt.Fprintln(stdout, "No backup target. Run `isotope backup init TARGET` to set one, for example a folder on a USB disk.")
		return exitOK
	}
	fmt.Fprintf(stdout, "Target:     %s\n", c.Target)
	fmt.Fprintf(stdout, "Schedule:   %s\n", c.Schedule)
	if c.Last == 0 {
		fmt.Fprintln(stdout, "Last:       never")
	} else {
		fmt.Fprintf(stdout, "Last:       %s\n", time.Unix(c.Last, 0).Format("2006-01-02 15:04"))
	}
	switch {
	case strings.HasPrefix(c.Result, "partial: "):
		fmt.Fprintf(stdout, "Last try:   %s\n", c.Result)
	case c.Result != "" && c.Result != "ok":
		fmt.Fprintf(stdout, "Last try:   failed: %s\n", c.Result)
	}
	if w := backupWarning(c); w != "" {
		fmt.Fprintln(stdout, w)
	}
	return exitOK
}

// backupWarning is the line `isotope status` adds when automatic backups
// have stopped working.
func backupWarning(c backupConfig) string {
	if c.Target == "" || c.Schedule == "off" {
		return ""
	}
	if c.Last == 0 || backupNow().Sub(time.Unix(c.Last, 0)) > 14*24*time.Hour {
		return "Backup:     no backup in the last 14 days; run `isotope backup` to see why."
	}
	return ""
}

func backupSchedule(p backupPaths, s string, stdout, stderr io.Writer) int {
	if s != "daily" && s != "weekly" && s != "off" {
		fmt.Fprintf(stderr, "isotope backup schedule: %q is not daily, weekly or off\n", s)
		return exitUsage
	}
	c, err := loadBackupConfig(p)
	if err == nil && c.Target == "" && s != "off" {
		err = errors.New("there is no backup target; run `isotope backup init TARGET` first")
	}
	if err == nil {
		c.Schedule = s
		err = saveBackupConfig(p, c)
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if s == "off" {
		fmt.Fprintln(stdout, "Automatic backups are off.")
	} else {
		fmt.Fprintf(stdout, "This machine backs up %s to %s.\n", s, c.Target)
	}
	return exitOK
}

// targetPresent says whether a directory target holds a restic repository
// now. A USB disk that is not plugged in leaves the mount point missing or
// empty; restic must then not be pointed at it, or it would write to the
// internal disk.
func targetPresent(target string) bool {
	return isURLTarget(target) || exists(filepath.Join(target, "config"))
}

// scheduledSkip says why a scheduled backup does nothing, or "" when it is
// due.
func scheduledSkip(c backupConfig) string {
	switch {
	case c.Target == "":
		return "no backup target"
	case c.Schedule == "off":
		return "automatic backups are off"
	case c.Schedule == "weekly" && c.Last != 0 && backupNow().Sub(time.Unix(c.Last, 0)) < 7*24*time.Hour:
		return "the last backup is less than a week old"
	case !targetPresent(c.Target):
		return c.Target + " is not there (a disk that is not plugged in?)"
	}
	return ""
}

func backupRun(p backupPaths, scheduled bool, stdout, stderr io.Writer) int {
	c, err := loadBackupConfig(p)
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if scheduled {
		if why := scheduledSkip(c); why != "" {
			fmt.Fprintln(stdout, "No backup:", why+".")
			return exitOK
		}
	} else {
		if c.Target == "" {
			fmt.Fprintln(stderr, "isotope: there is no backup target; run `isotope backup init TARGET` first.")
			return exitError
		}
		if !targetPresent(c.Target) {
			fmt.Fprintf(stderr, "isotope: there is no backup in %s. Is the disk plugged in?\n", c.Target)
			return exitError
		}
	}
	err = backupOnce(p, c.Target, stdout, stderr)
	switch {
	case errors.Is(err, errPartial):
		c.Result, c.Last = "partial: "+err.Error(), backupNow().Unix()
	case err != nil:
		c.Result = err.Error()
	default:
		c.Result, c.Last = "ok", backupNow().Unix()
	}
	if serr := saveBackupConfig(p, c); serr != nil && err == nil {
		err = serr
	}
	if errors.Is(err, errPartial) {
		fmt.Fprintf(stderr, "isotope: backed up, but %v.\n", err)
		return exitOK
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope: the backup failed:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "Backed up.")
	return exitOK
}

func backupOnce(p backupPaths, target string, stdout, stderr io.Writer) error {
	if err := writeManifest(p); err != nil {
		return fmt.Errorf("writing the manifest: %w", err)
	}
	host, _ := os.Hostname()
	args := []string{"backup", "--tag", "isotope", "--host", host, "--exclude-caches"}
	for _, x := range homeExcludes {
		args = append(args, "--exclude", filepath.Join(p.Home, "*", x))
	}
	// A restore's staging directory, and a target inside /home, which would
	// otherwise be backed up into itself.
	args = append(args, "--exclude", p.stage())
	if !isURLTarget(target) && strings.HasPrefix(target, p.Home+"/") {
		args = append(args, "--exclude", target)
	}
	args = append(args, p.Home, p.manifest())
	if exists(p.NetConn) {
		args = append(args, p.NetConn)
	}
	cmd, err := resticCmd(p, target, p.password(), args...)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	var partial error
	if err := cmd.Run(); err != nil {
		// Exit 3: some files could not be read, and the snapshot of the
		// others was still written. Keep it and prune as usual.
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3 {
			return fmt.Errorf("restic backup: %w", err)
		}
		partial = errPartial
	}
	forget := append([]string{"forget", "--tag", "isotope", "--host", host, "--prune"}, keepPolicy...)
	if cmd, err = resticCmd(p, target, p.password(), forget...); err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return partial
}

// errPartial is a backup that left out files it could not read.
var errPartial = errors.New("some files could not be read and are not in the backup")

// manifestUser is one line of manifest/users.
type manifestUser struct {
	Name string
	UID  int
}

// homeUsers are the users with a home in /home: every directory there that
// is not hidden, named after its owner.
func homeUsers(home string) ([]manifestUser, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var out []manifestUser
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		u := manifestUser{Name: e.Name(), UID: -1}
		if info, err := e.Info(); err == nil {
			u.UID = fileUID(info)
		}
		out = append(out, u)
	}
	return out, nil
}

func fileUID(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// writeManifest writes what a restore needs to know besides the files.
func writeManifest(p backupPaths) error {
	dir := p.manifest()
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if b, err := os.ReadFile(p.Release); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "release"), b, 0o644); err != nil {
			return err
		}
	}
	users, err := homeUsers(p.Home)
	if err != nil {
		return err
	}
	var ub strings.Builder
	for _, u := range users {
		fmt.Fprintf(&ub, "%s %d\n", u.Name, u.UID)
	}
	if err := os.WriteFile(filepath.Join(dir, "users"), []byte(ub.String()), 0o644); err != nil {
		return err
	}
	// The apps, when this edition has Flatpak. A failed listing leaves the
	// file out rather than failing the backup of the files.
	if flatpakPresent() {
		if out, err := sysCommand("flatpak", "list", "--system", "--app", "--columns=application,origin").Output(); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "flatpak-system"), out, 0o644)
		}
		for _, u := range users {
			out, err := sysCommand("runuser", "-u", u.Name, "--", "flatpak", "list", "--user", "--app", "--columns=application,origin").Output()
			if err == nil && len(strings.TrimSpace(string(out))) > 0 {
				_ = os.WriteFile(filepath.Join(dir, "flatpak-user-"+u.Name), out, 0o644)
			}
		}
	}
	// The names of the saved connections, so a restore can count them.
	var cb strings.Builder
	if entries, err := os.ReadDir(p.NetConn); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() {
				fmt.Fprintln(&cb, e.Name())
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "connections"), []byte(cb.String()), 0o644); err != nil {
		return err
	}
	// The extensions: the file name, where it came from, and whether it is
	// enabled at boot.
	list, err := listInstalled()
	if err != nil {
		return err
	}
	var xb strings.Builder
	for _, x := range list {
		if x.Source == "" {
			continue
		}
		fmt.Fprintf(&xb, "%s %s %t\n", x.File, x.Source, x.Enabled)
	}
	return os.WriteFile(filepath.Join(dir, "extensions"), []byte(xb.String()), 0o644)
}

func backupList(p backupPaths, stdout, stderr io.Writer) int {
	c, err := loadBackupConfig(p)
	if err == nil && c.Target == "" {
		err = errors.New("there is no backup target; run `isotope backup init TARGET` first")
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	cmd, err := resticCmd(p, c.Target, p.password(), "snapshots", "--tag", "isotope")
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(stderr, "isotope: could not list the backups:", err)
		return exitError
	}
	return exitOK
}

// writeSecret writes a 0600 file that never exists with wider permissions.
func writeSecret(path, s string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func backupInit(p backupPaths, target string, passStdin bool, in *bufio.Reader, stdout, stderr io.Writer) int {
	target, err := normalizeTarget(target)
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if !isURLTarget(target) {
		// Only the last directory is created: a missing parent is most likely
		// a USB disk that is not mounted, and creating its mount point would
		// put the backup on the internal disk.
		if !exists(filepath.Dir(target)) {
			fmt.Fprintf(stderr, "isotope: %s does not exist. Is the disk plugged in?\n", filepath.Dir(target))
			return exitError
		}
		// /run/media/anna exists while another disk is mounted; without the
		// disk, the folder would go on the tmpfs and vanish at the reboot.
		if onTmpfs(filepath.Dir(target)) {
			fmt.Fprintf(stderr, "isotope: %s is not on a disk (it is in memory, and empty after a reboot). Is the disk plugged in?\n", filepath.Dir(target))
			return exitError
		}
		if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			fmt.Fprintln(stderr, "isotope:", err)
			return exitError
		}
	}
	var password string
	existing := targetPresent(target) && !isURLTarget(target)
	if passStdin {
		line, err := in.ReadString('\n')
		password = strings.TrimSpace(line)
		if password == "" {
			if err == nil {
				err = errors.New("the password is empty")
			}
			fmt.Fprintln(stderr, "isotope: no password on stdin:", err)
			return exitError
		}
	} else {
		if existing {
			fmt.Fprintf(stderr, "isotope: %s already holds a backup; give its password with --password-stdin.\n", target)
			return exitError
		}
		key, err := newBackupKey()
		if err != nil {
			fmt.Fprintln(stderr, "isotope:", err)
			return exitError
		}
		if !confirmBackupKey(key, in, stdout) {
			fmt.Fprintln(stderr, "isotope: the password was not confirmed; nothing changed.")
			return exitError
		}
		password = key
	}

	// The password goes to a new file first: an init that fails must not
	// replace the password of a target that works.
	tmpPass := p.password() + ".new"
	if err := writeSecret(tmpPass, password); err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	defer os.Remove(tmpPass)
	op := "init"
	if existing || (passStdin && isURLTarget(target)) {
		// Opening, not creating: `cat config` fails on a wrong password.
		op = "cat"
	}
	args := []string{"init"}
	if op == "cat" {
		args = []string{"cat", "config"}
	}
	cmd, err := resticCmd(p, target, tmpPass, args...)
	if err == nil {
		cmd.Stdout, cmd.Stderr = io.Discard, stderr
		err = cmd.Run()
	}
	if err != nil && op == "cat" && passStdin && isURLTarget(target) {
		// A URL target given a password may be new: create it.
		if cmd, err = resticCmd(p, target, tmpPass, "init"); err == nil {
			cmd.Stdout, cmd.Stderr = io.Discard, stderr
			err = cmd.Run()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "isotope: could not use %s: %v\n", target, err)
		return exitError
	}
	if err := os.Rename(tmpPass, p.password()); err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	c, _ := loadBackupConfig(p)
	c.Target, c.Last, c.Result = target, 0, ""
	if err := saveBackupConfig(p, c); err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "This machine now backs up to %s. Run `isotope backup now` for the first backup", target)
	if c.Schedule == "off" {
		fmt.Fprint(stdout, ", and `isotope backup schedule daily` to back up every day")
	}
	fmt.Fprintln(stdout, ".")
	if isURLTarget(target) {
		fmt.Fprintf(stdout, "Credentials for %s go in %s, one NAME=value per line.\n", target, p.envFile())
	}
	return exitOK
}

// newBackupKey makes the password of a new backup. A variable for the tests.
var newBackupKey = newRecoveryKey

// confirmBackupKey shows the new password once and has one group of it typed
// back, as the installer does for a recovery key.
func confirmBackupKey(key string, in *bufio.Reader, stdout io.Writer) bool {
	groups := strings.Split(key, "-")
	fmt.Fprintln(stdout, "The password of this backup is:")
	fmt.Fprintln(stdout)
	for i := 0; i < len(groups); i += 2 {
		fmt.Fprintf(stdout, "    %s\n", strings.Join(groups[i:min(i+2, len(groups))], "-"))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Write it down and keep it away from this computer. Without it the")
	fmt.Fprintln(stdout, "backup cannot be restored, and it is shown only now.")
	n := int(backupNow().UnixNano()%int64(len(groups))) + 1
	for tries := 0; tries < 3; tries++ {
		fmt.Fprintf(stdout, "Type group %d of the password to confirm: ", n)
		line, err := in.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) == groups[n-1] {
			return true
		}
		if err != nil {
			return false
		}
		fmt.Fprintln(stdout, "That is not the group shown above.")
	}
	return false
}

// sortedKeys is for messages that list a map.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// chownTree gives every file under root to uid:gid, without following links
// and without stopping at the first error. Only for a tree no user can
// change while it runs: a restore's stage.
func chownTree(root string, uid, gid int) error {
	var errs []error
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			err = os.Lchown(path, uid, gid)
		}
		if err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	return errors.Join(errs...)
}

// lookupUser is the local account for a name. A variable for the tests.
var lookupUser = func(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, nil
}
