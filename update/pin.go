package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func pinFile() string {
	if p := os.Getenv("HADRON_UPDATE_PIN_FILE"); p != "" {
		return p
	}
	return "/var/lib/hadron-update/pin"
}

func confFile() string {
	if p := os.Getenv("HADRON_UPDATE_CONF"); p != "" {
		return p
	}
	return "/etc/hadron-desktop/update.conf"
}

// LoadPin returns the image reference the user deliberately installed, or ""
// when nothing is pinned. A pin tells the tray to stop offering "updates"
// that would replace an image the user chose on purpose.
func LoadPin() (string, error) {
	b, err := os.ReadFile(pinFile())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SavePin records ref. The write goes through a temp file and a rename so a
// crash never leaves a half-written pin that LoadPin would trust.
func SavePin(ref string) error {
	path := pinFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.WriteString(ref + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ClearPin removes the pin; there being none is not an error.
func ClearPin() error {
	if err := os.Remove(pinFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// AdvancedEnabled reports whether the advanced image picker is on. It is on
// unless update.conf says advanced=false; a missing file or a value that is
// not a boolean keeps the default, so a typo never locks a user out.
func AdvancedEnabled() bool {
	b, err := os.ReadFile(confFile())
	if err != nil {
		return true
	}
	enabled := true
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != "advanced" {
			continue
		}
		switch strings.TrimSpace(v) {
		case "false":
			enabled = false
		case "true":
			enabled = true
		}
	}
	return enabled
}
