#!/usr/bin/env bash
# Run hadron-apps against fixture .desktop files in a throwaway alpine
# container, with a fake picker that prints the app named in $PICK. Asserts the
# listing (what is shown, hidden and overridden) and the launched command.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

docker run --rm -i -v "$ROOT/rootfs/usr/bin/hadron-apps:/usr/bin/hadron-apps:ro" \
    alpine:3.22 sh -eu -s <<'INNER'
sys=/usr/share/applications fp=/var/lib/flatpak/exports/share/applications
home=/root/.local/share/applications
mkdir -p $sys $fp $home /out

mk() { # file name exec [extra lines...]
    f=$1 n=$2 e=$3; shift 3
    { printf '[Desktop Entry]\nType=Application\nName=%s\nExec=%s\n' "$n" "$e"
      for l in "$@"; do printf '%s\n' "$l"; done
      printf '[Desktop Action x]\nName=Hidden action\nExec=false\n'; } >"$f"
}
mk $sys/alpha.desktop   Alpha   "sh -c 'echo alpha \$0 > /out/alpha' %U %f %%x"
mk $sys/over.desktop    System-Over "echo system > /out/over"
mk $home/over.desktop   User-Over   "sh -c 'echo user > /out/over'"
mk $sys/nodisp.desktop  NoDisp  true NoDisplay=true
mk $sys/hid.desktop     Hid     true Hidden=true
mk $sys/onlyk.desktop   OnlyKde true 'OnlyShowIn=KDE;GNOME;'
mk $sys/noti.desktop    NotI3   true 'NotShowIn=i3;'
mk $sys/onlyi3.desktop  OnlyI3  "sh -c 'echo i3 > /out/onlyi3'" 'OnlyShowIn=i3;'
mk $sys/tui.desktop     Tui     "sh -c 'echo tui > /out/tui'" Terminal=true
mkdir -p $sys/sub && mk $sys/sub/dir.desktop Sub "sh -c 'echo sub > /out/sub'"
mk $fp/org.example.Flat.desktop "Flat App" "sh -c 'echo flat > /out/flat' @@u %U @@"
# a user file that hides a system app removes it entirely
mk $sys/gone.desktop Gone true
mk $home/gone.desktop Gone true NoDisplay=true

# Fake picker: records the list, prints $PICK.
cat >/usr/bin/hadron-pick <<'P'
#!/bin/sh
cat >/out/list
[ -n "$PICK" ] && printf '%s\n' "$PICK"
P
chmod +x /usr/bin/hadron-pick
# Fake terminal: records that it ran, then runs the command.
cat >/usr/bin/faketerm <<'P'
#!/bin/sh
echo term > /out/term; shift; exec "$@"
P
chmod +x /usr/bin/faketerm

export XDG_CURRENT_DESKTOP=i3 TERMINAL_EXEC="faketerm -e"
fail=0
ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }
launch() {
    rm -f /out/alpha /out/over /out/tui /out/term /out/flat /out/sub /out/onlyi3
    PICK=$1 hadron-apps || true
    sleep 0.3
}

PICK= hadron-apps || true
list=$(cat /out/list)
for want in Alpha User-Over "Flat App" Tui OnlyI3 Sub; do
    if printf '%s\n' "$list" | grep -qx "$want"; then ok "lists $want"; else bad "lists $want"; fi
done
for no in System-Over NoDisp Hid OnlyKde NotI3 Gone "Hidden action"; do
    if printf '%s\n' "$list" | grep -qx "$no"; then bad "hides $no"; else ok "hides $no"; fi
done
if [ "$(printf '%s\n' "$list" | sort -f)" = "$list" ]; then ok sorted; else bad sorted; fi

launch Alpha
if [ "$(cat /out/alpha 2>&1)" = "alpha %x" ]; then ok "field codes stripped, %% kept"; else bad "alpha: $(cat /out/alpha 2>&1)"; fi
if [ ! -e /out/term ]; then ok "plain app skips the terminal"; else bad "plain app used the terminal"; fi
launch User-Over
if [ "$(cat /out/over 2>&1)" = user ]; then ok "user overrides system"; else bad override; fi
launch "Flat App"
if [ "$(cat /out/flat 2>&1)" = flat ]; then ok "flatpak export launches"; else bad flatpak; fi
launch Sub
if [ "$(cat /out/sub 2>&1)" = sub ]; then ok "subdirectory file"; else bad subdir; fi
launch OnlyI3
if [ "$(cat /out/onlyi3 2>&1)" = i3 ]; then ok "OnlyShowIn=i3"; else bad onlyi3; fi
launch Tui
if [ "$(cat /out/tui 2>&1)" = tui ] && [ -f /out/term ]; then ok "Terminal=true uses the terminal"; else bad terminal; fi
launch ""
if [ ! -e /out/alpha ]; then ok "cancel launches nothing"; else bad cancel; fi
exit $fail
INNER
