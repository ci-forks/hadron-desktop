# Notes for agents working on hadron-desktop

## Every desktop variant has the same feature set

A variant is one value of `DESKTOP` (today sway, i3, cosmic, niri). The agent
image is an overlay on i3, not a variant, and is exempt where noted.

A user who switches variants must find the same Hadron features, reached the
same way. When you add a variant, or a feature that one variant gets, add it to
every variant in the same change, or say in the PR which variants are missing
it and why. A variant whose shell already provides a feature (COSMIC's panel,
Noctalia) uses that instead of a Hadron script, but the feature must exist.

The baseline:

| Feature | How the variants provide it today |
|---|---|
| Boot splash and branded login | `kairos-splash`; ly, or cosmic-greeter for COSMIC. `make check-boot-ux` asserts the subtitle per variant |
| Session identity | `/etc/hadron-desktop/session`, `/etc/issue`, `/etc/motd` in `rootfs-<variant>/`; `/etc/hadron-desktop/release` written in the Dockerfile `kairos` stage |
| Notifications server | mako (sway), dunst (i3), cosmic-notifications, Noctalia |
| Update tray icon (hadron-update) | the tray updater, `docs/superpowers/specs/2026-11-05-tray-updater-design.md`. Until it lands: `hadron-update notify` at session start. Not on agent |
| Upgrades only by `admin`, through polkit | polkit with the `admin` rule (sway and i3 gain it with the tray updater) |
| System tray that hosts StatusNotifierItem icons | waybar `tray`, i3bar + snixembed, COSMIC status area, Noctalia `tray` |
| First-login welcome | `hadron-welcome`; COSMIC uses cosmic-initial-setup |
| Keyboard habits (tiling variants) | Super+Enter terminal, Super+D launcher, Super+Shift+Q close, Super+Shift+E power menu, Super+F1 shortcut sheet (`hadron-help`) |
| Network, audio, power menus | `hadron-wifi-menu`, `hadron-audio-menu`, `hadron-power-menu`, or the shell's own panels |
| Audio | PipeWire and WirePlumber started with the session, media keys work |
| Screenshots and screen lock | a key binding for each. Known gaps: sway and i3 have no screen locker, i3 has no screenshot key |
| Flatpak apps with working file dialogs | flatpak, xdg-desktop-portal and a backend |

## Adding a variant

Besides the features above, a variant is wired into the build and release in
these places. Missing one ships a variant that builds locally but is never
released, never upgraded or never checked:

- `Makefile`: `VALID_DESKTOPS`, the help header, `check-boot-ux`.
- `Dockerfile`: its stages, the `DESKTOP` switch, the `kairos` stage.
- `rootfs-<variant>/` for its configuration.
- `.github/workflows/release.yml`: the build matrix, the allowed flavors and
  the expected asset counts, and the public-image check.
- `scripts/release-manifest.sh` and `update/release.go` (the flavor list).
- The tray updater's "Switch desktop" list.
- `README.md`: the variant list and the releases section.
- A design note in `docs/superpowers/specs/`.
