#!/usr/bin/env bash
# Assert the boot splash is ours, installed where Kairos runs it.
#
#   test/boot-ux/check-splash.sh [IMAGE]
#
# splash/ is built into /usr/bin/kairos-splash. kairos-init keeps an existing
# binary at that path instead of linking its stock `kairos splash` there, and
# both its units and its dracut module exec that path. So the image must carry
# our binary, not the symlink, and it must run: the non-tty fallback prints the
# name, and --render draws one framebuffer frame without a framebuffer.
set -euo pipefail
IMAGE="${1:-sway-desktop:dev}"
fail=0
check() {
    local desc="$1"; shift
    if docker run --rm "$IMAGE" sh -c "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}

check "kairos-splash is our binary, not the stock symlink" \
    'test -x /usr/bin/kairos-splash && ! test -L /usr/bin/kairos-splash'
check "binary is the isotope splash" \
    'grep -aq "built on Hadron, by Kairos" /usr/bin/kairos-splash'
check "non-tty fallback prints ISOTOPE" '/usr/bin/kairos-splash | grep -q ISOTOPE'
check "renders a framebuffer frame" \
    'mkdir -p /run/t && /usr/bin/kairos-splash --render=/run/t/f.ppm --size=320x200 && test "$(stat -c %s /run/t/f.ppm)" -eq $((15 + 320 * 200 * 3))'
check "splash shows the release version" \
    'test -s /etc/kairos/branding/splash/version && /usr/bin/kairos-splash | grep -q "ISOTOPE v"'
check "booted splash unit enabled" \
    'test -L /etc/systemd/system/multi-user.target.wants/kairos-splash.service'
check "booted splash ordered before ly on tty1" \
    'grep -qx "Before=ly@tty1.service" /usr/lib/systemd/system/kairos-splash.service.d/10-isotope.conf'
# The splash this replaced must be gone entirely, or two animations fight
# over tty1.
check "no leftover isotope-splash unit or dracut module" \
    '! test -e /usr/lib/systemd/system/isotope-splash.service && ! test -e /usr/lib/dracut/modules.d/50isotope-splash && ! test -e /etc/dracut.conf.d/50-isotope-splash.conf'

exit "$fail"
