package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinRoundTrip(t *testing.T) {
	t.Setenv("HADRON_UPDATE_PIN_FILE", filepath.Join(t.TempDir(), "sub", "pin"))
	if got, err := LoadPin(); err != nil || got != "" {
		t.Fatalf("empty LoadPin = %q, %v", got, err)
	}
	const ref = "ghcr.io/kairos-community/hadron-desktop@sha256:abc"
	if err := SavePin(ref); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadPin(); err != nil || got != ref {
		t.Fatalf("LoadPin = %q, %v; want %q", got, err, ref)
	}
	if err := ClearPin(); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadPin(); got != "" {
		t.Fatalf("pin after clear = %q", got)
	}
}

func TestClearPinMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("HADRON_UPDATE_PIN_FILE", filepath.Join(t.TempDir(), "pin"))
	if err := ClearPin(); err != nil {
		t.Fatal(err)
	}
}

func TestSavePinLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HADRON_UPDATE_PIN_FILE", filepath.Join(dir, "pin"))
	if err := SavePin("x"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want just the pin", len(entries))
	}
}

func TestAdvancedEnabled(t *testing.T) {
	for name, c := range map[string]struct {
		content *string
		want    bool
	}{
		"no file":    {nil, true},
		"false":      {ptr("advanced=false\n"), false},
		"true":       {ptr("advanced=true\n"), true},
		"other keys": {ptr("# c\nfoo=bar\nadvanced = false\n"), false},
		"empty":      {ptr(""), true},
		"no key":     {ptr("foo=bar\n"), true},
		"False":      {ptr("advanced=False\n"), false},
		"no":         {ptr("advanced=no\n"), false},
		"0":          {ptr("advanced=0\n"), false},
		"OFF":        {ptr("advanced=OFF\n"), false},
		"Yes":        {ptr("advanced=Yes\n"), true},
		"1":          {ptr("advanced=1\n"), true},
		"on":         {ptr("advanced=on\n"), true},
		"TRUE":       {ptr("advanced=TRUE\n"), true},
		// A value that is present but not a boolean turns it off: the
		// admin meant to set something, and off is the safe reading.
		"unreadable": {ptr("advanced=maybe\n"), false},
		"blank":      {ptr("advanced=\n"), false},
	} {
		t.Run(name, func(t *testing.T) {
			conf := filepath.Join(t.TempDir(), "update.conf")
			t.Setenv("HADRON_UPDATE_CONF", conf)
			if c.content != nil {
				if err := os.WriteFile(conf, []byte(*c.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := AdvancedEnabled(); got != c.want {
				t.Fatalf("AdvancedEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
