# Boot UX checks

Fast, non-booting assertions for the splash, initramfs, branding and ISO
overlay. Run against an already-built image:

    make image
    test/boot-ux/run.sh sway-desktop:dev 'sway · wayland · kairos'
    test/boot-ux/run.sh i3-desktop:dev   'i3 · xlibre · kairos'

Or via make, which runs the suite for whichever variants are already built:

    make check-boot-ux

| Script | Asserts |
|---|---|
| `check-splash.sh` | `/usr/bin/kairos-splash` is our binary (not kairos-init's symlink), runs in both the non-tty and `--render` paths, shows the release, unit ordered before ly, old splash units gone |
| `check-initramfs.sh` | splash **and** release version baked into `/boot/initrd`, the initramfs copy identical to the image's, and it execs inside it |
| `check-branding.sh` | `vconsole.conf`, per-variant GRUB theme subtitle, `grubmenu.cfg` log level, release identity file, `hadron-update` |
| `check-iso-overlay.sh` | staged live `grub.cfg` is retitled, themed, quiet, and has no unrendered `{{...}}` placeholders |

These do not boot anything. The animation itself, and the themed GRUB menu, need
a VM:

    make iso && tools/vm.sh install   # live ISO: menu, then splash, then installer
    tools/vm.sh run                   # installed disk: menu, then splash, then ly

## Why the initramfs check matters

Two failure modes leave every other check passing while the feature silently
does nothing:

1. dracut's `check()` refusing the `50kairos-splash` module, so the initramfs
   has no splash at all.
2. BuildKit **caching** the kairos stage, shipping an older splash in the
   initramfs than in the image.

`check-initramfs.sh` inspects `/boot/initrd` itself, which is the only way to
catch either.
