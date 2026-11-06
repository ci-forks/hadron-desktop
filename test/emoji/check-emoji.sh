#!/bin/sh
# The wiring patterns are grep regexes with a literal $mod, on purpose.
# shellcheck disable=SC2016
# Checks for the emoji picker, without an image or a display:
#
# - tools/gen-emoji-list.sh turns a fixture emoji-test.txt into the list,
#   keeping only fully-qualified emoji, as "<emoji> <name>".
# - hadron-emoji copies the picked emoji (and only the emoji) with wl-copy on
#   Wayland and xsel on X11, and shows a notification. It copies nothing when
#   the picker is cancelled or prints text that is not a line of the list.
# - Every variant binds Super+; to hadron-emoji, and the Hadron menu has an
#   Emoji entry.
#
# hadron-pick, wl-copy, xsel and busctl are fakes on PATH that log what they
# get. POSIX sh; CI runs it on the runner and it runs in alpine:
#
#   test/emoji/check-emoji.sh
#   docker run --rm -v "$PWD":/src -w /src alpine:3.22 sh test/emoji/check-emoji.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail=0
pass() { echo "PASS: $1"; }
bad() { echo "FAIL: $1"; fail=1; }
# expect DESC WANT GOT
expect() {
    if [ "$2" = "$3" ]; then pass "$1"; else bad "$1 (want '$2', got '$3')"; fi
}

# --- The list generator -----------------------------------------------------
"$repo/tools/gen-emoji-list.sh" "$here/fixtures/emoji-test.txt" > "$work/emoji.txt"
expect "list keeps only fully-qualified emoji" "$(cat "$here/fixtures/emoji.txt")" "$(cat "$work/emoji.txt")"

# --- The picker, with fakes ---------------------------------------------------
mkdir -p "$work/bin"
# hadron-pick: print the line named by PICK (a line number, "typed" for text
# that matches no line, or "cancel" to exit 1 like a cancelled picker).
cat > "$work/bin/hadron-pick" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" > "$LOG/pick.args"
cat > "$LOG/pick.input"
case "$PICK" in
    cancel) exit 1 ;;
    typed)  echo "not an emoji" ;;
    *)      sed -n "${PICK}p" "$LOG/pick.input" ;;
esac
EOF
cat > "$work/bin/wl-copy" <<'EOF'
#!/bin/sh
cat > "$LOG/wl-copy"
EOF
cat > "$work/bin/xsel" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" > "$LOG/xsel.args"
cat > "$LOG/xsel"
EOF
cat > "$work/bin/busctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" > "$LOG/notify"
EOF
chmod +x "$work/bin/"*

# run NAME PICK [VAR=VALUE...]: run hadron-emoji with the fakes, logging into
# $work/NAME. The fixture list stands in for /usr/share/hadron/emoji.txt.
run() {
    name=$1 pick=$2; shift 2
    mkdir -p "$work/$name"
    env -u WAYLAND_DISPLAY PATH="$work/bin:$PATH" LOG="$work/$name" PICK="$pick" \
        HADRON_EMOJI_LIST="$here/fixtures/emoji.txt" "$@" \
        sh "$repo/rootfs/usr/bin/hadron-emoji" && echo 0 > "$work/$name/status" \
        || echo $? > "$work/$name/status"
}
log() { cat "$work/$1/$2" 2>/dev/null || echo "<none>"; }

run wayland 2 WAYLAND_DISPLAY=wayland-1
expect "the picker gets the whole list" "$(cat "$here/fixtures/emoji.txt")" "$(log wayland pick.input)"
expect "the picker prompt is Emoji" "Emoji 14" "$(log wayland pick.args)"
expect "Wayland: wl-copy gets only the emoji" "😃" "$(log wayland wl-copy)"
expect "Wayland: xsel is not used" "<none>" "$(log wayland xsel)"
case "$(log wayland notify)" in
    *"Notify susssasa{sv}i Hadron Desktop 0  Emoji Copied 😃 grinning face with big eyes to the clipboard."*)
        pass "Wayland: a notification names the emoji" ;;
    *) bad "Wayland: a notification names the emoji (got '$(log wayland notify)')" ;;
esac
expect "Wayland: exit status" 0 "$(log wayland status)"

# A ZWJ sequence (woman office worker) and a flag are copied whole.
run zwj 4 WAYLAND_DISPLAY=wayland-1
expect "a ZWJ sequence is copied whole" "$(printf '\360\237\221\251\342\200\215\360\237\222\274')" "$(log zwj wl-copy)"
run flag 6 WAYLAND_DISPLAY=wayland-1
expect "a flag is copied whole" "🇮🇹" "$(log flag wl-copy)"

run x11 1 DISPLAY=:0
expect "X11: xsel gets only the emoji" "😀" "$(log x11 xsel)"
expect "X11: xsel writes the clipboard" "--clipboard --input" "$(log x11 xsel.args)"
expect "X11: wl-copy is not used" "<none>" "$(log x11 wl-copy)"

run cancel cancel WAYLAND_DISPLAY=wayland-1
expect "cancel: nothing is copied" "<none>" "$(log cancel wl-copy)"
expect "cancel: no notification" "<none>" "$(log cancel notify)"
expect "cancel: exit status" 0 "$(log cancel status)"

run typed typed WAYLAND_DISPLAY=wayland-1
expect "typed text: nothing is copied" "<none>" "$(log typed wl-copy)"

run nolist 1 WAYLAND_DISPLAY=wayland-1 HADRON_EMOJI_LIST="$work/missing.txt"
expect "no list: exit status" 1 "$(log nolist status)"
case "$(log nolist notify)" in
    *"No emoji list found"*) pass "no list: a notification says so" ;;
    *) bad "no list: a notification says so" ;;
esac

# --- Wiring -------------------------------------------------------------------
# check DESC FILE PATTERN: FILE has a line matching PATTERN (grep -E).
check() {
    if grep -Eq -- "$3" "$repo/$2"; then pass "$1"; else bad "$1"; fi
}
check "sway binds Super+; to hadron-emoji" rootfs-sway/etc/sway/config \
    '^bindsym \$mod\+semicolon +exec hadron-emoji$'
check "i3 binds Super+; to hadron-emoji" rootfs-i3/etc/i3/config \
    '^bindsym \$mod\+semicolon +exec --no-startup-id hadron-emoji$'
check "niri binds Super+; to hadron-emoji" rootfs-niri/etc/niri/config.kdl \
    '^ +Mod\+Semicolon .*\{ spawn "hadron-emoji"; \}$'
check "COSMIC binds Super+; to hadron-emoji (Dockerfile)" Dockerfile \
    'key: "semicolon"\): Spawn\("hadron-emoji"\)'
check "the Hadron menu has an Emoji entry" rootfs/usr/bin/hadron-menu \
    '^ +Emoji\) +exec hadron-emoji ;;$'
for v in sway i3 cosmic niri; do
    check "the $v shortcut sheet lists Super+;" "rootfs-$v/usr/share/hadron/shortcuts.txt" \
        'Super \+ ;'
done
check "fontconfig names Noto Color Emoji" rootfs/usr/share/fontconfig/conf.avail/60-hadron.conf \
    '<family>Noto Color Emoji</family>'
for v in sway i3 cosmic niri; do
    if awk -v s="$v-rootfs" '$1 == "FROM" { in_s = ($NF == s) } in_s && /^COPY --from=emoji \/emoji \/$/ { f = 1 } END { exit !f }' "$repo/Dockerfile"; then
        pass "$v ships the emoji font and list"
    else
        bad "$v ships the emoji font and list"
    fi
done
for v in sway cosmic niri; do
    if awk -v s="$v-rootfs" '$1 == "FROM" { in_s = ($NF == s) } in_s && /^COPY --from=wl-clipboard / { f = 1 } END { exit !f }' "$repo/Dockerfile"; then
        pass "$v ships wl-copy"
    else
        bad "$v ships wl-copy"
    fi
done
if awk '$1 == "FROM" { in_s = ($NF == "x11-desktop-tools") } in_s && /kfish\/xsel/ { f = 1 } END { exit !f }' "$repo/Dockerfile"; then
    pass "i3 ships xsel"
else
    bad "i3 ships xsel"
fi

exit "$fail"
