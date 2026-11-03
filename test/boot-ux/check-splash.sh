#!/usr/bin/env bash
# Assert the boot splash is kairos-init's, wearing the hadron-desktop branding.
#
#   test/boot-ux/check-splash.sh [IMAGE]
#
# The animation is `kairos splash` (kairos-io/kairos internal/splash), installed
# by kairos-init as /usr/bin/kairos-splash. This repo only ships artwork in
# /etc/kairos/branding/splash and an ordering drop-in for ly. Run with stdout on
# a pipe, the splash takes its non-tty fallback: it prints the branding name and
# reports any artwork it rejected on stderr, then falls back to the stock Kairos
# art. So "prints HADRON DESKTOP and complains about nothing" proves the files
# are present AND parse.
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

B=/etc/kairos/branding/splash
check "kairos-splash present and executable" 'test -x /usr/bin/kairos-splash'
for f in wordmark tagline palette name; do
    check "branding file $f present" "test -s $B/$f"
done
check "non-tty fallback prints the hadron-desktop name" \
    '/usr/bin/kairos-splash 2>/dev/null | grep -q "HADRON DESKTOP"'
# Artwork the splash rejects is reported as "<file>: <reason>" on stderr while
# the stock Kairos artwork is painted instead -- a silent rebrand failure.
check "no branding file is rejected" \
    '! /usr/bin/kairos-splash 2>&1 >/dev/null | grep -Eq "(wordmark|palette|tagline|name):"'
check "booted splash unit enabled" \
    'test -L /etc/systemd/system/multi-user.target.wants/kairos-splash.service'
check "booted splash ordered before ly on tty1" \
    'grep -qx "Before=ly@tty1.service" /usr/lib/systemd/system/kairos-splash.service.d/10-hadron-desktop.conf'
# The vendored splash this replaced must be gone entirely, or two animations
# fight over tty1.
check "no leftover hadron-splash unit or dracut module" \
    '! test -e /usr/lib/systemd/system/hadron-splash.service && ! test -e /usr/lib/dracut/modules.d/50hadron-splash && ! test -e /etc/dracut.conf.d/50-hadron-splash.conf'

exit "$fail"
