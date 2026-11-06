#!/usr/bin/env bash
# Prove hadron-notify-key sends each key to the variant's notification server,
# and that a click on the welcome notification opens the shortcut sheet. Runs
# the scripts in an Alpine container, with fake makoctl, dunstctl, noctalia,
# busctl, gdbus and hadron-menu that log their arguments.
#
#   test/notify/check-notify-keys.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
exec docker run --rm -i -v "$root/rootfs/usr/bin:/hadron:ro" alpine:3.22 sh -eu <<'EOF'
log=/tmp/log
mkdir -p /fake /etc/hadron-desktop
for t in makoctl dunstctl noctalia hadron-menu; do
    printf '#!/bin/sh\necho "%s $*" >> %s\n' "$t" "$log" > /fake/$t
done
# mako's mode list and dunst's pause state, as the toggles leave them.
cat >> /fake/makoctl <<'M'
[ "$1" = mode ] && [ $# -eq 1 ] && cat /tmp/mako-mode 2>/dev/null
[ "$1" = mode ] && [ "${2:-}" = -t ] && { grep -qx do-not-disturb /tmp/mako-mode 2>/dev/null && : > /tmp/mako-mode || echo do-not-disturb > /tmp/mako-mode; }
exit 0
M
cat >> /fake/dunstctl <<'D'
[ "$1" = is-paused ] && cat /tmp/dunst-paused
[ "$1" = set-paused ] && { [ "$(cat /tmp/dunst-paused)" = true ] && echo false > /tmp/dunst-paused || echo true > /tmp/dunst-paused; }
exit 0
D
echo false > /tmp/dunst-paused
# busctl: log the notification summary and answer with id 7.
cat > /fake/busctl <<'B'
#!/bin/sh
for a; do case "$a" in Welcome*|Notifications\ on) echo "notify $a" >> /tmp/log ;; esac; done
echo "u 7"
B
# gdbus monitor: replay signals from /tmp/signals.
printf '#!/bin/sh\ncat /tmp/signals\n' > /fake/gdbus
chmod +x /fake/*
export PATH=/fake:$PATH HOME=/tmp/home
fail=0
check() { # name, command, expected log
    : > $log
    sh -c "$2" >/dev/null 2>&1 || true
    if [ "$(cat $log)" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got [$(cat $log)] want [$3]"; fail=1; fi
}
session() { echo "DESKTOP_NAME=$1" > /etc/hadron-desktop/session; }

session Sway
check "sway dismiss"      "/hadron/hadron-notify-key dismiss"     "makoctl dismiss"
check "sway dismiss all"  "/hadron/hadron-notify-key dismiss-all" "makoctl dismiss --all"
check "sway restore"      "/hadron/hadron-notify-key restore"     "makoctl restore"
check "sway dnd on"       "/hadron/hadron-notify-key dnd"         "$(printf 'makoctl mode -t do-not-disturb\nmakoctl mode')"
check "sway dnd off"      "/hadron/hadron-notify-key dnd"         "$(printf 'makoctl mode -t do-not-disturb\nmakoctl mode\nnotify Notifications on')"

session i3
check "i3 dismiss"        "/hadron/hadron-notify-key dismiss"     "dunstctl close"
check "i3 dismiss all"    "/hadron/hadron-notify-key dismiss-all" "dunstctl close-all"
check "i3 restore"        "/hadron/hadron-notify-key restore"     "dunstctl history-pop"
check "i3 dnd on"         "/hadron/hadron-notify-key dnd"         "$(printf 'dunstctl set-paused toggle\ndunstctl is-paused')"
check "i3 dnd off"        "/hadron/hadron-notify-key dnd"         "$(printf 'dunstctl set-paused toggle\ndunstctl is-paused\nnotify Notifications on')"

session niri
check "niri dismiss"      "/hadron/hadron-notify-key dismiss"     "noctalia msg notification-clear-active"
check "niri restore"      "/hadron/hadron-notify-key restore"     "noctalia msg panel-toggle control-center notifications"
check "niri dnd"          "/hadron/hadron-notify-key dnd"         "noctalia msg notification-dnd-toggle"

session COSMIC
f=$HOME/.config/cosmic/com.system76.CosmicNotifications/v1/do_not_disturb
check "cosmic dismiss is a no-op" "/hadron/hadron-notify-key dismiss" ""
check "cosmic dnd on"     "/hadron/hadron-notify-key dnd"         ""
[ "$(cat $f)" = true ] && echo "ok   cosmic dnd file is true" || { echo "FAIL cosmic dnd file: $(cat $f)"; fail=1; }
check "cosmic dnd off"    "/hadron/hadron-notify-key dnd"         "notify Notifications on"
[ "$(cat $f)" = false ] && echo "ok   cosmic dnd file is false" || { echo "FAIL cosmic dnd file: $(cat $f)"; fail=1; }

if /hadron/hadron-notify-key bogus 2>/dev/null; then echo "FAIL unknown key accepted"; fail=1; else echo "ok   unknown key refused"; fi

# Welcome: a click (ActionInvoked for our id 7) opens the shortcut sheet; a
# close, or a click on another notification, does not.
welcome() { # name, signals, expected log
    rm -rf $HOME/.local/state
    printf '%s\n' "$2" > /tmp/signals
    check "$1" "/hadron/hadron-welcome" "$3"
}
welcome "welcome click opens the sheet" \
    "/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 7, 'default')" \
    "$(printf 'notify Welcome to Hadron Desktop\nhadron-menu Help')"
welcome "welcome closed: no sheet" \
    "/org/freedesktop/Notifications: org.freedesktop.Notifications.NotificationClosed (uint32 7, 2)" \
    "notify Welcome to Hadron Desktop"
welcome "another notification's click: no sheet" \
    "/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 17, 'default')" \
    "notify Welcome to Hadron Desktop"
[ -e $HOME/.local/state/hadron-desktop/welcomed ] && echo "ok   welcome stamp written" || { echo "FAIL no welcome stamp"; fail=1; }
check "welcome shows once" "/hadron/hadron-welcome" ""

exit $fail
EOF
