#!/usr/bin/env python3
"""Tests for tools/stage-key.py. Run: python3 tools/test_stage_key.py"""

import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "stage-key.py")

BASE = """\
# syntax=docker/dockerfile:1
ARG HADRON_VERSION=v1
ARG GPU=vm
FROM example/toolchain:${HADRON_VERSION} AS toolchain

FROM toolchain AS dep
RUN make dep

# A stage nothing below reaches.
FROM toolchain AS unrelated
RUN make unrelated

FROM toolchain AS gpu-vm
RUN make vm
FROM toolchain AS gpu-full
RUN make full
FROM gpu-${GPU} AS gpu

FROM toolchain AS mounted
RUN make mounted

FROM toolchain AS target
ARG GPU
COPY --from=dep /dep /
COPY --from=gpu /gpu /
COPY tools/helper.sh /usr/bin/helper
RUN --mount=type=bind,from=mounted,target=/m \\
    make target
"""


class StageKeyTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        os.mkdir(os.path.join(self.dir, "tools"))
        self.write("tools/helper.sh", "echo one\n")
        self.write("Dockerfile", BASE)

    def write(self, rel, text):
        with open(os.path.join(self.dir, rel), "w") as f:
            f.write(text)

    def key(self, *args, stage="target"):
        out = subprocess.run(
            [sys.executable, SCRIPT, "--dockerfile",
             os.path.join(self.dir, "Dockerfile"), *args, stage],
            capture_output=True, text=True, check=True,
        )
        return out.stdout.strip()

    def edit(self, old, new):
        self.write("Dockerfile", BASE.replace(old, new))

    def test_format(self):
        self.assertRegex(self.key(), r"^target-[0-9a-f]{12}$")

    def test_comments_and_unrelated_stages_do_not_change_it(self):
        k = self.key()
        self.edit("# A stage nothing below reaches.", "# Reworded.\n\n# More.")
        self.assertEqual(self.key(), k)
        self.edit("make unrelated", "make something-else")
        self.assertEqual(self.key(), k)

    def test_every_reached_stage_changes_it(self):
        k = self.key()
        for old, new in [
            ("make dep", "make dep2"),              # COPY --from
            ("make mounted", "make mounted2"),      # RUN --mount from=
            ("make vm", "make vm2"),                # FROM with an ARG
            ("make target", "make target2"),        # the stage itself
            ("AS toolchain", "AS toolchain\nRUN x"),  # the base of all
        ]:
            with self.subTest(old=old):
                self.edit(old, new)
                self.assertNotEqual(self.key(), k)

    def test_the_arg_picks_the_reached_stage(self):
        k = self.key()
        self.edit("make full", "make full2")
        self.assertEqual(self.key(), k)
        self.assertNotEqual(self.key("--arg", "GPU=full"), k)

    def test_build_args(self):
        k = self.key()
        self.assertNotEqual(self.key("--arg", "HADRON_VERSION=v2"), k)
        self.assertEqual(self.key("--arg", "DESKTOP=cosmic"), k)

    def test_context_files(self):
        k = self.key()
        self.write("tools/helper.sh", "echo two\n")
        self.assertNotEqual(self.key(), k)

    def test_unknown_stage_fails(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.key(stage="nope")


if __name__ == "__main__":
    unittest.main()
