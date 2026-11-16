# Notes for agents working on isotope

## Names: Isotope, built on Hadron

The project is **Isotope**: the binaries are `isotope-*`, the paths
`/etc/isotope` and `/usr/share/isotope`, the labels `io.kairos.isotope.*`, the
D-Bus name `io.kairos.IsotopeUpdate1`. **Hadron** is only the base distro this
is built on (`ghcr.io/kairos-io/hadron`, `HADRON_VERSION`, the Hadron musl
toolchain, hadron-layers). Do not call a feature of this repo "Hadron". The
naming rules (short form "Isotope, built on Hadron", editions as a suffix,
"Isotope Server") are in the Kairos design system, github.com/kairos-io/design.

The user command is `isotope` (`update/`, installed as `/usr/bin/isotope`):
one binary with a subcommand per job (`upgrade`, `ext`, `recovery`,
`backup`, `restore`, ...). Add a new user-facing command as a subcommand
there, not as a new `isotope-*` binary. `isotope-update`, `isotope-ext` and
`isotope-recovery` are links to it and must keep working: the sessions start
`isotope-update tray`, the menus test for `isotope-ext` and
`isotope-recovery`, and installed systems and people use the old names.

Isotope was called Hadron Desktop. These keep systems installed before the
rename updating; do not remove them without a plan for those systems:

- the `io.kairos.hadron-desktop.*` labels next to the new ones (Dockerfile,
  Dockerfile.agent, the UKI image in release.yml), and `label()` in
  update/policy.go, which reads either;
- `LEGACY_IMAGE_REPO` in release.yml, which pushes every upgrade image to
  `ghcr.io/kairos-community/hadron-desktop` too;
- `rootfs/system/oem/08_isotope_rename.yaml` (the installer's settings under
  `/etc/hadron-desktop`, the updater's state), the stamps under
  `~/.local/state/hadron*` read by isotope-welcome, isotope-first-run-apps and
  isotope-night-light, `hadron.unlock` and `hadron.splash.agent` on the kernel
  command line, `themes/hadron` and `hadron/grub2` removed from COS_STATE by
  isotope-grub-theme;
- on the agent: the `hadron_agent` cloud-config key, and the move of
  `/usr/local/.state/var-lib-hadron-agent.bind` in
  isotope-agent-provision.service.

## Every desktop variant has the same feature set

A variant is one value of `DESKTOP` (today sway, i3, cosmic, niri). The agent
image is an overlay on i3, not a variant, and is exempt where noted. The
server edition (`DESKTOP=server`) is not a variant either and is exempt from
the whole table below: it has no desktop. See "The server edition".

A user who switches variants must find the same Isotope features, reached the
same way. When you add a variant, or a feature that one variant gets, add it to
every variant in the same change, or say in the PR which variants are missing
it and why. A variant whose shell already provides a feature (COSMIC's panel,
Noctalia) uses that instead of an Isotope script, but the feature must exist.

The baseline:

| Feature | How the variants provide it today |
|---|---|
| Boot splash and branded login | `kairos-splash`; ly, or cosmic-greeter for COSMIC. `make check-boot-ux` asserts the subtitle per variant. In the initramfs the splash is also the password agent that asks for the disk passphrase (`rootfs/usr/lib/dracut/modules.d/51isotope-askpass/`, `splash/README.md`) |
| Session identity | `/etc/isotope/session`, `/etc/issue`, `/etc/motd` in `rootfs-<variant>/`; `/etc/isotope/release` written in the Dockerfile `kairos` stage |
| Notifications server | mako (sway), dunst (i3), cosmic-notifications, Noctalia |
| Update tray icon (isotope-update) | `isotope-update tray` at session start, talking to the root `isotope-updated` daemon over D-Bus; see `docs/superpowers/specs/2026-11-05-tray-updater-design.md`. Not on agent, which keeps `isotope-update upgrade` through sudo |
| Upgrades only by `admin`, through polkit | polkit with the `admin` rule (`50-isotope-update.rules`) on all four variants. Agent removes the update service and its polkit files |
| System tray that hosts StatusNotifierItem icons | waybar `tray`, i3bar + snixembed (started from `config.d/30-update-tray.conf`), COSMIC status area, Noctalia `tray` |
| First-login welcome | `isotope-welcome`, a notification that opens the shortcut sheet when clicked; COSMIC uses cosmic-initial-setup |
| First-run app picker (a browser and essentials from Flathub) | `isotope-first-run-apps --login` at session start: `exec` in the sway and i3 configs, `spawn-at-startup` on niri, `/etc/xdg/autostart/isotope-first-run-apps.desktop` on COSMIC (it waits for cosmic-initial-setup). Once per user (stamp in `~/.local/state/isotope/`), after `nm-online`; "Install apps" in `isotope-menu` reopens it. `test/first-run/check-first-run-apps.sh`. Not on agent, which removes the script and its i3 line |
| Keyboard habits (tiling variants) | Super+Enter terminal, Super+D launcher (`isotope-apps` on i3, which lists .desktop and Flatpak apps; Super+Shift+D keeps `dmenu_run`), Super+Shift+Q close, Super+Shift+E power menu, Super+F1 shortcut sheet (`isotope-help`) |
| Notification keys | Super+BackSpace closes the newest notification, Super+Shift+BackSpace closes all, Super+Ctrl+BackSpace shows the last closed (the history panel on niri), Super+Alt+BackSpace turns do not disturb on and off. `isotope-notify-key` sends them to mako, dunst or Noctalia. COSMIC binds only do not disturb, because its notification server has no command interface; its notifications close with a click |
| Network, audio, power menus | `isotope-wifi-menu`, `isotope-audio-menu`, `isotope-power-menu`, or the shell's own panels |
| Audio | PipeWire and WirePlumber started with the session, media keys work |
| Screenshots and screen lock | a key binding for each. Lockers: swaylock (sway, niri), i3lock (i3), COSMIC's own; sway (swayidle) and i3 (xss-lock) also lock on idle and before suspend. i3 takes screenshots with `isotope-screenshot`. The agent overlay starts no locker |
| Flatpak apps with working file dialogs and dark mode | flatpak, xdg-desktop-portal and a backend reporting `color-scheme`: the GTK backend with its Settings interface (plus xdg-desktop-portal-wlr on sway) on sway, i3 and niri, reading the `prefer-dark` override in gsettings-desktop-schemas; COSMIC's own portal |
| Hardware keys | volume, mute, brightness, play/pause/next, with an on-screen level. `isotope-media-key` on sway and i3 (wpctl, logind `SetBrightness`, MPRIS, and a mako or dunst progress pop-up); Noctalia and COSMIC (cosmic-settings-daemon, cosmic-osd) handle the keys themselves, except that COSMIC's playback keys call `isotope-media-key` because Hadron has no playerctl |
| Isotope menu on Super+Escape | one menu for apps, Wi-Fi, Bluetooth, audio, capture, clipboard history, update, extensions, backup, recovery key (on an encrypted disk), help and power: `isotope-menu` through `isotope-pick` (fuzzel on sway, niri and COSMIC; dmenu on i3). Each entry opens what the variant uses: the `isotope-*` menus, Noctalia's panels, COSMIC Settings and cosmic-osd. COSMIC's own Super+Escape lock moves to Super+Alt+L, niri's shortcut-inhibit toggle to Super+Shift+Escape |
| Clipboard history on Super+Ctrl+V | `isotope-clipboard`, also in the Isotope menu: the last 200 copies, kept in memory for the session, without the copies a password manager marks `x-kde-passwordManagerHint`. cliphist fed by `wl-paste --watch` on sway and COSMIC (cosmic-comp offers data-control to unsandboxed clients, no setting), clipnotify + xsel on i3 (text only), Noctalia's clipboard panel on niri. Super+V stays split (sway, i3) and float (niri). Not on agent: an AI-driven appliance must not keep the secrets it copies; Dockerfile.agent removes it |
| Bluetooth menu | pair, connect, disconnect, forget, power. `isotope-bluetooth-menu` (bluetoothctl through `isotope-pick`) on sway and i3, Super+Shift+B and the waybar module; the Noctalia control center and COSMIC's Bluetooth applet |
| Keyboard layout and timezone from the installer | the installer's cloud-config writes `/etc/isotope/keyboard`, `/etc/X11/xorg.conf.d/00-keyboard.conf`, the `KEYMAP` in `/etc/vconsole.conf` and the `/etc/localtime` link in an initramfs stage, so every boot gets them again. sway (`start-desktop`), niri (`niri-session`) and COSMIC (`start-cosmic`, greetd) read the layout as `XKB_DEFAULT_LAYOUT`/`XKB_DEFAULT_VARIANT` from `/etc/profile.d/isotope-keyboard.sh`; i3's XLibre reads the xorg.conf.d file. Hadron has no localed, so `localectl` does not work |
| Saved-password store | gnome-keyring (Secret Service and its portal) on all four variants, so Electron and Chromium Flatpaks do not store secrets in plain text. `pam_gnome_keyring` in ly's and greetd's PAM services unlocks the login keyring with the login password; `gnome-keyring-daemon.socket` is enabled for every user. The agent removes it (autologin, nothing can unlock it) |
| Night light | a warmer screen (4500 K) from 20:00 to 07:00, at fixed times because Hadron has no location. Super+Alt+N and the Isotope menu's "Night light" run `isotope-night-light toggle`, which keeps the setting in `$XDG_STATE_HOME/isotope/night-light` and restores it at session start: wlsunset on sway, xsct in a one-minute loop on i3, Noctalia's night light on niri (`noctalia msg nightlight-enable`). Not on COSMIC: cosmic-comp has no gamma control protocol and COSMIC Settings has its night light page disabled (epoch 1.11.0). `test/night-light/check-night-light.sh` checks the script |
| Emoji | Noto Color Emoji (the Dockerfile `emoji` stage; FreeType is built with libpng to load its PNG glyphs) as the last fallback in `60-isotope.conf`. `isotope-emoji` on Super+; and in the Isotope menu: the list `/usr/share/isotope/emoji.txt`, made from Unicode's `emoji-test.txt`, through `isotope-pick`, then wl-copy (sway, niri, COSMIC) or xsel (i3) and a notification. COSMIC's Super+; is added in the Dockerfile. `test/emoji/check-emoji.sh` |
| System extensions | `isotope ext` (also the `isotope-ext` link): browse the catalogs in `/etc/isotope/extensions.conf` (default hadron-layers), install, enable, disable, remove. The root `isotope-updated` runs `kairos-agent sysext` (`--common`, enabled `--now`) behind polkit's `io.kairos.isotope-update.extensions` (admin only, no Flatpak callers), shares the update job slot, and refuses an extension whose files differ from the running `/usr` (systemd-dissect `--mtree`) unless `--force`. The "Extensions" entry of `isotope-menu` (isotope-pick, ✓ on the installed ones) and the tray's "Extensions…" open the same list on every variant. `extensions.ignore_signatures` is set in `/etc/kairos/90_isotope_extensions.yaml` and `/system/oem/93_isotope_extensions.yaml`, both removed on a Trusted Boot image. See `docs/superpowers/specs/2026-11-07-extensions-design.md`; `test/extensions/check-extensions-menu.sh`. Not on agent: Dockerfile.agent removes `isotope-ext`, its config and the signature setting |
| Disk encryption from the installer (opt-in) | the installer's "Encrypt your files?" step, on every machine and off by default: `install.encrypted_partitions: [COS_PERSISTENT]` with kcrypt `method: passphrase` (LUKS2, argon2id, label `COS_PERSISTENT_LUKS`, subsystem `kcrypt:passphrase`); the passphrase reaches kcrypt only through a 0600 file in `/run` that the installer deletes, never through `/oem`. Asked for at every boot by `isotope-unlock` in the initramfs (#60). Marker `/etc/isotope/encryption` (`mode=passphrase`). The same installer for every variant; see `docs/superpowers/specs/2026-11-07-encrypt-home-design.md`. After the passphrase, "Also create a recovery key?" (off by default) adds a recovery key keyslot to that container, unlocked with the same `/run` passphrase file; the boot prompt (`isotope-unlock`, the splash) then says "Enter the disk passphrase or recovery key" and takes either. On a Trusted Boot medium (`/run/cos/uki_install_mode`) the passphrase is never offered: the disk stays TPM-only (PCR-sealed, one keyslot), and an opt-in "Create a recovery key?" step (off by default) adds a recovery key to COS_OEM_LUKS and COS_PERSISTENT_LUKS, unlocked through the TPM token; kcrypt (kairos-io/kairos#6024) asks "Enter the recovery key for this computer's disk" when the TPM refuses, from the key's token, since the config is in the encrypted OEM; the cloud-config sets `kcrypt.passphrase_fallback` and marker `mode=tpm-recovery-key` only then. A recovery key is systemd's format (crypto/rand, 256 bits, 8 groups of 8 modhex), shown once in the console font two groups per line, confirmed by typing one group back, stored as a PBKDF2 (1000 iterations) keyslot with a `systemd-recovery` token, and never written to `/oem` (0600 files in `/run`, deleted). `isotope-recovery` (a link to `isotope-update`; `status`, `new-key`, Trusted Boot `reseal`) runs through `isotope-updated` behind polkit's `io.kairos.isotope-update.recovery` (admin only, no Flatpak callers), with the secrets passed as a pipe fd, never as a D-Bus string; the "Recovery key" entry of `isotope-menu` and the tray's "Recovery key…" open it in a terminal on an encrypted disk. See `docs/superpowers/specs/2026-11-07-trusted-boot-edition-design.md`. Not on agent, whose installer runs `isotope-install-ui --progress-only` without the wizard, and which removes `isotope-recovery` |
| Backup and restore | `isotope backup` and `isotope restore` (restic, built in the Dockerfile `restic` stage), through sudo for the `admin` group. A backup is what survives a reboot: `/usr/local` (the persistent partition; `/home` is `.state/home.bind`) and `/oem`, without home caches, `/var/log`, Flatpak apps (a manifest lists them; a restore reinstalls them) and, unless `--with-containers`, Docker's and Podman's storage. A random password in the recovery key format, shown once, stored in `/usr/local/isotope-backup/password` (not `/var/lib`: Kairos rebuilds `/var` at every boot). `isotope backup --export FILE` writes one file (a tar of a restic repository). `isotope restore` brings the whole machine back (refused when the disk encryption differs) and restarts; `--only homes,apps,extensions` restores into the local users instead. `isotope-backup.timer` (enabled in the image, also on the server) runs `isotope backup --scheduled`, which does nothing until `isotope backup schedule daily|weekly` and skips an unplugged disk. "Backup" in `isotope-menu` when restic is there. See `docs/superpowers/specs/2026-11-16-isotope-cli-backup-design.md`. Not on agent: Dockerfile.agent removes restic and the timer |

## The server edition

`DESKTOP=server` builds a headless image: the Dockerfile's `server-default`
stage, which takes no desktop stage, so none of them is built. One image
line, released as `isotope-server-<tag>-amd64.iso` and `:server-<tag>`
(the Makefile and release.yml build it as `GPU=full FIRMWARE=true` only to
name that line).

| It has | It does not have |
|---|---|
| kernel, network card firmware (the Ethernet drivers' set in the `firmware` stage, checked by `--require`) and CPU microcode | GPU, Wi-Fi, Bluetooth and audio firmware, SOF |
| NetworkManager and nmcli; changes need root (`auth-polkit=root-only`) | wpa_supplicant, Wi-Fi |
| OpenSSH server, a console login on tty1 | ly, greetd, a display server, a compositor, Mesa, LLVM |
| the Isotope installer, both encryption modes, the splash's passphrase prompt in the initramfs | fonts beyond the console font, GTK, Qt, Flatpak, PipeWire, Distrobox |
| `isotope` (and its `isotope-update`, `isotope-ext`, `isotope-recovery` links) through `isotope-updated` and polkit (the admin rule needs no agent, so they work over SSH); `isotope backup` and `isotope restore` through sudo, with restic and the backup timer | the tray, the Isotope menu and every `isotope-*` desktop script |
| Docker, as on the desktop: the static bundle, `docker.service` and `docker.socket`, and `92_docker.yaml`, which keeps `/var/lib/docker` on the persistent partition and starts dockerd only on the installed system | |

Its configuration is `rootfs-server/` plus the files the `server-config`
stage names one by one from `rootfs/`. A new file in `rootfs/` that the server
needs (boot, installer, update service, polkit) must be added there too. A
server installs only server images and a desktop never installs one
(`Classify` in update/policy.go); `isotope switch` refuses on a server.
`test/server/check-server.sh` checks a built image.

## The shortcut sheet is generated

`isotope-help` shows `rootfs-<variant>/usr/share/isotope/shortcuts.txt`, which
`tools/gen-shortcuts.py` writes from the key bindings in the sway, i3 and niri
configs. A binding appears on the sheet only when a `# help: ...` comment
(`// help:` in niri) is on the line above it; see the script's docstring for
`help+:`, `help-section:` and `help-row:`. After you change a binding or its
comment, run `python3 tools/gen-shortcuts.py` and commit the result; CI fails
on a stale sheet. COSMIC's list is hand-kept in `tools/shortcuts-cosmic.txt`.

## Adding a variant

Besides the features above, a variant is wired into the build and release in
these places. Missing one ships a variant that builds locally but is never
released, never upgraded or never checked:

- `Makefile`: `VALID_DESKTOPS`, the help header, `check-boot-ux`.
- `Dockerfile`: its stages, the `DESKTOP` switch, the `kairos` stage.
- `rootfs-<variant>/` for its configuration.
- `.github/workflows/release.yml`: the build matrix (every variant is built
  in both GPU lines, full and vm), the allowed flavors and the expected asset
  counts (two ISOs per variant), the manifest's flavor/line pairs, and the
  public-image check (`<variant>-<tag>` and `<variant>-vm-<tag>`).
- `scripts/release-manifest.sh` (the flavor/line pairs) and `update/release.go`
  (the flavor list).
- The tray updater's "Switch desktop" list.
- `README.md`: the desktop list and the download table;
  `docs/DEVELOPMENT.md`: the variant list and the releases section.
- A design note in `docs/superpowers/specs/`.
