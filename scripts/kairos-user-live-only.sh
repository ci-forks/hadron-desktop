#!/bin/sh
# Limit Kairos' default `kairos` user to the live ISO and UKI install mode.
#
#   scripts/kairos-user-live-only.sh /system/oem/10_accounting.yaml
#
# kairos-init installs 10_accounting.yaml, whose "Setup users" step creates a
# locked `kairos` user (passwd "!") in the initramfs stage of every boot. On an
# installed system nobody can log in as it, but COSMIC's greeter lists it as
# "Created by entities" next to the real desktop user. The next step in the same
# file gives it the password "kairos" on a live or UKI install boot only; this
# script puts that step's guard on "Setup users" too, so the user exists only
# there. The rest of the file (the admin group, sudoers, serial login) stays as
# kairos-init wrote it.
#
# /etc is a tmpfs overlay on an installed system, so /etc/passwd is rebuilt on
# every boot and an upgraded system loses the user on its first boot from the
# new image. Nothing needs to delete it, and /home/kairos is left alone.
#
# The edit is exact on purpose. If kairos-init changes the shape of the step,
# this script exits non-zero and fails the image build, rather than leaving the
# user in place without a word.
set -eu

[ $# -eq 1 ] || { echo "usage: $0 FILE" >&2; exit 2; }
f="$1"

step='    - name: "Setup users"'
guard='      if: "[ -e /run/cos/live_mode ] || [ -e /run/cos/uki_install_mode ]"'

fail() { echo "kairos-user-live-only: $f: $*" >&2; exit 1; }

[ -f "$f" ] || fail "no such file"
[ "$(grep -cxF "$step" "$f")" = 1 ] || fail "expected exactly one '$step' step"
# The guard is copied from the live-password step, so that step must still use
# it, and it must be the only place that does.
[ "$(grep -cxF "$guard" "$f")" = 1 ] || fail "expected the live/uki guard exactly once (on the live-password step)"
# The three lines after the step must be the locked kairos user, as written
# today. Anything else (a guard already there, another user) needs a human.
after="$(grep -A3 -xF "$step" "$f" | tail -n 3)"
expected='      users:
        kairos:
          passwd: "!"'
[ "$after" = "$expected" ] || fail "the '$step' step no longer creates the locked kairos user as expected:
$after"

# POSIX `a\` form, so it works with GNU and busybox sed.
sed -i "/^    - name: \"Setup users\"\$/a\\
$guard" "$f"

[ "$(grep -A1 -xF "$step" "$f" | tail -n 1)" = "$guard" ] || fail "guard not inserted after '$step'"
[ "$(grep -cxF "$guard" "$f")" = 2 ] || fail "expected the live/uki guard exactly twice after the edit"
