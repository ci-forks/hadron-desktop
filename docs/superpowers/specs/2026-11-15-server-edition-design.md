# Server edition

## Request

"We should also do a version which is not having any wm/display manager.
just like a server edition."

## Decisions

1. **Name and kind.** `server`, selected with `DESKTOP=server`. It is an
   edition, not a desktop variant: it is exempt from the AGENTS.md feature
   table, the way the agent image is, and AGENTS.md lists what it has and
   lacks.
2. **One line.** No full/vm split. One ISO and one upgrade image per
   release: `hadron-desktop-server-<tag>-amd64.iso` and `:server-<tag>`. The
   Makefile and release.yml build it as `GPU=full FIRMWARE=true` only to name
   that line in the existing scheme (`<flavor>-<tag>` is the full line); it
   has no Mesa. `/etc/hadron-desktop/release` says `FLAVOR=server GPU=full`.
3. **Its own Dockerfile target.** `FROM ${DESKTOP}-default AS default`: the
   four desktops alias `desktop-default` (the old `default` stage, unchanged),
   and `server-default` is a new stage that names no desktop stage. BuildKit
   builds only what the target reaches, so a server build compiles glib,
   NetworkManager, polkit and the Go tools, and nothing graphical. It is not
   an overlay that deletes desktop files.
4. **Contents.** Kept: the Hadron base (kernel, systemd, OpenSSH, sudo),
   NetworkManager and nmcli, polkit, the splash binary (for the initramfs
   passphrase prompt), the installer, hadron-update and hadron-ext, and the
   boot, installer and update files of `rootfs/`, named one by one in the
   `server-config` stage. Left out: every display server, compositor, login
   manager, Mesa and LLVM, fonts beyond the console font, GTK and Qt,
   Flatpak, PipeWire, BlueZ, wpa_supplicant, Distrobox, the tray and every
   `hadron-*` desktop script. Docker was left out at first and added later
   (decision 10).
5. **Firmware.** The firmware stage takes a second set when `DESKTOP=server`:
   no GPU, Wi-Fi, Bluetooth or audio firmware and no sof-bin; instead the
   firmware the kernel's Ethernet drivers declare (ice DDP, e100, r8169 and
   r8152, tg3, bnx2, bnx2x, qed, netxen/qlcnic, cxgb3/4, nfp, myri10ge, bna,
   tn40xx, typhoon, starfire, kaweth), and both microcodes. `--require`
   checks it against the kernel's `firmware=` names, as on the desktop.
   Left out: Mellanox Spectrum switch ASICs, Omni-Path, PCMCIA, storage HBAs.
   `sun/cassini.bin` is no longer in linux-firmware.
6. **Admin commands.** hadron-update and hadron-ext go through
   `hadron-updated` on the system bus. The polkit rule answers yes for the
   `admin` group and no for everyone else, with no interaction, so no polkit
   agent is needed: they work from an SSH session and from the console
   exactly as from a desktop. No sudo path is needed (unlike the agent).
   NetworkManager has no polkit support in this build, so the server sets
   `auth-polkit=root-only`: reading works for everyone, changes need `sudo`.
7. **Boot.** The splash stays in the initramfs (`51hadron-askpass`) and asks
   for the disk passphrase there; `hadron-unlock` and the console agent are
   unchanged. tty1 keeps its getty, so the console has a login prompt.
8. **Editions never cross.** `Classify` refuses a server image on a desktop
   (`ErrServerImage`) and a desktop image on a server (`ErrDesktopImage`),
   by label and by release-repo tag. Neither refusal can be overridden in
   advanced mode. `hadron-update switch` refuses on a server; desktops never
   list `server` in `switch` or the tray. The tray menu builder offers a
   server no "Switch desktop" and no other line.
9. **Installer.** Unchanged. It still puts the user in the desktop groups,
   so the server image creates them (an unknown group would stop the user
   from being created) and the harmless ly `save.ini` is still written.
   A follow-up could make the installer edition-aware.

10. **Docker.** Asked for after the first version, "useful to run other
    things outside the standard things available on the disk". The server
    takes the desktop's Docker unchanged: the static bundle (`docker` stage),
    `docker.service` and `docker.socket`, and `92_docker.yaml`, which adds
    `/var/lib/docker` to the persistent bind mounts and enables and starts
    dockerd only on the installed system. The image keeps it disabled,
    because the live ISO's `/var` is an overlayfs and Docker's overlay
    storage refuses a nested overlay. Docker 29 keeps its images in the
    containerd store (the `overlayfs` snapshotter) under `/var/lib/docker`,
    so they survive a reboot like the rest of that directory. The `docker`
    group already existed for the installer, which puts the user in it. The
    base image provides iptables and the kernel modules (overlay, bridge,
    br_netfilter, veth, nf_nat). No Distrobox: it is a desktop tool.

## Size (measured 2026-11-15)

| | server (this change, local build) | sway-vm v0.0.10 (published) |
|---|---|---|
| image, unpacked (`du -sx /`) | 509 MB | 1030 MB |
| image, compressed layers | 342 MB | 488 MB |
| ISO | 455 MB | 568 MB |

Docker (decision 10) adds 225 MB unpacked (the static binaries), 88 MB of
compressed layers and 86 MB to the ISO: 735 MB unpacked, 431 MB compressed
and a 541 MB ISO.

The Hadron base alone is 246 MB unpacked. The server carries 46 MB of
firmware (19 MB of it Intel microcode, which also goes into the initramfs
early cpio), which sway-vm does not. The server image builds in minutes with
a warm toolchain: its slowest stages are glib, NetworkManager and polkit.

## Follow-ups

- The installer still says "desktop install" and "desktop account", and
  writes ly's `save.ini`. Harmless; an edition-aware installer could drop both
  and give the user only the `admin` group.
- The first release that ships the server lists older releases (v0.0.10 and
  earlier) that have no `server-<tag>` image; `hadron-update` on a dev build
  offers them. An installed release only sees later ones, which do.
