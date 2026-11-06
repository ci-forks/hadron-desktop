package main

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Keyboard layout and timezone: the lists the wizard offers, the checks on
// what reaches the cloud-config, and the console keymap that matches an XKB
// layout.

const (
	defaultLayout   = "us"
	defaultTimezone = "UTC"

	// xkbRulesList is xkeyboard-config's human-readable rules list. Every
	// desktop variant ships xkeyboard-config, so the live installer has it.
	xkbRulesList = "/usr/share/X11/xkb/rules/base.lst"
	zoneinfoDir  = "/usr/share/zoneinfo"
	keymapsDir   = "/usr/share/keymaps"
)

// Keyboard is one entry of the keyboard list: an XKB layout and an optional
// variant of it.
type Keyboard struct {
	Layout      string
	Variant     string
	Description string
}

// Code is the setxkbmap-style name, "de" or "de(nodeadkeys)".
func (k Keyboard) Code() string {
	if k.Variant == "" {
		return k.Layout
	}
	return k.Layout + "(" + k.Variant + ")"
}

// curatedKeyboards is the list offered when the image has no xkeyboard-config
// rules list to read: the most common layouts, a few variants people look for.
var curatedKeyboards = []Keyboard{
	{"us", "", "English (US)"},
	{"us", "intl", "English (US, intl., with dead keys)"},
	{"us", "dvorak", "English (Dvorak)"},
	{"us", "colemak", "English (Colemak)"},
	{"gb", "", "English (UK)"},
	{"ie", "", "Irish"},
	{"de", "", "German"},
	{"de", "nodeadkeys", "German (no dead keys)"},
	{"ch", "", "German (Switzerland)"},
	{"ch", "fr", "French (Switzerland)"},
	{"at", "", "German (Austria)"},
	{"fr", "", "French"},
	{"be", "", "Belgian"},
	{"it", "", "Italian"},
	{"es", "", "Spanish"},
	{"latam", "", "Spanish (Latin American)"},
	{"pt", "", "Portuguese"},
	{"br", "", "Portuguese (Brazil)"},
	{"nl", "", "Dutch"},
	{"dk", "", "Danish"},
	{"no", "", "Norwegian"},
	{"se", "", "Swedish"},
	{"fi", "", "Finnish"},
	{"is", "", "Icelandic"},
	{"pl", "", "Polish"},
	{"cz", "", "Czech"},
	{"sk", "", "Slovak"},
	{"hu", "", "Hungarian"},
	{"ro", "", "Romanian"},
	{"si", "", "Slovenian"},
	{"hr", "", "Croatian"},
	{"gr", "", "Greek"},
	{"tr", "", "Turkish"},
	{"ru", "", "Russian"},
	{"ua", "", "Ukrainian"},
	{"il", "", "Hebrew"},
	{"jp", "", "Japanese"},
	{"kr", "", "Korean"},
	{"ca", "", "French (Canada)"},
}

// loadKeyboards reads xkeyboard-config's base.lst and falls back to the
// curated list when it is missing or holds no layouts.
func loadKeyboards(path string) []Keyboard {
	f, err := os.Open(path)
	if err != nil {
		return curatedKeyboards
	}
	defer f.Close()
	if ks := parseRulesList(f); len(ks) > 0 {
		return ks
	}
	return curatedKeyboards
}

// parseRulesList reads the "! layout" and "! variant" sections of a
// xkeyboard-config .lst file. A layout line is "  de   German"; a variant
// line is "  nodeadkeys   de: German (no dead keys)". Each variant is placed
// right after its layout, and US English goes first because it is the
// default. Entries whose names would not pass validateKeyboard are dropped.
func parseRulesList(r io.Reader) []Keyboard {
	var layouts []Keyboard
	variants := map[string][]Keyboard{}
	section := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "!") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		desc := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), name))
		switch section {
		case "layout":
			if validateKeyboard(name, "") == nil {
				layouts = append(layouts, Keyboard{Layout: name, Description: desc})
			}
		case "variant":
			layout, d, ok := strings.Cut(desc, ":")
			if !ok || validateKeyboard(layout, name) != nil {
				continue
			}
			variants[layout] = append(variants[layout], Keyboard{Layout: layout, Variant: name, Description: strings.TrimSpace(d)})
		}
	}
	sort.SliceStable(layouts, func(i, j int) bool {
		return layouts[i].Layout == defaultLayout && layouts[j].Layout != defaultLayout
	})
	out := make([]Keyboard, 0, len(layouts))
	for _, l := range layouts {
		out = append(out, l)
		out = append(out, variants[l.Layout]...)
	}
	return out
}

// xkbName is what XKB layout and variant names look like in xkeyboard-config:
// lowercase layouts ("us", "latam", "brai"); variants also with digits,
// underscores and dashes ("mac_nodeadkeys", "dvorak-l"). Both are written
// into the cloud-config, into an xorg.conf.d file and into a shell-sourced
// file, so nothing outside this set may reach them.
var (
	xkbLayout  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	xkbVariant = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

func validateKeyboard(layout, variant string) error {
	if !xkbLayout.MatchString(layout) {
		return errors.New("keyboard layout must be an XKB layout name such as us or de")
	}
	if variant != "" && !xkbVariant.MatchString(variant) {
		return errors.New("keyboard variant must be an XKB variant name such as nodeadkeys")
	}
	return nil
}

// tzName is an IANA zone name: "UTC", "Europe/Rome", "America/Argentina/Salta",
// "Etc/GMT+1". No dots, so no "..", and no leading slash: the value becomes
// part of a path under /usr/share/zoneinfo.
var tzName = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,2}$`)

func validateTimezone(tz string) error {
	if !tzName.MatchString(tz) {
		return errors.New("timezone must be an IANA name such as UTC or Europe/Rome")
	}
	return nil
}

// curatedTimezones is the fallback list when the image has no tzdata. The
// installed system could not use any other zone then anyway.
var curatedTimezones = []string{
	"UTC",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Sao_Paulo", "America/Mexico_City", "America/Toronto",
	"Europe/London", "Europe/Dublin", "Europe/Lisbon", "Europe/Madrid", "Europe/Paris",
	"Europe/Berlin", "Europe/Rome", "Europe/Amsterdam", "Europe/Zurich", "Europe/Vienna",
	"Europe/Stockholm", "Europe/Warsaw", "Europe/Athens", "Europe/Kyiv", "Europe/Moscow",
	"Africa/Cairo", "Africa/Johannesburg", "Asia/Dubai", "Asia/Kolkata", "Asia/Shanghai",
	"Asia/Tokyo", "Asia/Seoul", "Asia/Singapore", "Australia/Sydney", "Pacific/Auckland",
}

// loadTimezones lists the zones under dir. It prefers tzdata's own index
// (zone1970.tab, then zone.tab), which names only canonical zones, over a
// directory walk, which also finds every backward-compatible alias. UTC is
// always first. The curated list is the fallback when nothing is found.
func loadTimezones(dir string) []string {
	seen := map[string]bool{}
	var zones []string
	add := func(z string) {
		if !seen[z] && validateTimezone(z) == nil {
			seen[z] = true
			zones = append(zones, z)
		}
	}
	for _, tab := range []string{"zone1970.tab", "zone.tab"} {
		f, err := os.Open(filepath.Join(dir, tab))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "#") {
				continue
			}
			if fields := strings.Split(line, "\t"); len(fields) >= 3 {
				add(fields[2])
			}
		}
		f.Close()
		if len(zones) > 0 {
			break
		}
	}
	if len(zones) == 0 {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			if d.IsDir() {
				if rel == "posix" || rel == "right" {
					return filepath.SkipDir
				}
				return nil
			}
			if isTZif(p) {
				add(filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if len(zones) == 0 {
		return curatedTimezones
	}
	sort.Strings(zones)
	out := []string{defaultTimezone}
	for _, z := range zones {
		if z != defaultTimezone {
			out = append(out, z)
		}
	}
	return out
}

// zoneExists checks that tz names a zone file under dir. A system with no
// zoneinfo directory at all cannot check, and accepts any valid name.
func zoneExists(dir, tz string) error {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	if !isTZif(filepath.Join(dir, tz)) {
		return errors.New("no such timezone in " + dir + ": " + tz)
	}
	return nil
}

func isTZif(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	_, err = io.ReadFull(f, magic)
	return err == nil && string(magic) == "TZif"
}

// detectTimezone returns the live system's zone: the target of /etc/localtime
// below a zoneinfo directory, else TZ, else UTC. The live ISO usually has no
// /etc/localtime, so UTC is the common answer.
func detectTimezone(localtime string) string {
	if target, err := os.Readlink(localtime); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok && validateTimezone(zone) == nil {
			return zone
		}
	}
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); validateTimezone(tz) == nil {
		return tz
	}
	return defaultTimezone
}

// consoleKeymapAlias maps XKB layouts whose kbd keymap has another name.
// The same table, in spirit, as systemd-localed's kbd-model-map, which
// Hadron does not ship (it has no localed).
var consoleKeymapAlias = map[string][]string{
	"gb":            {"uk"},
	"ch":            {"sg", "de_CH-latin1"},
	"ch:fr":         {"fr_CH", "fr_CH-latin1"},
	"br":            {"br-abnt2"},
	"se":            {"sv-latin1"},
	"latam":         {"la-latin1"},
	"ee":            {"et"},
	"si":            {"slovene"},
	"rs":            {"sr-latin"},
	"tr":            {"trq"},
	"ca":            {"cf"},
	"jp":            {"jp106"},
	"ir":            {"fa"},
	"is":            {"is-latin1"},
	"at":            {"de"},
	"sk":            {"sk-qwertz"},
	"hr":            {"croat"},
	"kr":            {"us"},
	"fr:bepo":       {"fr-bepo"},
	"us:dvorak":     {"dvorak"},
	"de:nodeadkeys": {"de-latin1-nodeadkeys"},
}

// consoleKeymap finds the kbd keymap under dir that matches an XKB layout and
// variant, or "" when there is none (the console then keeps the kernel's US
// map). It tries the alias table, then "<layout>-<variant>" and
// "<layout>-latin1-<variant>", then the layout alone and "<layout>-latin1".
func consoleKeymap(dir, layout, variant string) string {
	have := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if name, ok := strings.CutSuffix(d.Name(), ".map.gz"); ok {
				have[name] = true
			} else if name, ok := strings.CutSuffix(d.Name(), ".map"); ok {
				have[name] = true
			}
		}
		return nil
	})
	var candidates []string
	if variant != "" {
		candidates = append(candidates, consoleKeymapAlias[layout+":"+variant]...)
		candidates = append(candidates, layout+"-"+variant, layout+"-latin1-"+variant)
	}
	candidates = append(candidates, consoleKeymapAlias[layout]...)
	candidates = append(candidates, layout, layout+"-latin1")
	for _, c := range candidates {
		if have[c] && consoleKeymapName.MatchString(c) {
			return c
		}
	}
	return ""
}

// consoleKeymapName is what a kbd keymap name looks like ("de-latin1",
// "lt.baltic", "de_CH-latin1"). It is written to /etc/vconsole.conf.
var consoleKeymapName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// loadConsoleKeymap switches the live console to the chosen keymap, so the
// password typed in the next steps is the one the installed system, which
// uses the same keymap, will expect. Best effort: over a serial line or SSH
// there is no console to switch, and that is fine. An empty name loads "us",
// the kernel's own map, so going back from "de" to a layout with no console
// keymap does not leave the console in German.
//
// The wizard calls it through its applyKeymap field, which newWizard leaves
// a no-op, so tests do not change the keymap of the machine they run on.
func loadConsoleKeymap(name string) {
	_ = exec.Command("loadkeys", orDefault(name, "us")).Run()
}
