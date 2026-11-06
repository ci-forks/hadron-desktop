#!/usr/bin/env python3
"""Generate the keyboard shortcut sheet of each variant from its real config.

    gen-shortcuts.py [--check] [--root DIR]

hadron-help (Super+F1) shows rootfs-<variant>/usr/share/hadron/shortcuts.txt.
This script writes that file from the key bindings of the variant, so the
sheet cannot drift from the config. With --check it writes nothing and exits 1
when a committed sheet differs from what the configs give (CI runs this).

Sources
  sway  rootfs-sway/etc/sway/config, then etc/sway/config.d/*
  i3    rootfs-i3/etc/i3/config, then etc/i3/config.d/*
  niri  rootfs-niri/etc/niri/config.kdl (the binds block)
  cosmic  tools/shortcuts-cosmic.txt: COSMIC keeps its defaults upstream, so
        the keys Hadron documents are listed by hand. A line may end with
        "@ TEXT": the script fails unless the Dockerfile contains TEXT, which
        ties the line to the override Hadron applies.

Descriptions come from comments in the config. A comment must be on its own
line, directly above the binding, because i3 and sway read "#" after a command
as part of the command:

    # help-section: Basics        the section of the rows that follow
    # help: Open a terminal       describes the next binding
    # help+: Move focus           describes the next binding and every
                                  binding on the lines right after it
    # help-row: System | Super + drag | Move a floating window
                                  a row that is not a key binding

("//" starts these comments in niri's KDL.) The description ends at a blank
line, at any other comment or at a line that is not a binding. A binding with
no description is left out of the sheet. Rows with the same section and
description are merged: Super + Left and Super + H become "Super + Left / H".

Output, one row per line, tab separated: SECTION, KEYS, DESCRIPTION. Lines
that start with "#" are comments.
"""

import argparse
import glob
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = "usr/share/hadron/shortcuts.txt"

# Sheet order of the sections; any other section follows, alphabetically.
SECTIONS = ["Basics", "Windows", "Workspaces", "System"]

MODS = {
    "mod4": "Super", "super": "Super", "mod": "Super",
    "mod1": "Alt", "alt": "Alt",
    "shift": "Shift",
    "ctrl": "Ctrl", "control": "Ctrl",
}
MOD_ORDER = ["Super", "Ctrl", "Alt", "Shift"]

KEYS = {
    "return": "Enter", "minus": "-", "plus": "+", "equal": "=", "space": "Space",
    "slash": "/", "comma": ",", "period": ".", "bracketleft": "[",
    "bracketright": "]", "page_down": "Page Down", "page_up": "Page Up",
    "backslash": "\\", "semicolon": ";", "apostrophe": "'", "grave": "`",
}

HELP_RE = re.compile(r"^\s*(?:#|//)\s*help(-section|-row|\+)?:\s*(.*?)\s*$")


class Error(Exception):
    pass


def norm_combo(combo):
    """'Mod+Shift+Slash' -> (('Super', 'Shift'), '/')."""
    parts = combo.split("+")
    mods = []
    for p in parts[:-1]:
        m = MODS.get(p.lower())
        if m is None:
            raise Error("unknown modifier %r in %r" % (p, combo))
        if m not in mods:
            mods.append(m)
    mods.sort(key=MOD_ORDER.index)
    key = parts[-1]
    if not key:
        raise Error("empty key in %r" % combo)
    key = KEYS.get(key.lower(), key)
    if len(key) == 1:
        key = key.upper()
    return tuple(mods), key


class Collector:
    """Turns config lines, in order, into (section, desc, mods, key) rows."""

    def __init__(self):
        self.section = "Basics"
        self.pending = None      # (description, run)
        self.rows = []           # (section, description, mods, key)
        self.literal = []        # (section, keys, description)

    def reset(self):
        self.pending = None

    def comment(self, line):
        """Feed a comment line; any comment ends the previous description."""
        m = HELP_RE.match(line)
        if not m:
            self.reset()
            return
        kind, text = m.groups()
        if kind == "-section":
            self.section = text
            self.reset()
        elif kind == "-row":
            f = [x.strip() for x in text.split("|")]
            if len(f) != 3 or not all(f):
                raise Error("help-row needs 'Section | Keys | Description': %r" % line)
            self.literal.append(tuple(f))
            self.reset()
        else:
            if not text:
                raise Error("empty help text: %r" % line)
            self.pending = (text, kind == "+")

    def binding(self, combo):
        if self.pending is None:
            return
        desc, run = self.pending
        mods, key = norm_combo(combo)
        self.rows.append((self.section, desc, mods, key))
        if not run:
            self.reset()


def parse_i3(paths):
    """sway and i3 share the syntax: set, bindsym, comments, blocks."""
    lines = []
    for p in paths:
        with open(p, encoding="utf-8") as f:
            lines += f.read().splitlines()
    variables = {}
    for line in lines:
        m = re.match(r"^\s*set\s+(\$\w+)\s+(\S.*?)\s*$", line)
        if m:
            variables[m.group(1)] = m.group(2)

    def expand(s):
        # Longest names first, so $ws10 is not read as $ws1 then "0".
        for name in sorted(variables, key=len, reverse=True):
            s = s.replace(name, variables[name])
        return s

    c = Collector()
    for line in lines:
        s = line.strip()
        if not s:
            c.reset()
        elif s.startswith("#"):
            c.comment(s)
        else:
            m = re.match(r"^bindsym\s+((?:--\S+\s+)*)(\S+)\s", s + " ")
            if m:
                c.binding(expand(m.group(2)))
            else:
                c.reset()
    return c


def parse_niri(path):
    with open(path, encoding="utf-8") as f:
        lines = f.read().splitlines()
    c = Collector()
    inside = False
    for line in lines:
        s = line.strip()
        if not inside:
            inside = bool(re.match(r"^binds\s*\{", line))
            continue
        if line.startswith("}"):
            break
        if not s:
            c.reset()
        elif s.startswith("//"):
            c.comment(s)
        else:
            c.binding(s.split()[0])
    return c


def parse_cosmic(path, dockerfile):
    with open(dockerfile, encoding="utf-8") as f:
        docker = f.read()
    rows = []
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            needle = None
            if " @ " in line:
                line, needle = line.split(" @ ", 1)
            fields = [x.strip() for x in line.split("|")]
            if len(fields) != 3 or not all(fields):
                raise Error("%s:%d: want 'Section | Keys | Description'" % (path, n))
            if needle is not None and needle.strip() not in docker:
                raise Error(
                    "%s:%d: the Dockerfile no longer contains %r; "
                    "update the Dockerfile override or this line"
                    % (path, n, needle.strip()))
            rows.append(tuple(fields))
    return rows


def compress(keys):
    """Join the keys of one modifier set: 1 ... 9, arrows, H J K L, A / B."""
    keys = list(dict.fromkeys(keys))
    if len(keys) >= 3 and all(len(k) == 1 and k.isdigit() for k in keys):
        return ["%s ... %s" % (keys[0], keys[-1])]
    out = []
    if {"Left", "Right", "Up", "Down"} <= set(keys):
        keys = [k for k in keys if k not in ("Left", "Right", "Up", "Down")]
        out.append("arrows")
    if {"H", "J", "K", "L"} <= set(keys):
        keys = [k for k in keys if k not in ("H", "J", "K", "L")]
        out.append("H J K L")
    out.sort(key=lambda t: t != "arrows")
    return out + keys


def keys_text(entries):
    """entries: [(mods, key)] of one description -> 'Super + Left / H'."""
    groups = {}
    for mods, key in entries:
        groups.setdefault(mods, []).append(key)
    parts = []
    for mods, keys in groups.items():
        parts.append(" + ".join(list(mods) + [" / ".join(compress(keys))]))
    return " / ".join(parts)


def sheet(source):
    """A Collector, or a list of (section, keys, desc) -> sheet text."""
    if isinstance(source, Collector):
        merged = {}
        for section, desc, mods, key in source.rows:
            merged.setdefault((section, desc), []).append((mods, key))
        rows = [(s, keys_text(e), d) for (s, d), e in merged.items()]
        rows += source.literal
    else:
        rows = list(source)
    names = sorted({r[0] for r in rows},
                   key=lambda s: (SECTIONS.index(s) if s in SECTIONS else len(SECTIONS), s))
    out = ["# Generated by tools/gen-shortcuts.py from the variant's config. Do not edit.",
           "# Columns: section, keys, description."]
    for s in names:
        out += ["%s\t%s\t%s" % r for r in rows if r[0] == s]
    return "\n".join(out) + "\n"


def variants(root):
    def i3like(name, cfg):
        d = os.path.join(root, "rootfs-" + name, "etc", cfg)
        paths = [os.path.join(d, "config")] + sorted(glob.glob(os.path.join(d, "config.d", "*")))
        return lambda: sheet(parse_i3(paths))

    return {
        "sway": i3like("sway", "sway"),
        "i3": i3like("i3", "i3"),
        "niri": lambda: sheet(parse_niri(
            os.path.join(root, "rootfs-niri/etc/niri/config.kdl"))),
        "cosmic": lambda: sheet(parse_cosmic(
            os.path.join(root, "tools/shortcuts-cosmic.txt"),
            os.path.join(root, "Dockerfile"))),
    }


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--check", action="store_true",
                    help="write nothing; exit 1 if a committed sheet is stale")
    ap.add_argument("--root", default=ROOT, help="repository root")
    args = ap.parse_args(argv)

    stale = []
    for name, build in variants(args.root).items():
        try:
            text = build()
        except (Error, OSError) as e:
            print("gen-shortcuts: %s: %s" % (name, e), file=sys.stderr)
            return 1
        if not [l for l in text.splitlines() if not l.startswith("#")]:
            print("gen-shortcuts: %s: no documented bindings found" % name, file=sys.stderr)
            return 1
        path = os.path.join(args.root, "rootfs-" + name, OUT)
        try:
            with open(path, encoding="utf-8") as f:
                old = f.read()
        except FileNotFoundError:
            old = None
        if old == text:
            continue
        if args.check:
            stale.append(path)
        else:
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as f:
                f.write(text)
            print("wrote", os.path.relpath(path, args.root))
    if stale:
        for p in stale:
            print("stale: %s" % os.path.relpath(p, args.root), file=sys.stderr)
        print("Run: python3 tools/gen-shortcuts.py", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
