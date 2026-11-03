#!/usr/bin/env bash
# Assert the splash, with our branding, is baked into the image's initramfs.
#
#   test/boot-ux/check-initramfs.sh [IMAGE]
#
# kairos-init's 50kairos-splash dracut module copies /usr/bin/kairos-splash and
# every file in /etc/kairos/branding/splash into the initramfs. Two failure
# modes leave every image-side check green:
#   1. BuildKit serving a cached kairos stage, so the initramfs carries older
#      artwork than the image (the Makefile's --no-cache-filter guards this);
#   2. the module's check() refusing (no /usr/bin/kairos-splash), so the
#      initramfs has no splash at all.
# Both are caught here by looking inside /boot/initrd, the second by content.
#
# NOTE: the chroot-based exec check requires root INSIDE the container. It fails
# under `docker run --user` or on a rootless / userns-remapped daemon, where
# chroot(2) is not permitted. That is an environment limitation, not a
# regression in the image.
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

B=etc/kairos/branding/splash
check "initrd exists" "test -f /boot/initrd"
check "splash baked into the initramfs" \
    "lsinitrd /boot/initrd 2>/dev/null | grep -q 'usr/bin/kairos-splash'"
check "branding baked into the initramfs" \
    "lsinitrd /boot/initrd 2>/dev/null | grep -q '$B/wordmark'"
# .wants, not .requires: a failed splash must not be able to stall initrd.target.
check "initramfs unit activated via initrd.target.wants" \
    "lsinitrd /boot/initrd 2>/dev/null | grep -q 'initrd.target.wants/kairos-splash.service'"
check "initramfs unit NOT wired as a hard initrd.target.requires dependency" \
    "! lsinitrd /boot/initrd 2>/dev/null | grep -q 'initrd.target.requires/kairos-splash.service'"
check "immucore quiet drop-in baked into the initramfs" \
    "lsinitrd /boot/initrd 2>/dev/null | grep -q 'immucore.service.d/10-quiet.conf'"
check "no leftover hadron-splash in the initramfs" \
    "! lsinitrd /boot/initrd 2>/dev/null | grep -q 'hadron-splash'"
# Failure mode (1): stale artwork at the right path. Only content catches it.
# sha256sum, not cmp: the image has no diffutils, and a missing tool would read
# as a mismatch.
check "initramfs branding matches the image's (not a stale cached build)" \
    "mkdir -p /tmp/ird-sha && cd /tmp/ird-sha && lsinitrd --unpack /boot/initrd && \
     for f in wordmark tagline palette name; do \
       a=\$(sha256sum < /$B/\$f) && b=\$(sha256sum < /tmp/ird-sha/$B/\$f) && \
       [ \"\$a\" = \"\$b\" ] || exit 1; \
     done"
# Exec it inside the unpacked initramfs: proves the multi-call binary and its
# loader resolve there AND that the branding it reads there is ours.
check "splash execs inside the unpacked initramfs and paints our name" \
    "mkdir -p /tmp/ird && cd /tmp/ird && lsinitrd --unpack /boot/initrd && chroot . /usr/bin/kairos-splash 2>/dev/null | grep -q 'HADRON DESKTOP'"

exit "$fail"
