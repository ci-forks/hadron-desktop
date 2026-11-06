package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rulesFixture = `! model
  pc105           Generic 105-key PC

! layout
  al              Albanian
  de              German
  us              English (US)
  Bad;Name        Not an XKB name

! variant
  plisi           al: Albanian (Plisi)
  nodeadkeys      de: German (no dead keys)
  intl            us: English (US, intl., with dead keys)
  evil$var        us: Not an XKB name

! option
  grp             Switching to another layout
`

func TestParseRulesListPutsUSFirstAndVariantsAfterTheirLayout(t *testing.T) {
	got := parseRulesList(strings.NewReader(rulesFixture))
	want := []Keyboard{
		{"us", "", "English (US)"},
		{"us", "intl", "English (US, intl., with dead keys)"},
		{"al", "", "Albanian"},
		{"al", "plisi", "Albanian (Plisi)"},
		{"de", "", "German"},
		{"de", "nodeadkeys", "German (no dead keys)"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLoadKeyboardsFallsBackToTheCuratedList(t *testing.T) {
	got := loadKeyboards(filepath.Join(t.TempDir(), "missing.lst"))
	if len(got) < 30 {
		t.Fatalf("curated list has %d entries, want about 30", len(got))
	}
	if got[0].Layout != defaultLayout || got[0].Variant != "" {
		t.Errorf("first entry = %+v, want the default %q", got[0], defaultLayout)
	}
	// The curated list is written by hand, so check it obeys the same rule
	// the renderer enforces.
	for _, k := range got {
		if err := validateKeyboard(k.Layout, k.Variant); err != nil {
			t.Errorf("curated entry %+v is invalid: %v", k, err)
		}
	}
}

func TestLoadKeyboardsReadsTheRulesList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.lst")
	if err := os.WriteFile(path, []byte(rulesFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadKeyboards(path); len(got) != 6 {
		t.Errorf("got %d entries from the rules list, want 6", len(got))
	}
}

func TestValidateKeyboard(t *testing.T) {
	for _, tc := range []struct {
		layout, variant string
		ok              bool
	}{
		{"us", "", true},
		{"de", "nodeadkeys", true},
		{"us", "dvorak-l", true},
		{"latam", "", true},
		{"", "", false},
		{"US", "", false},
		{"de\nstages:", "", false},
		{"de", "x\"\nOption", false},
		{"de", "-leading", false},
		{"de", "a b", false},
		{"../etc", "", false},
	} {
		err := validateKeyboard(tc.layout, tc.variant)
		if (err == nil) != tc.ok {
			t.Errorf("validateKeyboard(%q, %q) = %v, want ok=%v", tc.layout, tc.variant, err, tc.ok)
		}
	}
}

func TestValidateTimezone(t *testing.T) {
	for _, tc := range []struct {
		tz string
		ok bool
	}{
		{"UTC", true},
		{"Europe/Rome", true},
		{"America/Argentina/Salta", true},
		{"Etc/GMT+1", true},
		{"", false},
		{"/etc/passwd", false},
		{"../../etc/shadow", false},
		{"Europe/../../etc", false},
		{"Europe/Rome /etc/localtime; rm -rf /", false},
		{"Europe/Rome\nstages:", false},
		{"a/b/c/d", false},
	} {
		err := validateTimezone(tc.tz)
		if (err == nil) != tc.ok {
			t.Errorf("validateTimezone(%q) = %v, want ok=%v", tc.tz, err, tc.ok)
		}
	}
}

// writeZone creates a minimal TZif file; only the magic is ever read.
func writeZone(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("TZif2\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTimezonesPrefersTheZoneTable(t *testing.T) {
	dir := t.TempDir()
	tab := "# comment\nIT,SM,VA\t+4154+01229\tEurope/Rome\nUS\t+404251-0740023\tAmerica/New_York\tEastern\nXX\t+0\tbad zone\n"
	if err := os.WriteFile(filepath.Join(dir, "zone1970.tab"), []byte(tab), 0o644); err != nil {
		t.Fatal(err)
	}
	writeZone(t, dir, "Europe/Berlin") // in the directory, not in the table
	got := loadTimezones(dir)
	want := []string{"UTC", "America/New_York", "Europe/Rome"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("loadTimezones = %v, want %v", got, want)
	}
}

func TestLoadTimezonesWalksWithoutATable(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "Europe/Rome")
	writeZone(t, dir, "UTC")
	writeZone(t, dir, "posix/Europe/Rome") // duplicate tree, skipped
	if err := os.WriteFile(filepath.Join(dir, "iso3166.tab"), []byte("not a zone"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadTimezones(dir)
	want := []string{"UTC", "Europe/Rome"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("loadTimezones = %v, want %v", got, want)
	}
}

func TestLoadTimezonesFallsBackToTheCuratedList(t *testing.T) {
	got := loadTimezones(filepath.Join(t.TempDir(), "none"))
	if len(got) == 0 || got[0] != defaultTimezone {
		t.Fatalf("fallback = %v, want the curated list starting with UTC", got)
	}
	for _, z := range got {
		if err := validateTimezone(z); err != nil {
			t.Errorf("curated zone %q is invalid: %v", z, err)
		}
	}
}

func TestDetectTimezone(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "localtime")
	if err := os.Symlink("/usr/share/zoneinfo/Europe/Rome", link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "")
	if got := detectTimezone(link); got != "Europe/Rome" {
		t.Errorf("from symlink: %q, want Europe/Rome", got)
	}
	missing := filepath.Join(dir, "missing")
	t.Setenv("TZ", ":Asia/Tokyo")
	if got := detectTimezone(missing); got != "Asia/Tokyo" {
		t.Errorf("from TZ: %q, want Asia/Tokyo", got)
	}
	t.Setenv("TZ", "EST5EDT,M3.2.0,M11.1.0") // a POSIX rule, not a zone name
	if got := detectTimezone(missing); got != defaultTimezone {
		t.Errorf("with a POSIX TZ: %q, want %q", got, defaultTimezone)
	}
}

func TestZoneExists(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "Europe/Rome")
	if err := zoneExists(dir, "Europe/Rome"); err != nil {
		t.Errorf("Europe/Rome: %v", err)
	}
	if err := zoneExists(dir, "Europe/Atlantis"); err == nil {
		t.Error("a missing zone was accepted")
	}
	if err := zoneExists(filepath.Join(dir, "none"), "Europe/Atlantis"); err != nil {
		t.Errorf("without a zoneinfo dir any valid name passes, got %v", err)
	}
}

func TestConsoleKeymap(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"i386/qwerty/us.map.gz", "i386/qwertz/de.map.gz", "i386/qwertz/de-latin1-nodeadkeys.map.gz",
		"i386/qwerty/uk.map.gz", "i386/qwertz/sg.map.gz", "i386/qwertz/fr_CH.map.gz",
		"i386/azerty/fr-latin1.map.gz", "i386/include/euro.map.gz",
	} {
		writeZone(t, dir, f)
	}
	for _, tc := range []struct{ layout, variant, want string }{
		{"us", "", "us"},
		{"de", "", "de"},
		{"de", "nodeadkeys", "de-latin1-nodeadkeys"},
		{"de", "unknownvariant", "de"},
		{"gb", "", "uk"},
		{"ch", "", "sg"},
		{"ch", "fr", "fr_CH"},
		{"fr", "", "fr-latin1"},
		{"ara", "", ""},
	} {
		if got := consoleKeymap(dir, tc.layout, tc.variant); got != tc.want {
			t.Errorf("consoleKeymap(%q, %q) = %q, want %q", tc.layout, tc.variant, got, tc.want)
		}
	}
	if got := consoleKeymap(filepath.Join(dir, "none"), "de", ""); got != "" {
		t.Errorf("without keymaps: %q, want empty", got)
	}
}

func TestPickerFiltersByEveryWordAndKeepsTheSelection(t *testing.T) {
	p := newPicker([]string{"English (US)  us", "German  de", "German (no dead keys)  de(nodeadkeys)"})
	p.move(1) // German
	p.filter("GERMAN")
	if i, _ := p.selected(); i != 1 {
		t.Errorf("selection moved to %d after a filter that still matches it, want 1", i)
	}
	p.filter("german dead")
	if len(p.match) != 1 || p.match[0] != 2 {
		t.Errorf("matches = %v, want only entry 2", p.match)
	}
	p.filter("nothing like this")
	if _, ok := p.selected(); ok {
		t.Error("an empty match list must have no selection")
	}
	p.move(1) // must not panic or go out of range
	if p.cursor != 0 {
		t.Errorf("cursor = %d on an empty list, want 0", p.cursor)
	}
}
