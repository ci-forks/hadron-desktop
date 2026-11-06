#!/usr/bin/env bash
# Assert per-variant branding is correct in a built image.
#
#   test/boot-ux/check-branding.sh IMAGE EXPECTED_SUBTITLE
#
# e.g. test/boot-ux/check-branding.sh sway-desktop:dev 'sway · wayland · kairos'
#      test/boot-ux/check-branding.sh i3-desktop:dev   'i3 · xlibre · kairos'
#
# The subtitle check catches a real defect: theme.txt lives in the COMMON overlay
# but hardcoded the sway subtitle, so the i3 boot menu claimed to be sway.
set -euo pipefail
IMAGE="${1:-sway-desktop:dev}"
EXPECTED="${2:-sway · wayland · kairos}"
fail=0
check() {
    local desc="$1"; shift
    if docker run --rm "$IMAGE" sh -c "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}

T=/etc/kairos/branding/hadron-theme/theme.txt
check "vconsole.conf present"        'test -f /etc/vconsole.conf'
check "vconsole.conf sets a font"    'grep -q "^FONT=" /etc/vconsole.conf'
check "theme.txt present"            "test -f $T"
check "theme subtitle is '$EXPECTED'" "grep -qF '$EXPECTED' $T"
check "theme has no unsubstituted placeholder" "! grep -q '@VARIANT_SUBTITLE@' $T"

G=/etc/kairos/branding/grubmenu.cfg
check "grubmenu.cfg lowers the console log level" \
    "grep -qF 'loglevel=3 udev.log_level=3' $G"
# kairos-init's grub.cfg owns `splash quiet` and keeps recovery verbose on
# purpose; forcing them from extra_cmdline would put them on recovery too.
check "grubmenu.cfg leaves splash/quiet to kairos-init" \
    "! grep -E '^set extra_cmdline=.*(quiet|splash)' $G"
check "grubmenu.cfg preserves any pre-existing extra_cmdline" \
    "grep -q 'set extra_cmdline=\"\${extra_cmdline}' $G"
check "grubmenu.cfg sets the console palette (navy, not black)" \
    "grep -q '^set hadron_vt_palette=\"vt.default_red=0x1a,' $G && grep -q 'extra_cmdline}.*\${hadron_vt_palette}' $G"

# The installed EFI GRUB's $prefix is (COS_STATE)/grub2. A command.lst in
# $prefix/<platform> makes rmmod a real command, and /etc/cos/grub.cfg's
# `rmmod tpm` then prints "no such module" on every boot. hadron-grub-theme must
# put the theme modules elsewhere, without a command.lst (GRUB also reads it
# when grubmenu.cfg sets $prefix), and remove the tree older releases left at
# $prefix, keeping the modules kairos-agent put there.
S=/tmp/hadron-grub-state
P="\$(cd /usr/lib/grub && ls -d *-efi | head -1)"
check "hadron-grub-theme keeps its modules out of the EFI \$prefix" \
    "p=$P && mkdir -p $S/grub2/\$p && touch $S/grub2/\$p/command.lst $S/grub2/\$p/gfxmenu.mod $S/grub2/\$p/loopback.mod && \
     ! /usr/libexec/hadron-grub-theme --check $S && /usr/libexec/hadron-grub-theme $S && \
     /usr/libexec/hadron-grub-theme --check $S && \
     test ! -e $S/grub2/\$p/command.lst -a ! -e $S/grub2/\$p/gfxmenu.mod -a -e $S/grub2/\$p/loopback.mod && \
     test -f $S/hadron/grub2/\$p/tga.mod -a -f $S/hadron/grub2/\$p/moddep.lst -a -f $S/themes/hadron/theme.txt && \
     test ! -e $S/hadron/grub2/\$p/command.lst && \
     [ \"\$(md5sum < $G)\" = \"\$(md5sum < $S/grubmenu)\" ]"
check "grubmenu.cfg loads the theme modules from \$prefix first" \
    "grep -q 'if \[ -f \"\${prefix}/\${grub_cpu}-\${grub_platform}/tga.mod\" \]' $G && \
     grep -q 'set prefix=\"(\${menu_blk})/hadron/grub2\"' $G && ! grep -q 'set prefix=(\${menu_blk})/grub2' $G"
check "grubmenu.cfg leaves the font to loadfont unicode" "! grep -q '^ *loadfont' $G"

# The Hadron wallpapers are drawn at build time; every variant points at these.
WP=/usr/share/backgrounds/hadron
for w in hadron-orbits-night-3840x2160 hadron-plain-night-1920x1080; do
    check "wallpaper $w.png is a PNG" "[ \"\$(head -c 4 $WP/$w.png | tail -c 3)\" = PNG ]"
done
case "${EXPECTED%% *}" in niri|cosmic)
    check "the day wallpaper is a PNG" "[ \"\$(head -c 4 $WP/hadron-orbits-day-3840x2160.png | tail -c 3)\" = PNG ]" ;;
esac
# ly runs on the 16-colour console: no animation, the console's own colours.
if [ "${EXPECTED%% *}" != cosmic ]; then
    check "ly has no animation" "grep -qx 'animation = none' /etc/ly/config.ini"
    check "ly uses the console palette (eight-colour mode)" "grep -qx 'full_color = false' /etc/ly/config.ini"
    check "no third-party ly animation file ships" "! ls /etc/ly/*.dur 2>/dev/null | grep -q ."
fi

# Release identity: hadron-update builds the upgrade image reference from it.
R=/etc/hadron-desktop/release
FLAVOR="${EXPECTED_FLAVOR:-${EXPECTED%% *}}"
check "release file names the '$FLAVOR' image line" "grep -qx 'FLAVOR=$FLAVOR' $R"
check "release file records the Hadron base" "grep -Eq '^HADRON_VERSION=v[0-9]' $R"
check "release file points at the published image repo" \
    "grep -qx 'IMAGE_REPO=ghcr.io/kairos-community/hadron-desktop' $R"
check "hadron-update is installed and runs" "hadron-update help | grep -q upgrade"
# Screen lockers run as the user; pam_unix needs this setuid helper to check
# the password, or nobody can unlock.
check "unix_chkpwd is setuid (screen unlock)" "test -u /usr/sbin/unix_chkpwd"
# Sway and i3 ship their own locker and screenshot tools. Without its PAM
# service a locker falls back to `other`, which denies every password.
if [ "$FLAVOR" = sway ]; then
    check "swaylock finds all its libraries in the image" "test -x /usr/bin/swaylock && ! ldd /usr/bin/swaylock 2>&1 | grep -q 'not found'"
    check "swaylock has its PAM service" "grep -Eq '^auth[[:space:]]+include[[:space:]]+login' /etc/pam.d/swaylock"
fi
if [ "$FLAVOR" = i3 ]; then
    for b in i3lock xss-lock shotgun hacksaw xclip; do
        check "$b finds all its libraries in the image" "test -x /usr/bin/$b && ! ldd /usr/bin/$b 2>&1 | grep -q 'not found'"
    done
    check "i3lock has its PAM service" "grep -Eq '^auth[[:space:]]+include[[:space:]]+login' /etc/pam.d/i3lock"
fi
if [ "$FLAVOR" = cosmic ]; then
    # The COSMIC password prompt (cosmic-osd) execs polkit's helper at this
    # path; without it every privileged Settings change fails silently.
    check "polkit agent helper at the path cosmic-osd runs, setuid" \
        "test -u /usr/libexec/polkit-agent-helper-1"
    check "locale -a lists UTF-8 locales (the setup wizard's language list)" \
        "locale -a | grep -qx de_DE.UTF-8"
    check "hicolor icon theme index present" "test -f /usr/share/icons/hicolor/index.theme"
fi
if [ "$FLAVOR" = niri ]; then
    # A config error makes niri fall back to an empty default with no shell.
    check "the shipped niri config validates" "niri validate -c /etc/niri/config.kdl"
    check "niri config starts Noctalia" "grep -qx 'spawn-at-startup \"hadron-noctalia\"' /etc/niri/config.kdl"
    # Noctalia's polkit agent is off unless configured; without it privileged
    # requests fail instead of asking for the password.
    check "Hadron's Noctalia defaults turn the polkit agent on" \
        "grep -qx 'polkit_agent = true' /usr/share/hadron-desktop/noctalia-defaults.toml && test -x /usr/bin/hadron-noctalia"
    check "ly offers the niri session" "grep -q '^Exec=niri-session' /usr/share/wayland-sessions/niri.desktop"
    # The build stages check ldd against their own build root; this checks
    # the image, where a library a stage built but the rootfs never copied
    # is missing.
    for b in niri noctalia xwayland-satellite; do
        check "$b finds all its libraries in the image" "! ldd /usr/bin/$b 2>&1 | grep -q 'not found'"
    done
    check "Noctalia runs" "noctalia --version"
    # Noctalia refuses its asset root without these four files.
    check "Noctalia's runtime assets are installed" \
        "cd /usr/share/noctalia/assets && test -f emoji.json -a -f fonts/noctalia-tabler.ttf -a -f templates/builtin.toml -a -f translations/en.json"
    check "xwayland-satellite is on PATH (niri starts it for X11 apps)" "command -v xwayland-satellite"
    # foot's default TERM=foot has no terminfo in the image; less (the
    # Super+F1 cheat sheet), vim and htop then run in dumb-terminal mode.
    check "foot uses a TERM the image has terminfo for" "grep -qx 'term=xterm-256color' /etc/xdg/foot/foot.ini"
    check "a default cursor theme is installed" "test -e /usr/share/icons/default/index.theme -a -e /usr/share/icons/Adwaita/cursors/default"
    # Noctalia's polkit agent checks the password through this helper.
    check "polkit agent helper is setuid" "test -u /usr/lib/polkit-1/polkit-agent-helper-1"
    check "the GTK portal backend is installed (Flatpak file dialogs)" \
        "test -f /usr/share/xdg-desktop-portal/portals/gtk.portal"
fi

# Flatpak portals: file dialogs on every variant, and the dark colour scheme
# through the Settings interface. xdg-desktop-portal reads the backends for a
# session from <XDG_CURRENT_DESKTOP>-portals.conf. The agent image is i3.
case "$FLAVOR" in
    sway)     desktop=sway;   backends="gtk wlr" ;;
    i3|agent) desktop=i3;     backends="gtk" ;;
    niri)     desktop=niri;   backends="gtk" ;;
    cosmic)   desktop=cosmic; backends="cosmic" ;;
    *)        desktop="";     backends="" ;;
esac
if [ -n "$desktop" ]; then
    P=/usr/share/xdg-desktop-portal
    check "$desktop-portals.conf is installed" "test -f $P/$desktop-portals.conf"
    for b in xdg-desktop-portal $(printf 'xdg-desktop-portal-%s ' $backends); do
        # meson installs them in libexecdir: lib here, libexec for COSMIC's.
        check "$b is installed and finds all its libraries in the image" \
            "for f in /usr/lib/$b /usr/libexec/$b; do [ -x \$f ] && break; done && [ -x \$f ] && ! ldd \$f 2>&1 | grep -q 'not found'"
    done
    for b in $backends; do
        check "$desktop-portals.conf picks the $b backend" "grep -Eq '=([^#]*;)?$b(;|\$)' $P/$desktop-portals.conf"
        check "the $b backend is registered" "test -f $P/portals/$b.portal"
    done
    # The backend that serves org.freedesktop.appearance color-scheme.
    settings=${backends%% *}
    check "the $settings backend implements Settings (the colour scheme)" \
        "grep -q 'org.freedesktop.impl.portal.Settings' $P/portals/$settings.portal"
    if [ "$settings" = gtk ]; then
        # xdg-desktop-portal-gtk reads the colour scheme from GSettings.
        check "Hadron's dark GSettings override is installed" \
            "grep -qx \"color-scheme='prefer-dark'\" /usr/share/glib-2.0/schemas/90_hadron.gschema.override"
        check "the GSettings schemas are compiled" "test -f /usr/share/glib-2.0/schemas/gschemas.compiled"
        check "GSettings reports the dark colour scheme" \
            "[ \"\$(gsettings get org.gnome.desktop.interface color-scheme 2>/dev/null)\" = \"'prefer-dark'\" ]"
        check "the Adwaita-dark GTK3 theme the override names exists" \
            "test -f /usr/share/themes/Adwaita-dark/gtk-3.0/gtk.css"
    fi
fi
if [ "$desktop" = sway ]; then
    check "Sway pushes WAYLAND_DISPLAY to D-Bus activation (portals)" \
        "grep -q '^exec dbus-update-activation-environment --systemd WAYLAND_DISPLAY' /etc/sway/config.d/50-portals.conf"
    check "the Sway session sets XDG_CURRENT_DESKTOP=sway" "grep -qx 'export XDG_CURRENT_DESKTOP=sway' /etc/sway-desktop/env"
fi
if [ "$desktop" = i3 ]; then
    check "i3 pushes DISPLAY to D-Bus activation (portals)" \
        "grep -q '^exec --no-startup-id dbus-update-activation-environment --systemd DISPLAY' /etc/i3/config.d/20-portals.conf"
    check "the i3 session sets XDG_CURRENT_DESKTOP=i3" "grep -qx 'export XDG_CURRENT_DESKTOP=i3' /etc/i3-desktop/env"
    check "the i3 GTK3 has the X11 backend (the portal's file chooser)" "test -f /usr/lib/pkgconfig/gtk+-x11-3.0.pc"
fi

exit "$fail"
