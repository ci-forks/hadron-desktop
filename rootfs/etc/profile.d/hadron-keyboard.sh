# Keyboard layout chosen in the installer, for the Wayland compositors.
#
# The installer's cloud-config writes /etc/hadron-desktop/keyboard at every
# boot (XKB_LAYOUT= and XKB_VARIANT= lines). sway, niri and cosmic-comp give
# libxkbcommon an empty layout when their own configuration names none, and
# libxkbcommon then reads XKB_DEFAULT_LAYOUT and XKB_DEFAULT_VARIANT. Hadron
# has no systemd-localed to ask instead. The i3 variant's X server reads
# /etc/X11/xorg.conf.d/00-keyboard.conf, written by the same stage.
#
# Sourced by login shells: ly's session setup, niri-session and start-cosmic
# start one; sway's start-desktop sources this file itself. A value that is
# already set (from ~/.profile, say) wins, and the file is only read, never
# run, so a hand-edited line cannot execute anything.
if [ -z "${XKB_DEFAULT_LAYOUT:-}" ] && [ -r /etc/hadron-desktop/keyboard ]; then
    while IFS='=' read -r _hk_key _hk_value; do
        case "$_hk_key" in
            XKB_LAYOUT) _hk_layout=$_hk_value ;;
            XKB_VARIANT) _hk_variant=$_hk_value ;;
        esac
    done < /etc/hadron-desktop/keyboard
    if [ -n "${_hk_layout:-}" ]; then
        export XKB_DEFAULT_LAYOUT="$_hk_layout"
        if [ -n "${_hk_variant:-}" ]; then
            export XKB_DEFAULT_VARIANT="$_hk_variant"
        fi
    fi
    unset _hk_key _hk_value _hk_layout _hk_variant
fi
