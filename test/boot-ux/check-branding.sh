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

# Release identity: hadron-update builds the upgrade image reference from it.
R=/etc/hadron-desktop/release
FLAVOR="${EXPECTED_FLAVOR:-${EXPECTED%% *}}"
check "release file names the '$FLAVOR' image line" "grep -qx 'FLAVOR=$FLAVOR' $R"
check "release file records the Hadron base" "grep -Eq '^HADRON_VERSION=v[0-9]' $R"
check "release file points at the published image repo" \
    "grep -qx 'IMAGE_REPO=ghcr.io/kairos-community/hadron-desktop' $R"
check "hadron-update is installed and runs" "hadron-update help | grep -q upgrade"

exit "$fail"
