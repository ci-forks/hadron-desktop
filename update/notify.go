package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// busctl is the D-Bus client. It ships in the Hadron base, and as a
// `--user` client it honours DBUS_SESSION_BUS_ADDRESS, which is how it reaches
// the private bus start-desktop opens with dbus-run-session (mako and dunst
// both own org.freedesktop.Notifications there). HADRON_UPDATE_BUSCTL replaces
// it, so a test can record the call instead of needing a bus.
func busctl() string {
	if p := os.Getenv("HADRON_UPDATE_BUSCTL"); p != "" {
		return p
	}
	return "/usr/bin/busctl"
}

// notifyArgs is the org.freedesktop.Notifications.Notify call: app name,
// replaces_id, icon, summary, body, no actions, no hints, default timeout.
// The `--` ends busctl's own option parsing: the timeout argument is -1, and
// without it busctl rejects that as an unknown option and nothing is shown.
func notifyArgs(summary, body string) []string {
	return []string{
		"--user", "--", "call",
		"org.freedesktop.Notifications", "/org/freedesktop/Notifications",
		"org.freedesktop.Notifications", "Notify", "susssasa{sv}i",
		"Hadron Desktop", "0", "system-software-update", summary, body,
		"0", "0", "-1",
	}
}

// Notify shows a desktop notification on the session bus.
func Notify(summary, body string) error {
	out, err := exec.Command(busctl(), notifyArgs(summary, body)...).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return nil
}

// stateDir holds the last version the user was told about, so a session that
// restarts every few hours does not repeat the same notification.
func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hadron-update")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "hadron-update")
}

func alreadyNotified(tag string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir(), "notified"))
	return err == nil && strings.TrimSpace(string(b)) == tag
}

func markNotified(tag string) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir(), "notified"), []byte(tag+"\n"), 0o644)
}
