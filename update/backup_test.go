package main

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backupSetup points every backup path into a temporary directory, with one
// user "anna" in the fake /home, and runs as root without sudo.
func backupSetup(t *testing.T) backupPaths {
	t.Helper()
	root := t.TempDir()
	p := backupPaths{
		Persist:    filepath.Join(root, "usr-local"),
		OEM:        filepath.Join(root, "oem"),
		State:      filepath.Join(root, "usr-local", "isotope-backup"),
		Run:        filepath.Join(root, "run"),
		Release:    filepath.Join(root, "release"),
		Encryption: filepath.Join(root, "encryption"),
		ExtDir:     filepath.Join(root, "extstate"),
	}
	home := p.homeState()
	for _, d := range []string{home + "/anna/Documents", home + "/anna/.cache/big", p.OEM, p.bindDir("/etc/ssh")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, home+"/anna/Documents/thesis.txt", "chapter one\n")
	write(t, home+"/anna/.cache/big/blob", "cache\n")
	write(t, p.bindDir("/etc/ssh")+"/ssh_host_ed25519_key", "host key\n")
	write(t, p.OEM+"/90_custom.yaml", "users: [anna]\n")
	write(t, p.Release, "FLAVOR=sway\nVERSION=v1.2.0\nIMAGE_REPO=ghcr.io/x/isotope\nGITHUB_REPO=x/isotope\n")
	t.Setenv("ISOTOPE_UPDATE_EXT_DIR", filepath.Join(root, "ext"))
	t.Setenv("ISOTOPE_UPDATE_EXT_STATE", p.ExtDir)

	oldPaths, oldRoot, oldFlatpak, oldLookup, oldTmpfs := defaultBackupPaths, needRoot, flatpakPresent, lookupUser, onTmpfs
	oldLayout, oldMounts := diskLayout, mountinfoPath
	onTmpfs = func(string) bool { return false } // t.TempDir may be a tmpfs
	diskLayout = func(p backupPaths) string { return "encryption=" + encryptionMode(p) }
	mountinfoPath = filepath.Join(root, "no-mountinfo")
	defaultBackupPaths = func() backupPaths { return p }
	needRoot = func() bool { return false }
	flatpakPresent = func() bool { return false }
	lookupUser = func(name string) (int, int, error) {
		if name == "anna" || name == "ben" {
			return os.Getuid(), os.Getgid(), nil
		}
		return 0, 0, os.ErrNotExist
	}
	t.Cleanup(func() {
		defaultBackupPaths, needRoot, flatpakPresent, lookupUser, onTmpfs = oldPaths, oldRoot, oldFlatpak, oldLookup, oldTmpfs
		diskLayout, mountinfoPath = oldLayout, oldMounts
	})
	return p
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRestic records each call's arguments and repository in log, one call
// per line, and succeeds.
func fakeRestic(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$RESTIC_REPOSITORY | $*\" >> "+log+"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISOTOPE_RESTIC", bin)
	return log
}

func TestUpdateIsUpgrade(t *testing.T) {
	setup(t, "v0.1.0", "v0.1.0")
	code, out, errOut := runCLI("", "update", "--yes")
	wcode, wout, werr := runCLI("", "upgrade", "--yes")
	if code != wcode || out != wout || errOut != werr {
		t.Fatalf("update: %d %q %q; upgrade: %d %q %q", code, out, errOut, wcode, wout, werr)
	}
}

func TestUsageNamesTheCommands(t *testing.T) {
	_, out, _ := runCLI("", "help")
	for _, w := range []string{"Usage: isotope ", "backup [command]", "restore", "recovery [command]", "isotope-ext X"} {
		if !strings.Contains(out, w) {
			t.Errorf("usage lacks %q", w)
		}
	}
}

func TestBackupRunsResticWithTheExcludes(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	usb := t.TempDir()
	write(t, usb+"/config", "restic")
	if err := saveBackupConfig(p, backupConfig{Target: usb, Schedule: "daily"}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("", "backup")
	if code != exitOK || !strings.Contains(out, "Backed up.") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	b, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) != 2 {
		t.Fatalf("restic calls: %q", calls)
	}
	home := p.homeState()
	for _, w := range []string{usb + " | backup --tag isotope", "--exclude-caches",
		"--exclude " + home + "/*/.cache", "--exclude " + home + "/*/.local/share/flatpak",
		"--exclude " + home + "/*/.local/share/containers",
		"--exclude " + p.Persist + "/.state/var-lib-docker.bind",
		"--exclude " + p.Persist + "/.state/var-log.bind",
		"--exclude " + p.Persist + "/.isotope-restore",
		"--exclude " + p.State + "/password ", "--exclude " + p.State + "/cache ",
		"--exclude " + p.State + "/work-* ", "--exclude " + p.State + "/pending ",
		" " + p.Persist + " " + p.OEM} {
		if !strings.Contains(calls[0], w) {
			t.Errorf("backup call lacks %q: %s", w, calls[0])
		}
	}
	if !strings.Contains(calls[1], "forget --tag isotope") || !strings.Contains(calls[1], "--keep-daily 7 --keep-weekly 4 --keep-monthly 12") {
		t.Errorf("forget call: %s", calls[1])
	}
	c, _ := loadBackupConfig(p)
	if c.Result != "ok" || c.Last == 0 {
		t.Errorf("config after a backup: %+v", c)
	}
	users, _ := os.ReadFile(p.State + "/manifest/users")
	if !strings.HasPrefix(string(users), "anna ") {
		t.Errorf("manifest users: %q", users)
	}
	layout, _ := os.ReadFile(p.State + "/manifest/layout")
	if string(layout) != "encryption=none\n" {
		t.Errorf("manifest layout: %q", layout)
	}
}

func TestBackupFailureIsRecorded(t *testing.T) {
	p := backupSetup(t)
	bin := filepath.Join(t.TempDir(), "restic")
	write(t, bin, "#!/bin/sh\nexit 3\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	saveBackupConfig(p, backupConfig{Target: "sftp:nas:/backup", Schedule: "daily", Last: 1})
	if code, _, errOut := runCLI("", "backup", "now"); code != exitError || !strings.Contains(errOut, "the backup failed") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	c, _ := loadBackupConfig(p)
	if c.Result == "ok" || c.Result == "" || c.Last != 1 {
		t.Errorf("config after a failure: %+v", c)
	}
	if _, out, _ := runCLI("", "backup", "status"); !strings.Contains(out, "Last try:   failed") || !strings.Contains(out, "no backup in the last 14 days") {
		t.Errorf("status: %q", out)
	}
}

func TestScheduledBackupSkips(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := backupNow
	backupNow = func() time.Time { return now }
	t.Cleanup(func() { backupNow = old })
	usb := t.TempDir()
	write(t, usb+"/config", "restic")
	unplugged := t.TempDir() // the mount point is there, the disk is not
	day := int64(24 * 3600)
	for _, tc := range []struct {
		name string
		c    backupConfig
		skip bool
	}{
		{"no target", backupConfig{Schedule: "daily"}, true},
		{"off", backupConfig{Target: usb, Schedule: "off"}, true},
		{"daily", backupConfig{Target: usb, Schedule: "daily", Last: now.Unix() - day/2}, false},
		{"weekly, recent", backupConfig{Target: usb, Schedule: "weekly", Last: now.Unix() - 3*day}, true},
		{"weekly, due", backupConfig{Target: usb, Schedule: "weekly", Last: now.Unix() - 8*day}, false},
		{"weekly, never", backupConfig{Target: usb, Schedule: "weekly"}, false},
		{"usb unplugged", backupConfig{Target: unplugged, Schedule: "daily"}, true},
		{"usb mount point gone", backupConfig{Target: "/run/media/anna/Gone", Schedule: "daily"}, true},
		{"server", backupConfig{Target: "sftp:nas:/backup", Schedule: "daily"}, false},
	} {
		if got := scheduledSkip(tc.c) != ""; got != tc.skip {
			t.Errorf("%s: skip %v, want %v (%q)", tc.name, got, tc.skip, scheduledSkip(tc.c))
		}
	}
}

func TestScheduledBackupOfAnUnpluggedDiskIsNotAnError(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	saveBackupConfig(p, backupConfig{Target: t.TempDir(), Schedule: "daily"})
	code, out, _ := runCLI("", "backup", "--scheduled")
	if code != exitOK || !strings.Contains(out, "No backup:") {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
	// By hand it is an error: the user asked for a backup.
	if code, _, errOut := runCLI("", "backup", "now"); code != exitError || !strings.Contains(errOut, "plugged in") {
		t.Fatalf("by hand: exit %d, err %q", code, errOut)
	}
}

func TestBackupScheduleNeedsATarget(t *testing.T) {
	backupSetup(t)
	fakeRestic(t)
	if code, _, errOut := runCLI("", "backup", "schedule", "daily"); code != exitError || !strings.Contains(errOut, "backup init") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if code, _, _ := runCLI("", "backup", "schedule", "hourly"); code != exitUsage {
		t.Fatalf("hourly: exit %d", code)
	}
}

func TestBackupWithoutResticSaysSo(t *testing.T) {
	backupSetup(t)
	t.Setenv("ISOTOPE_RESTIC", "/nonexistent/restic")
	if code, _, errOut := runCLI("", "backup"); code != exitError || !strings.Contains(errOut, "not available on this edition") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
}

func TestInitShowsAndConfirmsThePassword(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	const key = "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd-eeeeeeee-ffffffff-gggggggg-hhhhhhhh"
	oldKey, oldNow := newBackupKey, backupNow
	newBackupKey = func() (string, error) { return key, nil }
	backupNow = func() time.Time { return time.Unix(0, 2) } // asks for group 3
	t.Cleanup(func() { newBackupKey, backupNow = oldKey, oldNow })
	target := filepath.Join(t.TempDir(), "isotope")

	code, out, _ := runCLI("aaaaaaaa\nbbbbbbbb\nhhhhhhhh\n", "backup", "init", target)
	if code != exitError || !strings.Contains(out, "aaaaaaaa-bbbbbbbb") {
		t.Fatalf("wrong groups: exit %d, out %q", code, out)
	}
	if exists(p.password()) || exists(p.config()) {
		t.Fatal("a password was stored without confirmation")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran without a confirmed password: %s", b)
	}

	code, out, _ = runCLI("CCCCCCCC\n", "backup", "init", target)
	if code != exitOK || !strings.Contains(out, "Type group 3") {
		t.Fatalf("right group: exit %d, out %q", code, out)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != key+"\n" {
		t.Fatalf("stored password %q", b)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), target+" | init") {
		t.Fatalf("restic: %s", b)
	}
}

func TestInitWithAPasswordFromStdin(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	target := filepath.Join(t.TempDir(), "isotope")
	code, out, errOut := runCLI("my secret\n", "backup", "init", target, "--password-stdin")
	if code != exitOK || !strings.Contains(out, "now backs up to "+target) {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != "my secret\n" {
		t.Fatalf("stored password %q", b)
	}
	if info, _ := os.Stat(p.password()); info.Mode().Perm() != 0o600 {
		t.Fatalf("password mode %v", info.Mode())
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), target+" | init") {
		t.Fatalf("restic: %s", b)
	}
	if c, _ := loadBackupConfig(p); c.Target != target {
		t.Fatalf("config %+v", c)
	}
}

func TestInitRefusesAMissingDisk(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	gone := filepath.Join(t.TempDir(), "media", "MyDisk", "isotope")
	if code, _, errOut := runCLI("pw\n", "backup", "init", gone, "--password-stdin"); code != exitError || !strings.Contains(errOut, "plugged in") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if exists(filepath.Dir(gone)) || exists(p.password()) {
		t.Fatal("init created the mount point or stored a password")
	}
}

func TestBackupWithUnreadableFilesIsKept(t *testing.T) {
	p := backupSetup(t)
	bin := filepath.Join(t.TempDir(), "restic")
	// restic exits 3 when it skipped files it could not read; the snapshot
	// of the rest is written.
	write(t, bin, "#!/bin/sh\n[ \"$1\" = backup ] && exit 3\nexit 0\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	saveBackupConfig(p, backupConfig{Target: "sftp:nas:/backup", Schedule: "daily"})
	code, _, errOut := runCLI("", "backup")
	if code != exitOK || !strings.Contains(errOut, "backed up, but some files could not be read") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if c, _ := loadBackupConfig(p); c.Last == 0 || !strings.HasPrefix(c.Result, "partial: ") {
		t.Fatalf("config %+v", c)
	}
}

func TestBackupTargetInsideHomeIsExcluded(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	target := p.homeState() + "/anna/backup"
	os.MkdirAll(target, 0o700)
	write(t, target+"/config", "restic")
	saveBackupConfig(p, backupConfig{Target: target})
	if code, _, errOut := runCLI("", "backup"); code != exitOK {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "--exclude "+target+" ") {
		t.Fatalf("the target is not excluded: %s", b)
	}
}

func TestTargets(t *testing.T) {
	for in, want := range map[string]string{
		"/run/media/anna/Disk/isotope/": "/run/media/anna/Disk/isotope",
		"local:/mnt/backup":             "/mnt/backup",
		"sftp:nas:/srv/backup":          "sftp:nas:/srv/backup",
		"s3:s3.amazonaws.com/bucket":    "s3:s3.amazonaws.com/bucket",
		"Backup":                        "",
		"./usb":                         "",
		"nas:/srv/backup":               "",
	} {
		got, err := normalizeTarget(in)
		if (want == "") != (err != nil) || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestInitRefusesATargetInMemory(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	media := t.TempDir()
	onTmpfs = func(dir string) bool { return dir == media }
	if code, _, errOut := runCLI("pw\n", "backup", "init", media+"/Disk", "--password-stdin"); code != exitError || !strings.Contains(errOut, "not on a disk") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if exists(media+"/Disk") || exists(p.password()) {
		t.Fatal("init wrote to the tmpfs")
	}
}

func TestOnlyAppsStillMapsTheUsers(t *testing.T) {
	backupSetup(t)
	dir := t.TempDir()
	write(t, dir+"/users", "anna 1000\nzoe 1001\n")
	write(t, dir+"/flatpak-user-anna", "org.gnome.Maps\tflathub\n")
	write(t, dir+"/flatpak-user-zoe", "org.gnome.Chess\tflathub\n")
	write(t, dir+"/flatpak-system", "org.mozilla.firefox\tflathub\n")
	r := restorer{users: userMap{"anna": "ben"}, parts: map[string]bool{"apps": true}}
	pl, err := r.readPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range pl.apps {
		got[a.id] = a.user
	}
	want := map[string]string{"org.gnome.Maps": "ben", "org.mozilla.firefox": ""}
	if len(got) != len(want) || got["org.gnome.Maps"] != "ben" || got["org.mozilla.firefox"] != "" {
		t.Fatalf("apps %v, want %v (zoe has no local user)", got, want)
	}
	if len(pl.homes) != 0 {
		t.Fatalf("--only apps restores homes: %v", pl.homes)
	}
}

func TestMergeMoveDoesNotFollowLinks(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(src+"/Documents", 0o755)
	write(t, src+"/Documents/a.txt", "backup")
	write(t, src+"/notes", "backup")
	write(t, outside+"/precious", "outside")
	// The user made their Documents a link out of the home, and notes a
	// link to a file outside it.
	os.Symlink(outside, dst+"/Documents")
	os.Symlink(outside+"/precious", dst+"/notes")
	if err := mergeMove(src, dst); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("the restore wrote outside the home: %v", entries)
	}
	if b, _ := os.ReadFile(outside + "/precious"); string(b) != "outside" {
		t.Fatalf("the file outside changed: %q", b)
	}
	if fi, _ := os.Lstat(dst + "/Documents"); fi == nil || !fi.IsDir() {
		t.Fatal("Documents is not the restored directory")
	}
	if b, _ := os.ReadFile(dst + "/Documents/a.txt"); string(b) != "backup" {
		t.Fatalf("a.txt: %q", b)
	}
	// The replaced links are now in src, removed with the stage.
	if fi, _ := os.Lstat(src + "/Documents"); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the old link was not swapped into the stage")
	}
}

func TestMergeMove(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(src+"/Documents/sub", 0o755)
	write(t, src+"/Documents/a.txt", "backup")
	write(t, src+"/Documents/sub/b.txt", "backup")
	write(t, src+"/.bashrc", "backup")
	os.MkdirAll(dst+"/Documents", 0o755)
	write(t, dst+"/Documents/a.txt", "new install")
	write(t, dst+"/Documents/keep.txt", "new install")
	write(t, dst+"/.bashrc", "new install")
	if err := mergeMove(src, dst); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/Documents/a.txt": "backup", "/Documents/sub/b.txt": "backup",
		"/Documents/keep.txt": "new install", "/.bashrc": "backup",
	} {
		if b, _ := os.ReadFile(dst + path); string(b) != want {
			t.Errorf("%s: %q, want %q", path, b, want)
		}
	}
}

func needRealRestic(t *testing.T) {
	t.Helper()
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic is not on PATH")
	}
	t.Setenv("ISOTOPE_RESTIC", bin)
}

// TestRestoreFilesWithRestic runs the real restic: back up, then restore one
// user's files into another user's home with --only homes.
func TestRestoreFilesWithRestic(t *testing.T) {
	needRealRestic(t)
	p := backupSetup(t)
	target := filepath.Join(t.TempDir(), "isotope")
	if code, _, errOut := runCLI("pw-123\n", "backup", "init", target, "--password-stdin"); code != exitOK {
		t.Fatalf("init: %d %s", code, errOut)
	}
	if code, _, errOut := runCLI("", "backup"); code != exitOK {
		t.Fatalf("backup: %d %s", code, errOut)
	}
	if code, out, errOut := runCLI("", "backup", "list"); code != exitOK || !strings.Contains(out, "1 snapshots") {
		t.Fatalf("list: %d %q %s", code, out, errOut)
	}

	// A new machine: the same user under another name, and no backup
	// configuration.
	home := p.homeState()
	os.RemoveAll(home)
	os.RemoveAll(p.State)
	os.MkdirAll(home+"/ben/Documents", 0o755)
	write(t, home+"/ben/Documents/thesis.txt", "empty")
	write(t, home+"/ben/notes.txt", "kept")

	code, out, errOut := runCLI("pw-123\ny\n", "restore", "--from", target, "--only", "homes", "--user", "anna=ben")
	if code != exitOK {
		t.Fatalf("restore: %d %q %s", code, out, errOut)
	}
	for _, w := range []string{"Isotope v1.2.0 (sway)", "the files of anna, into the home of ben", "Restored."} {
		if !strings.Contains(out, w) {
			t.Errorf("restore output lacks %q: %s", w, out)
		}
	}
	for path, want := range map[string]string{
		home + "/ben/Documents/thesis.txt": "chapter one\n",
		home + "/ben/notes.txt":            "kept",
	} {
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Errorf("%s: %q, want %q", path, b, want)
		}
	}
	if exists(home + "/ben/.cache/big/blob") {
		t.Error("the cache was backed up")
	}
	if exists(p.stage()) {
		t.Error("the staging directory is left behind")
	}
	if exists(p.password()) {
		t.Error("an --only restore kept the --from password")
	}
}

// TestExportAndWholeMachineRestoreWithRestic exports the machine to one
// file, then restores all of it onto a fresh install.
func TestExportAndWholeMachineRestoreWithRestic(t *testing.T) {
	needRealRestic(t)
	p := backupSetup(t)
	rebooted := false
	old := rebootCommand
	rebootCommand = func() *exec.Cmd { rebooted = true; return exec.Command("true") }
	t.Cleanup(func() { rebootCommand = old })

	// One installed extension, which comes back through isotope ext.
	extDir := os.Getenv("ISOTOPE_UPDATE_EXT_DIR")
	os.MkdirAll(extDir, 0o755)
	os.MkdirAll(p.ExtDir, 0o755)
	write(t, extDir+"/tailscale.raw", "image")
	write(t, p.ExtDir+"/tailscale.raw", "tailscale\n")

	file := filepath.Join(t.TempDir(), "laptop.isotope")
	code, out, errOut := runCLI("pw-123\n", "backup", "--export", file, "--password-stdin")
	if code != exitOK || !strings.Contains(out, "Wrote "+file) {
		t.Fatalf("export: %d %q %s", code, out, errOut)
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Fatalf("the export left files behind: %v", entries)
	}
	if code, _, _ := runCLI("pw\n", "backup", "--export", file, "--password-stdin"); code != exitError {
		t.Fatal("export over an existing file")
	}

	// A fresh install: a new /oem and an empty home with one new file.
	os.RemoveAll(p.Persist)
	os.RemoveAll(p.OEM)
	os.MkdirAll(p.homeState()+"/anna", 0o755)
	write(t, p.homeState()+"/anna/new.txt", "made after the install")
	os.MkdirAll(p.OEM, 0o755)
	write(t, p.OEM+"/90_custom.yaml", "users: [new]\n")
	write(t, p.OEM+"/99_isotope-user.yaml", "users: [new]\n")
	write(t, p.OEM+"/notes.txt", "not a cloud-config")

	code, out, errOut = runCLI("pw-123\ny\n", "restore", "--from", file, "--yes")
	if code != exitOK {
		t.Fatalf("restore: %d %q %s", code, out, errOut)
	}
	for path, want := range map[string]string{
		p.homeState() + "/anna/Documents/thesis.txt":    "chapter one\n",
		p.homeState() + "/anna/new.txt":                 "made after the install",
		p.bindDir("/etc/ssh") + "/ssh_host_ed25519_key": "host key\n",
		p.OEM + "/90_custom.yaml":                       "users: [anna]\n",
	} {
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Errorf("%s: %q, want %q", path, b, want)
		}
	}
	if exists(p.homeState() + "/anna/.cache/big/blob") {
		t.Error("the cache was backed up")
	}
	if exists(p.stage()) {
		t.Error("the staging directory is left behind")
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Errorf("the unpacked export is left behind: %v", entries)
	}
	if !rebooted {
		t.Error("no restart after a whole-machine restore")
	}
	// /oem has exactly the backup's cloud-configs; the install's own is aside.
	if exists(p.OEM + "/99_isotope-user.yaml") {
		t.Error("a cloud-config of the new install still runs at boot")
	}
	if aside, _ := filepath.Glob(p.State + "/oem-before-restore/*/99_isotope-user.yaml"); len(aside) != 1 {
		t.Error("the new install's cloud-config was not kept aside")
	}
	if !exists(p.OEM + "/notes.txt") {
		t.Error("a file of /oem that is not a cloud-config was removed")
	}
	if b, _ := os.ReadFile(p.pending() + "/extensions"); !strings.HasPrefix(string(b), "tailscale.raw tailscale ") {
		t.Errorf("the extensions queued for isotope restore --finish: %q", b)
	}
	// The export had no target, so the restored machine has none, and no
	// password is kept.
	if exists(p.password()) {
		t.Error("a password without a target")
	}
}

func TestWholeRestoreNeedsTheSameEncryption(t *testing.T) {
	needRealRestic(t)
	p := backupSetup(t)
	file := filepath.Join(t.TempDir(), "laptop.isotope")
	if code, _, errOut := runCLI("pw\n", "backup", "--export", file, "--password-stdin"); code != exitOK {
		t.Fatalf("export: %d %s", code, errOut)
	}
	write(t, p.Encryption, "mode=passphrase\n")
	write(t, p.OEM+"/90_custom.yaml", "kcrypt: {method: passphrase}\n")
	code, _, errOut := runCLI("pw\n", "restore", "--from", file, "--yes")
	if code != exitError || !strings.Contains(errOut, "--only homes,apps,extensions") {
		t.Fatalf("exit %d, err %q", code, errOut)
	}
	if b, _ := os.ReadFile(p.OEM + "/90_custom.yaml"); !strings.Contains(string(b), "kcrypt") {
		t.Fatal("the restore replaced /oem")
	}
}

func TestUnpackRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]func(*tar.Writer){
		"climb": func(w *tar.Writer) {
			w.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
			w.Write([]byte("x"))
		},
		"absolute": func(w *tar.Writer) {
			w.WriteHeader(&tar.Header{Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
			w.Write([]byte("x"))
		},
		"link": func(w *tar.Writer) {
			w.WriteHeader(&tar.Header{Name: "data", Typeflag: tar.TypeSymlink, Linkname: "/etc"})
		},
	} {
		f := filepath.Join(t.TempDir(), name+".isotope")
		out, _ := os.Create(f)
		w := tar.NewWriter(out)
		body(w)
		w.Close()
		out.Close()
		into := filepath.Join(dir, name)
		os.Mkdir(into, 0o700)
		if err := unpackFile(f, into); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	if exists(filepath.Join(dir, "evil")) {
		t.Fatal("an entry climbed out")
	}
}

func TestOnPersistMapsBindMounts(t *testing.T) {
	p := backupPaths{Persist: "/usr/local"}
	dir := t.TempDir()
	old := mountinfoPath
	mountinfoPath = filepath.Join(dir, "mountinfo")
	t.Cleanup(func() { mountinfoPath = old })
	write(t, mountinfoPath, `22 1 0:20 / / rw - overlay overlay rw
30 22 253:1 / /usr/local rw - ext4 /dev/mapper/persistent rw
31 22 253:1 /.state/home.bind /home rw - ext4 /dev/mapper/persistent rw
32 22 253:1 /.state/root.bind /root rw - ext4 /dev/mapper/persistent rw
33 22 8:17 / /run/media/anna/My\040Disk rw - vfat /dev/sdb1 rw
`)
	for in, want := range map[string]string{
		"/root/backups":               "/usr/local/.state/root.bind/backups",
		"/home/anna/b":                "/usr/local/.state/home.bind/anna/b",
		"/usr/local/x":                "/usr/local/x",
		"/run/media/anna/My Disk/iso": "/run/media/anna/My Disk/iso",
		"/srv/elsewhere":              "/srv/elsewhere",
	} {
		if got := p.onPersist(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestWholeRestoreKeepsThisMachinesBackupSettings(t *testing.T) {
	p := backupSetup(t)
	saveBackupConfig(p, backupConfig{Target: "/run/media/anna/Old/isotope", Schedule: "weekly"})
	r := restorer{p: p, pass: filepath.Join(t.TempDir(), "typed")}
	write(t, r.pass, "typed-pw\n")

	// This machine already had a target: its config and password win.
	mine := backupConfig{Target: "sftp:nas:/new", Schedule: "daily"}
	if err := r.keepBackupSettings(mine, "my-pw\n"); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadBackupConfig(p); c.Target != "sftp:nas:/new" {
		t.Errorf("config %+v", c)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != "my-pw\n" {
		t.Errorf("password %q", b)
	}

	// A fresh machine: the restored config, with the typed password.
	saveBackupConfig(p, backupConfig{Target: "/run/media/anna/Old/isotope"})
	os.Remove(p.password())
	if err := r.keepBackupSettings(backupConfig{}, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != "typed-pw\n" {
		t.Errorf("password %q", b)
	}
}

func TestRestoreFinishInstallsWhatWasQueued(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	os.MkdirAll(p.pending(), 0o700)
	write(t, p.pending()+"/users", "anna 1000\n")
	write(t, p.pending()+"/flatpak-user-anna", "org.gnome.Maps\tflathub\n")
	write(t, p.pending()+"/flatpak-system", "org.mozilla.firefox\tflathub\n")
	var ran []string
	old := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "org.gnome.Maps") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { sysCommand = old })
	code, _, _ := runCLI("", "restore", "--finish")
	if code != exitError {
		t.Fatalf("exit %d with a failed app", code)
	}
	joined := strings.Join(ran, "\n")
	for _, w := range []string{"flatpak install --system --noninteractive -y flathub org.mozilla.firefox",
		"runuser -u anna -- flatpak install --user --noninteractive -y flathub org.gnome.Maps"} {
		if !strings.Contains(joined, w) {
			t.Errorf("did not run %q in:\n%s", w, joined)
		}
	}
	if exists(p.pending()) {
		t.Error("the queue is still there: it would run at every boot")
	}
	if _, out, _ := runCLI("", "backup", "status"); !strings.Contains(out, "could not install") || !strings.Contains(out, "org.gnome.Maps") {
		t.Errorf("status does not say what failed: %q", out)
	}
	if code, _, _ := runCLI("", "restore", "--finish"); code != exitOK {
		t.Error("a second --finish with nothing queued fails")
	}
}
