#!/usr/bin/env python3
"""Tests for tools/gen-shortcuts.py. Run: python3 tools/test_gen_shortcuts.py"""

import importlib.util
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("gen_shortcuts", os.path.join(HERE, "gen-shortcuts.py"))
gen = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gen)


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def rows(text):
    return [tuple(l.split("\t")) for l in text.splitlines() if not l.startswith("#")]


def i3_sheet(text):
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "config")
        write(p, text)
        return rows(gen.sheet(gen.parse_i3([p])))


class I3Parse(unittest.TestCase):
    def test_variables_flags_and_merge(self):
        got = i3_sheet("""\
set $mod Mod4
set $left h
set $right l
# help-section: Windows
# help+: Move focus
bindsym $mod+$left focus left
bindsym $mod+$right focus right
bindsym $mod+Left focus left
# help: Lock
bindsym --locked $mod+Mod1+l exec lock
""")
        self.assertEqual(got, [
            ("Windows", "Super + H / L / Left", "Move focus"),
            ("Windows", "Super + Alt + L", "Lock"),
        ])

    def test_undocumented_bindings_are_left_out(self):
        self.assertEqual(i3_sheet("set $mod Mod4\nbindsym $mod+x kill\n"), [])

    def test_single_help_covers_one_binding(self):
        got = i3_sheet("set $mod Mod4\n# help: One\nbindsym $mod+a x\nbindsym $mod+b y\n")
        self.assertEqual(got, [("Basics", "Super + A", "One")])

    def test_blank_line_and_comment_end_a_run(self):
        got = i3_sheet("""\
set $mod Mod4
# help+: Run
bindsym $mod+a x

bindsym $mod+b y
# help+: Run2
bindsym $mod+c z
# another comment
bindsym $mod+d w
""")
        self.assertEqual(got, [("Basics", "Super + A", "Run"), ("Basics", "Super + C", "Run2")])

    def test_binding_in_a_mode_block_needs_its_own_help(self):
        got = i3_sheet("""\
set $mod Mod4
# help: Resize
bindsym $mod+r mode "resize"
mode "resize" {
    bindsym Return mode "default"
}
""")
        self.assertEqual(got, [("Basics", "Super + R", "Resize")])

    def test_digit_range_and_variable_prefix(self):
        text = "set $mod Mod4\nset $ws1 \"1\"\nset $ws10 \"10\"\n# help+: Go\n"
        text += "".join("bindsym $mod+%s workspace number $ws\n" % k for k in "1234567890")
        self.assertEqual(i3_sheet(text), [("Basics", "Super + 1 ... 0", "Go")])

    def test_literal_row(self):
        got = i3_sheet("# help-row: Windows | Super + drag | Move a window\n")
        self.assertEqual(got, [("Windows", "Super + drag", "Move a window")])

    def test_bad_help_row(self):
        with self.assertRaises(gen.Error):
            i3_sheet("# help-row: Windows | only two\n")

    def test_unknown_modifier(self):
        with self.assertRaises(gen.Error):
            i3_sheet("# help: X\nbindsym Hyper+a x\n")


class NiriParse(unittest.TestCase):
    def test_binds_block_only(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "config.kdl")
            write(p, """\
// help: Outside
Mod+Z { nope; }
binds {
    // help-section: System
    // help: Overview
    Mod+Shift+Slash { show-hotkey-overlay; }
    // help+: Close
    Mod+Q repeat=false { close-window; }
    Mod+Shift+Q { close-window; }
}
// help: After
Mod+Y { nope; }
""")
            got = rows(gen.sheet(gen.parse_niri(p)))
        self.assertEqual(got, [
            ("System", "Super + Shift + /", "Overview"),
            ("System", "Super + Q / Super + Shift + Q", "Close"),
        ])

    def test_section_order(self):
        text = gen.sheet([("System", "a", "x"), ("Zeta", "b", "y"), ("Basics", "c", "z")])
        self.assertEqual([r[0] for r in rows(text)], ["Basics", "System", "Zeta"])


class Cosmic(unittest.TestCase):
    def test_needle_must_be_in_the_dockerfile(self):
        with tempfile.TemporaryDirectory() as d:
            src, dock = os.path.join(d, "c.txt"), os.path.join(d, "Dockerfile")
            write(src, "Basics | Super + A | Apps @ needle\n")
            write(dock, "RUN nothing\n")
            with self.assertRaises(gen.Error):
                gen.parse_cosmic(src, dock)
            write(dock, "RUN needle\n")
            self.assertEqual(gen.parse_cosmic(src, dock), [("Basics", "Super + A", "Apps")])


class Repository(unittest.TestCase):
    """The committed sheets, the real configs and the real hadron-help."""

    def test_committed_sheets_are_current(self):
        self.assertEqual(gen.main(["--check"]), 0)

    def test_tiling_variants_document_the_help_key(self):
        # AGENTS.md: Super+F1 opens the shortcut sheet on the tiling variants.
        for v in ("sway", "i3", "niri"):
            with open(os.path.join(gen.ROOT, "rootfs-" + v, gen.OUT), encoding="utf-8") as f:
                got = rows(f.read())
            self.assertIn(("Basics", "Super + F1", "This help"), got, v)

    def test_every_variant_documents_the_hadron_menu(self):
        for v in ("sway", "i3", "niri", "cosmic"):
            with open(os.path.join(gen.ROOT, "rootfs-" + v, gen.OUT), encoding="utf-8") as f:
                keys = [r[1] for r in rows(f.read())]
            self.assertIn("Super + Escape", keys, v)


if __name__ == "__main__":
    unittest.main()
