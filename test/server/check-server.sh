#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Check a built server edition image: what it must have, and the desktop
# parts it must not have.
#
#   test/server/check-server.sh [IMAGE]
set -euo pipefail
IMAGE="${1:-server-desktop:dev}"
fail=0
check() {
    local desc="$1"; shift
    if docker run --rm --entrypoint sh "$IMAGE" -c "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}
libs() { # DESC PATH: PATH runs its loader and every library resolves
    check "$1" "test -x $2 && ldd $2 >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
}

R=/etc/isotope/release
check "release file names the server line" \
    "grep -qx FLAVOR=server $R && grep -qx GPU=full $R"
check "the image is labelled as the server edition" \
    "true" # labels are checked from outside below
for l in flavor=server gpu=full firmware=true; do
    k="${l%%=*}" v="${l#*=}"
    got="$(docker image inspect --format "{{index .Config.Labels \"io.kairos.isotope.$k\"}}" "$IMAGE")"
    if [ "$got" = "$v" ]; then echo "PASS: label $k is $v"; else echo "FAIL: label $k is '$got', want $v"; fail=1; fi
done

# No desktop.
for b in sway Xorg X Xwayland niri cosmic-comp i3 ly greetd waybar foot flatpak pipewire wireplumber distrobox wpa_supplicant bluetoothd; do
    check "no $b" "! command -v $b && ! test -e /usr/bin/$b"
done
check "no Mesa or LLVM" "! ls /usr/lib/libGL* /usr/lib/libEGL* /usr/lib/libLLVM* /usr/lib/dri 2>/dev/null | grep -q ."
check "no GTK or Qt" "! ls /usr/lib/libgtk* /usr/lib/libQt* 2>/dev/null | grep -q ."
check "no fontconfig fonts" "! test -d /usr/share/fonts"
check "no desktop scripts" "! ls /usr/bin/isotope-menu /usr/bin/isotope-pick /usr/bin/start-desktop 2>/dev/null | grep -q ."
check "no ly unit or session" "! test -e /usr/lib/systemd/system/ly@.service && ! test -d /usr/share/wayland-sessions"
check "tty1 has a console login (getty not masked)" \
    "! test -L /etc/systemd/system/getty@tty1.service"

# What it keeps.
libs "NetworkManager finds its libraries" /usr/bin/NetworkManager
libs "nmcli finds its libraries" /usr/bin/nmcli
check "NetworkManager starts at boot" \
    "test -L /etc/systemd/system/multi-user.target.wants/NetworkManager.service"
check "network changes need root" \
    "grep -qx 'auth-polkit=root-only' /etc/NetworkManager/NetworkManager.conf"
check "sshd is installed" "test -x /usr/sbin/sshd || test -x /usr/bin/sshd"
libs "polkitd finds its libraries" /usr/lib/polkit-1/polkitd
check "polkit agent helper is setuid" "test -u /usr/lib/polkit-1/polkit-agent-helper-1"
check "the admin rule answers the update service" \
    "grep -q 'isInGroup(\"admin\")' /usr/share/polkit-1/rules.d/50-isotope-update.rules"
check "polkitd user exists" "getent passwd polkitd"
check "the update service is D-Bus activated" \
    "test -f /usr/share/dbus-1/system-services/io.kairos.IsotopeUpdate1.service && test -f /usr/lib/systemd/system/isotope-updated.service"
check "isotope runs, and isotope-update and isotope-ext link to it" \
    "isotope help | grep -q upgrade && test \"\$(readlink /usr/bin/isotope-update)\" = isotope && test \"\$(readlink /usr/bin/isotope-ext)\" = isotope"
check "isotope-recovery links to isotope" \
    "test \"\$(readlink /usr/bin/isotope-recovery)\" = isotope"
check "backups: restic runs and the timer is enabled" \
    "restic version | grep -q '^restic ' && test -L /etc/systemd/system/timers.target.wants/isotope-backup.timer && test -f /usr/lib/systemd/system/isotope-backup.service"
check "the installer's groups exist" \
    "for g in admin audio video render input bluetooth seat docker; do getent group \$g || exit 1; done"
check "the installer is wired" "grep -q /usr/bin/isotope-install-ui /system/oem/90_desktop_installer.yaml"
check "the passphrase prompt modules are in the initramfs" \
    "lsinitrd /boot/initrd | grep -q isotope-unlock && lsinitrd /boot/initrd | grep -q usr/libexec/isotope-ask-gate && lsinitrd /boot/initrd | grep -q usr/bin/kairos-splash"

# Docker, as on the desktop: disabled in the image (the live ISO's /var is an
# overlayfs), started by 92_docker.yaml on the installed system, with
# /var/lib/docker on the persistent partition.
for b in dockerd docker containerd containerd-shim-runc-v2 runc docker-init docker-proxy ctr; do
    check "$b is installed" "test -x /usr/bin/$b"
done
check "the docker CLI runs" "docker --version | grep -q 'Docker version'"
check "docker units are installed" \
    "test -f /usr/lib/systemd/system/docker.service && test -f /usr/lib/systemd/system/docker.socket"
check "the docker socket belongs to the docker group" \
    "grep -qx 'SocketGroup=docker' /usr/lib/systemd/system/docker.socket"
check "docker is not enabled in the image" \
    "! test -e /etc/systemd/system/multi-user.target.wants/docker.service && ! test -e /etc/systemd/system/sockets.target.wants/docker.socket"
check "the boot stage persists /var/lib/docker and starts docker after install" \
    "grep -q 'CUSTOM_BIND_MOUNTS: \"/var/lib/docker\"' /system/oem/92_docker.yaml && grep -q 'live_mode' /system/oem/92_docker.yaml && grep -q 'systemctl start docker.service' /system/oem/92_docker.yaml"
check "iptables is installed" "command -v iptables"
check "the container kernel modules are present" \
    "k=\$(ls /lib/modules) && for m in overlay bridge br_netfilter veth nf_nat xt_MASQUERADE; do find /lib/modules/\$k -name \"\$m.ko*\" | grep -q . || exit 1; done"

# Firmware: network cards and microcode, no desktop set.
F=/usr/lib/firmware
for f in intel/ice/ddp/ice.pkg rtl_nic/rtl8125b-2.fw tigon/tg3.bin bnx2x/bnx2x-e2-7.13.21.0.fw; do
    check "firmware $f" "test -e $F/$f || test -e $F/$f.zst"
done
check "Intel and AMD microcode" "ls $F/intel-ucode | grep -q . && test -s $F/amd-ucode/microcode_amd_fam19h.bin"
for d in amdgpu i915 nvidia intel/iwlwifi intel/sof intel/sof-ipc4 ath11k rtw89 brcm; do
    check "no $d firmware" "! test -e $F/$d"
done

exit "$fail"
