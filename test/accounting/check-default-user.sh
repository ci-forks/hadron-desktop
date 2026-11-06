#!/usr/bin/env bash
# Assert that Kairos' default `kairos` user exists only on a live or UKI install
# boot, and that the rest of 10_accounting.yaml (admin group, sudoers, serial
# login) is still there.
#
#   test/accounting/check-default-user.sh IMAGE
#   test/accounting/check-default-user.sh --file FILE
#   test/accounting/check-default-user.sh --edit FILE
#
# IMAGE reads /system/oem/10_accounting.yaml from a built image. --file checks a
# file as it is. --edit runs scripts/kairos-user-live-only.sh on a copy of an
# unedited file (for example one taken from kairos-init or an older release),
# checks the result, and checks that a second run fails instead of editing again.
set -euo pipefail

usage() { echo "usage: $0 IMAGE | --file FILE | --edit FILE" >&2; exit 2; }

here="$(cd "$(dirname "$0")" && pwd)"
edit_script="$here/../../scripts/kairos-user-live-only.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
f="$tmp/10_accounting.yaml"

case "${1:-}" in
    --file) [ $# -eq 2 ] || usage; cp "$2" "$f" ;;
    --edit) [ $# -eq 2 ] || usage; cp "$2" "$f"; sh "$edit_script" "$f" ;;
    -*|"") usage ;;
    *) [ $# -eq 1 ] || usage
       docker run --rm --entrypoint cat "$1" /system/oem/10_accounting.yaml > "$f" ;;
esac

fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

guard='[ -e /run/cos/live_mode ] || [ -e /run/cos/uki_install_mode ]'

# One line per step of the file: name, if and the users it sets, separated by
# tabs (an `if` contains "||"). A step starts at
# a "    - " item; its name, if and users are the keys indented under it.
steps="$(awk '
    function flush() { if (started) print name "\t" cond "\t" users; name = ""; cond = ""; users = ""; inusers = 0 }
    /^    - / { flush(); started = 1; line = substr($0, 7) }
    /^    - / || /^      [a-z_]+:/ {
        l = ($0 ~ /^    - /) ? line : substr($0, 7)
        inusers = 0
        if (l ~ /^name: /)  { v = substr(l, 7); sub(/[ \t]+#.*$/, "", v); gsub(/^"|"$/, "", v); name = v }
        if (l ~ /^if: /)    { v = substr(l, 5); gsub(/^["\x27]|["\x27]$/, "", v); cond = v }
        if (l ~ /^users:/)  { inusers = 1 }
        next
    }
    inusers && /^        [a-z_][a-z0-9_-]*:/ { u = $1; sub(/:$/, "", u); users = users u " " }
    END { flush() }
' "$f")"

kairos_steps="$(printf '%s\n' "$steps" | awk -F'\t' '$3 ~ /(^| )kairos /')"
if [ -z "$kairos_steps" ]; then
    bad "no step creates the kairos user; the live ISO needs it"
else
    unguarded="$(printf '%s\n' "$kairos_steps" | awk -F'\t' -v g="$guard" '$2 != g')"
    if [ -z "$unguarded" ]; then
        ok "every step that sets the kairos user runs only on a live or UKI install boot ($(printf '%s\n' "$kairos_steps" | wc -l) steps)"
    else
        bad "a step sets the kairos user on an installed system too: $unguarded"
    fi
fi
if grep -qF 'passwd: "!"' "$f"; then
    ok "the locked kairos user is still defined (for the live/uki path)"
else
    bad "the locked kairos user definition is gone; the edit removed more than the guard"
fi

for name in "Setup groups" "Setup sudo" "Enable serial login for alpine"; do
    if printf '%s\n' "$steps" | awk -F'\t' -v n="$name" '$1 == n && $2 !~ /live_mode \] \|\|/' | grep -q .; then
        ok "step '$name' is kept and not limited to live/uki"
    else
        bad "step '$name' is missing or limited to live/uki"
    fi
done
if grep -qxF '           %admin ALL=(ALL) NOPASSWD: ALL' "$f"; then
    ok "sudoers grants the admin group"
else
    bad "sudoers no longer grants the admin group"
fi

if [ "${1:-}" = "--edit" ]; then
    if sh "$edit_script" "$f" 2>/dev/null; then
        bad "a second run of kairos-user-live-only.sh edited the file again instead of failing"
    else
        ok "a second run of kairos-user-live-only.sh fails, so a changed upstream file breaks the build"
    fi
fi

exit "$fail"
