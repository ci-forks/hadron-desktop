#!/bin/sh
# Exercise hadron-first-run-apps (the first-run app picker) against fake
# flatpak, nm-online, busctl and terminal programs, in an Alpine container,
# so busybox sh runs the script the way a minimal POSIX shell would.
#
#   test/first-run/check-first-run-apps.sh            run in alpine (docker)
#   test/first-run/check-first-run-apps.sh --inner    run here (used by the
#                                                     container; needs a
#                                                     throwaway HOME)
#
# It checks the selection logic (defaults, one browser, toggles, installed
# apps), the stamp (written on completion and on skip, not when nothing
# installed), and the --login hook (stamp, network, Flathub, COSMIC).
set -eu

if [ "${1:-}" != --inner ]; then
    repo=$(cd "$(dirname "$0")/../.." && pwd)
    exec docker run --rm -v "$repo:/src:ro" "${ALPINE_IMAGE:-alpine:3.22}" \
        sh /src/test/first-run/check-first-run-apps.sh --inner
fi

src=${SRC:-/src}
script="$src/rootfs/usr/bin/hadron-first-run-apps"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
bin="$work/bin"
mkdir -p "$bin"
log="$work/log"

# Fakes. Each records what it was asked into $log.
cat > "$bin/flatpak" <<'EOF'
#!/bin/sh
echo "flatpak $*" >> "$FAKE_LOG"
case "$*" in
    "--user remotes "*) printf '%s\n' "${FAKE_REMOTES-flathub	}" ;;
    "list "*) printf '%s\n' "${FAKE_INSTALLED:-}" ;;
    "--user install "*)
        for id; do :; done
        case " ${FAKE_FAIL:-} " in *" $id "*) exit 1 ;; esac ;;
esac
exit 0
EOF
cat > "$bin/nm-online" <<'EOF'
#!/bin/sh
echo "nm-online $*" >> "$FAKE_LOG"
exit "${FAKE_NM:-0}"
EOF
cat > "$bin/busctl" <<'EOF'
#!/bin/sh
shift 6
echo "notify $5" >> "$FAKE_LOG"
EOF
cat > "$bin/fake-term" <<'EOF'
#!/bin/sh
echo "terminal $*" >> "$FAKE_LOG"
EOF
chmod +x "$bin"/*
cp "$script" "$bin/hadron-first-run-apps"

printf 'DESKTOP_NAME=Sway\nTERMINAL_EXEC="fake-term -e"\n' > "$work/session"
export FAKE_LOG="$log" PATH="$bin:$PATH" HADRON_SESSION_FILE="$work/session"

fail=0
pass() { echo "PASS: $1"; }
bad() { echo "FAIL: $1" >&2; fail=1; }

# reset: a fresh HOME and log, and no fake knobs.
reset() {
    HOME="$work/home"
    rm -rf "$HOME" && mkdir -p "$HOME"
    : > "$log"
    unset FAKE_INSTALLED FAKE_FAIL FAKE_NM FAKE_REMOTES XDG_STATE_HOME
    export HOME
}
stamp() { cat "$HOME/.local/state/hadron/first-run-apps" 2>/dev/null || echo none; }
installs() { sed -n 's/^flatpak --user install --assumeyes flathub //p' "$log" | tr '\n' ' '; }

# ui INPUT: run the checklist with INPUT on stdin.
ui() { printf '%b' "$1" | hadron-first-run-apps --ui > "$work/out" 2>&1; }

has() { # PATTERN FILE NAME
    if grep -q "$1" "$2"; then pass "$3"; else bad "$3"; fi
}
expect() { # NAME GOT WANT
    if [ "$2" = "$3" ]; then pass "$1"; else bad "$1: got '$2', want '$3'"; fi
}

reset; ui '\n'
expect "Enter installs the defaults" "$(installs)" \
    "org.mozilla.firefox org.gnome.Loupe org.videolan.VLC org.gnome.TextEditor org.gnome.Papers "
expect "completion writes the stamp" "$(stamp)" completed
has '^notify Apps installed' "$log" "final notification"

reset; ui '2 9\n\n'
expect "a second browser replaces the first; Flatseal adds" "$(installs)" \
    "org.chromium.Chromium org.gnome.Loupe org.videolan.VLC org.gnome.TextEditor org.gnome.Papers com.github.tchx84.Flatseal "

reset; ui '1\n\n'
expect "toggling the picked browser drops it" "$(installs)" \
    "org.gnome.Loupe org.videolan.VLC org.gnome.TextEditor org.gnome.Papers "

reset; ui '0 5 6 7 8\n\n'
expect "no browser, no essentials: nothing installed" "$(installs)" ""
expect "an empty choice still completes" "$(stamp)" completed

reset; ui 'abc 99 -1\n\n'
expect "bad input is ignored" "$(installs)" \
    "org.mozilla.firefox org.gnome.Loupe org.videolan.VLC org.gnome.TextEditor org.gnome.Papers "

reset; ui 's\n'
expect "skip installs nothing" "$(installs)" ""
expect "skip writes the stamp" "$(stamp)" skipped

reset; ui ''
expect "end of input dismisses" "$(stamp)" skipped

reset; FAKE_INSTALLED="org.mozilla.firefox
org.gnome.Loupe"; export FAKE_INSTALLED; ui '\n'
expect "installed apps are not installed again, nor a second browser" "$(installs)" \
    "org.videolan.VLC org.gnome.TextEditor org.gnome.Papers "
has 'Firefox (installed)' "$work/out" "installed apps are marked"

reset; FAKE_FAIL="org.mozilla.firefox org.gnome.Loupe org.videolan.VLC org.gnome.TextEditor org.gnome.Papers"
export FAKE_FAIL; ui '\n'
expect "all installs failed: no stamp" "$(stamp)" none
has '^notify Apps did not install' "$log" "failure notification"

reset; FAKE_FAIL="org.videolan.VLC"; export FAKE_FAIL; ui '\n'
expect "some installs failed: stamp" "$(stamp)" completed
has '^notify Some apps did not install' "$log" "partial notification"

reset; XDG_STATE_HOME="$work/xdg"; export XDG_STATE_HOME; ui 's\n'
expect "the stamp follows XDG_STATE_HOME" "$(cat "$work/xdg/hadron/first-run-apps")" skipped

# --- the login hook ---
reset; hadron-first-run-apps --login
expect "login opens the checklist in the session terminal" "$(grep '^terminal' "$log")" \
    "terminal -e hadron-first-run-apps --ui"

reset; mkdir -p "$HOME/.local/state/hadron"; echo skipped > "$HOME/.local/state/hadron/first-run-apps"
hadron-first-run-apps --login
expect "login does nothing after the stamp" "$(cat "$log")" ""

reset; FAKE_NM=1; export FAKE_NM; hadron-first-run-apps --login
expect "offline: no terminal" "$(grep -c '^terminal' "$log" || :)" 0
expect "offline: no stamp" "$(stamp)" none
has '^notify No network' "$log" "offline notification"

reset; FAKE_REMOTES="flathub	disabled"; export FAKE_REMOTES; hadron-first-run-apps --login
expect "disabled Flathub: no terminal" "$(grep -c '^terminal' "$log" || :)" 0
has '^notify Flathub is not ready' "$log" "Flathub notification"

reset; FAKE_REMOTES=""; export FAKE_REMOTES; hadron-first-run-apps --login
expect "no Flathub remote: no terminal" "$(grep -c '^terminal' "$log" || :)" 0

# COSMIC: wait for cosmic-initial-setup while it runs, then go on.
reset
printf 'DESKTOP_NAME=COSMIC\nTERMINAL_EXEC="fake-term -e"\n' > "$work/cosmic"
mkdir -p "$work/proc/42"; echo cosmic-initial- > "$work/proc/42/comm"
( sleep 3; rm -rf "$work/proc/42" ) &
start=$(date +%s)
HADRON_SESSION_FILE="$work/cosmic" HADRON_PROC="$work/proc" HADRON_FIRST_RUN_SETUP_GRACE=0 \
    hadron-first-run-apps --login
waited=$(( $(date +%s) - start ))
if [ "$waited" -ge 2 ]; then
    pass "COSMIC: waits while cosmic-initial-setup runs"
else
    bad "COSMIC: did not wait for cosmic-initial-setup ($waited s)"
fi
expect "COSMIC: then opens the checklist" "$(grep -c '^terminal' "$log")" 1

reset; mkdir -p "$HOME/.config"; : > "$HOME/.config/cosmic-initial-setup-done"
HADRON_SESSION_FILE="$work/cosmic" hadron-first-run-apps --login
expect "COSMIC: setup done, no wait" "$(grep -c '^terminal' "$log")" 1

exit "$fail"
