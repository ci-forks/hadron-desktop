#!/bin/sh
# Check rootfs/usr/bin/hadron-night-light: the saved setting, toggle, restore
# at session start, and that each variant starts and stops its own tool. The
# tools (wlsunset, xsct, noctalia, busctl) and the clock are fakes that log
# their arguments, so no display is needed.
#
#   test/night-light/check-night-light.sh
#
# The script reads /etc/hadron-desktop/session, so the checks run as root in
# a throwaway Alpine container.
set -eu

if [ "${NIGHT_LIGHT_IN_CONTAINER:-}" != 1 ]; then
    root=$(cd "$(dirname "$0")/../.." && pwd)
    exec docker run --rm -e NIGHT_LIGHT_IN_CONTAINER=1 -v "$root:/src:ro" \
        alpine:3.22 sh /src/test/night-light/check-night-light.sh
fi

script=/src/rootfs/usr/bin/hadron-night-light
fail=0
ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }
check() { # name, command...
    name=$1; shift
    if "$@"; then ok "$name"; else bad "$name"; fi
}

t=$(mktemp -d)
mkdir -p "$t/bin" /etc/hadron-desktop
log=$t/log
: > "$log"

# Fakes. wlsunset stays up like the real one; xsct returns at once.
cat > "$t/bin/wlsunset" <<'EOF'
#!/bin/sh
echo "wlsunset $*" >> "$FAKE_LOG"
trap 'echo "wlsunset stopped" >> "$FAKE_LOG"; exit 0' TERM
while :; do sleep 1; done
EOF
cat > "$t/bin/xsct" <<'EOF'
#!/bin/sh
echo "xsct $*" >> "$FAKE_LOG"
EOF
# noctalia fails while $FAKE_NOCTALIA_DOWN counts down, as before the shell is up.
cat > "$t/bin/noctalia" <<'EOF'
#!/bin/sh
n=$(cat "$FAKE_NOCTALIA_DOWN" 2>/dev/null || echo 0)
if [ "$n" -gt 0 ]; then echo $((n - 1)) > "$FAKE_NOCTALIA_DOWN"; exit 1; fi
echo "noctalia $*" >> "$FAKE_LOG"
EOF
cat > "$t/bin/busctl" <<'EOF'
#!/bin/sh
echo "notify $*" >> "$FAKE_LOG"
EOF
# The clock: HHMM from $FAKE_HHMM.
cat > "$t/bin/date" <<'EOF'
#!/bin/sh
echo "$FAKE_HHMM"
EOF
# Short naps, so the retry and the xsct loop run fast.
cat > "$t/bin/sleep" <<'EOF'
#!/bin/sh
exec /bin/sleep 0.1
EOF
chmod +x "$t/bin/"*

export PATH="$t/bin:$PATH" FAKE_LOG="$log" FAKE_HHMM=1200 \
    FAKE_NOCTALIA_DOWN="$t/down" HOME="$t/home" XDG_RUNTIME_DIR="$t/run"
unset XDG_STATE_HOME
mkdir -p "$HOME" "$XDG_RUNTIME_DIR"
state="$HOME/.local/state/hadron-desktop/night-light"
pidf="$XDG_RUNTIME_DIR/hadron-night-light.pid"

session() { printf 'DESKTOP_NAME=%s\n' "$1" > /etc/hadron-desktop/session; }
reset() { : > "$log"; rm -rf "$HOME/.local" "$pidf" "$t/down"; }
saved() { mkdir -p "$(dirname "$state")" && echo "$1" > "$state"; }
has() { grep -q -- "$1" "$log"; }
count() { grep -c -- "$1" "$log" || true; }
# shellcheck disable=SC2317 # called through check().
alive() { [ -s "$pidf" ] && kill -0 "$(cat "$pidf")" 2>/dev/null; }
# wait_for PATTERN: the fakes run in the background; give them a moment.
wait_for() {
    i=0
    while ! has "$1"; do
        i=$((i + 1)); [ "$i" -ge 50 ] && return 1
        /bin/sleep 0.1
    done
}

# --- Common ---------------------------------------------------------------
session Sway; reset
check "status is off before the first use" [ "$("$script" status)" = off ]
rc=0; "$script" bogus 2>/dev/null || rc=$?
check "an unknown command exits 2" [ "$rc" = 2 ]
saved garbage
check "a damaged state file reads as off" [ "$("$script" status)" = off ]

# --- Sway: wlsunset --------------------------------------------------------
reset
"$script" toggle
check "sway: toggle from off saves on" [ "$(cat "$state")" = on ]
check "sway: toggle starts wlsunset with the fixed times" \
    wait_for "wlsunset -t 4500 -T 6500 -S 07:00 -s 20:00"
check "sway: wlsunset pid is recorded and alive" alive
check "sway: a pop-up says when it is warm" has "On: warmer screen from 20:00 to 07:00"
"$script" restore
/bin/sleep 0.3
check "sway: restore does not start a second wlsunset" [ "$(count 'wlsunset -t')" = 1 ]
pid=$(cat "$pidf")
"$script" toggle
check "sway: toggle from on saves off" [ "$(cat "$state")" = off ]
check "sway: toggle stops wlsunset" wait_for "wlsunset stopped"
check "sway: the stopped wlsunset is gone" sh -c "! kill -0 $pid 2>/dev/null"
check "sway: the pid file is removed" [ ! -e "$pidf" ]

reset
"$script" restore
/bin/sleep 0.3
check "sway: restore with no saved setting starts nothing" [ ! -s "$log" ]
"$script" on; "$script" on
/bin/sleep 0.3
check "sway: on twice leaves one wlsunset" [ "$(( $(count 'wlsunset -t') - $(count 'wlsunset stopped') ))" = 1 ]
"$script" off
wait_for "wlsunset stopped" || :
rm -f "$pidf"; : > "$log"
saved on
"$script" restore
check "sway: restore with on saved starts wlsunset" wait_for "wlsunset -t 4500"
"$script" off; wait_for "wlsunset stopped" || :

# --- i3: the xsct loop -----------------------------------------------------
session i3; reset
FAKE_HHMM=2100 "$script" on
check "i3: on at 21:00 sets 4500 K" wait_for "xsct 4500"
check "i3: the loop pid is recorded and alive" alive
"$script" off
check "i3: off resets the colour" wait_for "xsct 0"
check "i3: off stops the loop" [ ! -e "$pidf" ]
check "i3: off saves off" [ "$(cat "$state")" = off ]

# The night window crosses midnight: 20:00 to 07:00.
for case in 2000:night 2359:night 0000:night 0659:night 0700:day 1200:day 1959:day; do
    hhmm=${case%:*} want=${case#*:}
    : > "$log"
    FAKE_HHMM=$hhmm "$script" __xsct-loop &
    loop=$!
    wait_for "xsct" || :
    kill "$loop" 2>/dev/null; wait "$loop" 2>/dev/null || :
    first=$(head -n1 "$log")
    if [ "$want" = night ]; then w="xsct 4500"; else w="xsct 0"; fi
    check "i3: at $hhmm it is $want" [ "$first" = "$w" ]
done

# --- niri: Noctalia's night light -----------------------------------------
session niri; reset
"$script" toggle
check "niri: toggle on enables Noctalia's schedule" has "noctalia msg nightlight-enable"
check "niri: no pop-up, Noctalia shows its own" sh -c "! grep -q notify '$log'"
"$script" toggle
check "niri: toggle off disables it" has "noctalia msg nightlight-disable"
reset
saved on; echo 3 > "$t/down"
"$script" restore
check "niri: restore waits for Noctalia to come up" has "noctalia msg nightlight-enable"
reset
echo 3 > "$t/down"
rc=0; "$script" on 2>/dev/null || rc=$?
check "niri: on fails at once when Noctalia is not running" [ "$rc" = 1 ]

# --- COSMIC: no night light ------------------------------------------------
session COSMIC; reset
rc=0; "$script" toggle 2>/dev/null || rc=$?
check "cosmic: toggle exits 1" [ "$rc" = 1 ]
check "cosmic: toggle says it is not available" has "not available on COSMIC"
check "cosmic: toggle saves nothing" [ ! -e "$state" ]
"$script" restore
check "cosmic: restore is quiet" sh -c "! grep -q 'noctalia\|wlsunset\|xsct' '$log'"

rm -rf "$t"
exit "$fail"
