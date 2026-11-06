package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// badAdvanced is the last update.conf problem logged (an invalid advanced=
// value, or a read error), so it is logged once, not at every Status call.
var (
	badAdvancedMu sync.Mutex
	badAdvanced   string
)

// AdvancedEnabled reports whether the advanced image picker is on. A missing
// file or key keeps the default, on; a file that exists but cannot be read
// turns it off, and is logged. The value is read without regard to
// case: true, yes, 1 and on turn it on; false, no, 0 and off turn it off.
// Any other value turns it off and is logged: the admin meant to set
// something, and off is the reading that installs nothing unexpected. The
// last advanced= line wins.
func AdvancedEnabled() bool {
	b, err := os.ReadFile(confFile())
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		// The file is there but unreadable: the admin set something, and
		// off is the reading that installs nothing unexpected.
		logBadAdvanced("unreadable: "+err.Error(), func() {
			fmt.Fprintf(os.Stderr, "hadron-update: cannot read %s: %v; advanced mode is off\n", confFile(), err)
		})
		return false
	}
	enabled := true
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != "advanced" {
			continue
		}
		switch v = strings.TrimSpace(v); strings.ToLower(v) {
		case "true", "yes", "1", "on":
			enabled = true
		case "false", "no", "0", "off":
			enabled = false
		default:
			enabled = false
			logBadAdvanced(v, func() {
				fmt.Fprintf(os.Stderr, "hadron-update: %s: advanced=%q is not true or false; advanced mode is off\n", confFile(), v)
			})
		}
	}
	return enabled
}

// logBadAdvanced calls log unless key is the problem logged last.
func logBadAdvanced(key string, log func()) {
	badAdvancedMu.Lock()
	defer badAdvancedMu.Unlock()
	if key != badAdvanced {
		badAdvanced = key
		log()
	}
}
