#!/usr/bin/env python3
"""Print a key for one Dockerfile stage: a hash of everything it is built from.

    stage-key.py [--dockerfile Dockerfile] [--arg NAME=VALUE ...] STAGE

The key covers the stage and every stage it reaches through FROM, COPY --from
and RUN --mount=...,from=, with comments and blank lines left out; the build
context files those stages COPY or ADD; and the values of the global ARGs they
use (the defaults at the top of the Dockerfile, overridden by --arg the way
--build-arg overrides them). Two builds of the stage with the same key run the
same instructions on the same inputs, so CI can push the stage as an image
tagged with the key and reuse it until something it depends on changes.

The key does not cover what the instructions download: those are pinned by
version or commit in the stage text, which is.
"""

import argparse
import hashlib
import os
import re
import sys

FROM_RE = re.compile(r"^FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?\s*$", re.I)
ARG_RE = re.compile(r"^ARG\s+([A-Za-z_][A-Za-z0-9_]*)(?:=(.*))?$")
STAGE_REF_RE = re.compile(r"(?:--from=|,from=)([^\s,]+)")
COPY_RE = re.compile(r"^(?:COPY|ADD)\s+(.*)$", re.I)
VAR_RE = re.compile(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?")


def logical_lines(text):
    """Join backslash continuations; drop comments and blank lines."""
    out, cur = [], ""
    for raw in text.splitlines():
        line = raw.strip()
        if not cur and (not line or line.startswith("#")):
            continue
        if cur and line.startswith("#"):
            continue
        if line.endswith("\\"):
            cur += line[:-1] + " "
            continue
        out.append(cur + line)
        cur = ""
    if cur:
        out.append(cur)
    return out


def parse(path):
    """Return (global ARG defaults, {stage name: (base, [lines])})."""
    with open(path, encoding="utf-8") as f:
        lines = logical_lines(f.read())
    global_args, stages, current = {}, {}, None
    for line in lines:
        m = FROM_RE.match(line)
        if m:
            base, name = m.group(1), m.group(2)
            current = (name or f"#{len(stages)}").lower()
            stages[current] = (base, [line])
            continue
        if current is None:
            a = ARG_RE.match(line)
            if a:
                global_args[a.group(1)] = (a.group(2) or "").strip().strip('"')
            continue
        stages[current][1].append(line)
    return global_args, stages


def expand(s, args):
    return VAR_RE.sub(lambda m: args.get(m.group(1), ""), s)


def context_sources(line):
    """The build-context paths a COPY or ADD line reads, or [] for --from."""
    m = COPY_RE.match(line)
    if not m:
        return []
    words = m.group(1).split()
    if any(w.startswith("--from=") for w in words):
        return []
    words = [w for w in words if not w.startswith("--")]
    return words[:-1]


def hash_path(h, root, rel):
    full = os.path.join(root, rel)
    if not os.path.exists(full):
        sys.exit(f"stage-key: {rel} is copied from the build context but does not exist")
    paths = [full]
    if os.path.isdir(full):
        paths = sorted(
            os.path.join(d, f) for d, _, fs in os.walk(full) for f in fs
        )
    for p in paths:
        h.update(os.path.relpath(p, root).encode() + b"\0")
        with open(p, "rb") as f:
            h.update(hashlib.sha256(f.read()).digest())


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--dockerfile", default="Dockerfile")
    ap.add_argument("--arg", action="append", default=[], metavar="NAME=VALUE")
    ap.add_argument("stage")
    opts = ap.parse_args()

    global_args, stages = parse(opts.dockerfile)
    global_args.update(kv.partition("=")[::2] for kv in opts.arg)
    # Defaults may refer to earlier ones (BASE_IMAGE uses HADRON_VERSION).
    resolved = {}
    for k, v in global_args.items():
        resolved[k] = expand(v, resolved)

    stage = opts.stage.lower()
    if stage not in stages:
        sys.exit(f"stage-key: no stage named {opts.stage} in {opts.dockerfile}")

    seen, todo = [], [stage]
    while todo:
        name = todo.pop()
        if name in seen:
            continue
        seen.append(name)
        base, lines = stages[name]
        refs = [base] + [r for l in lines for r in STAGE_REF_RE.findall(l)]
        for r in refs:
            r = expand(r, resolved).lower()
            if r in stages and r not in seen:
                todo.append(r)

    root = os.path.dirname(os.path.abspath(opts.dockerfile))
    h = hashlib.sha256()
    used_args = set()
    for name in sorted(seen):
        base, lines = stages[name]
        used_args.update(VAR_RE.findall(base))
        for line in lines:
            a = ARG_RE.match(line)
            if a:
                used_args.add(a.group(1))
            h.update(line.encode() + b"\n")
            for src in context_sources(line):
                hash_path(h, root, src)
    # A stage ARG takes the global value only when it declares no default of
    # its own, but a --build-arg overrides it either way, so the value of every
    # ARG the stages name is hashed, whether it came from a default or --arg.
    for k in sorted(used_args & resolved.keys()):
        h.update(f"{k}={resolved[k]}\n".encode())

    print(f"{opts.stage}-{h.hexdigest()[:12]}")


if __name__ == "__main__":
    main()
