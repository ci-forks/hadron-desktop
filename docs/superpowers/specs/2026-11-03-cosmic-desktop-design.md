# COSMIC desktop variant

## Intent

Sway and i3 serve people who already know a tiling window manager. The COSMIC
variant is for someone **new to Linux**: it must be very simple to use. They
log in on a graphical screen, find a familiar panel and dock, install apps
from a store by clicking, change settings in a settings app, and get told when
an update is ready. Nothing in the normal path should need a terminal.

## Goals

- `DESKTOP=cosmic` builds a third variant from the same Dockerfile, on the
  same pinned Hadron release, from source against the Hadron musl toolchain.
- COSMIC Epoch 1.11.0 (`epoch-1.11.0` tags), all components on one epoch.
- Graphical login (cosmic-greeter on greetd), not the ly TUI.
- First login runs COSMIC's own welcome (cosmic-initial-setup): appearance,
  panel/dock layout, shortcuts, Wi-Fi.
- Apps from Flathub through COSMIC Store (Flatpak is already in the image).
- Settings panels that need privileges work (date/time, language, users),
  which means polkit and its password prompt.
- Flatpak apps that only speak X11 still run (Xwayland), and Flatpak file
  dialogs work (xdg-desktop-portal + xdg-desktop-portal-cosmic).
- `hadron-update notify` works unchanged: cosmic-notifications serves
  org.freedesktop.Notifications.

## Non-goals (this first variant)

- cosmic-player (needs GStreamer), geoclue (automatic time zone), playerctl,
  printing, a preinstalled browser. Each is a follow-up.
- Changing the sway or i3 variants.
- Trusted boot / UKI.

## Anti-goals

- Shipping a desktop that boots but where the store, settings or file dialogs
  silently fail. A newcomer cannot debug that; it is worse than not shipping.
- Mixing Alpine-built shared libraries into the image. Alpine is used only for
  the Rust toolchain, as the agent overlay already does.

## Constraints and decisions

- **Toolchain.** Rust comes from `rust:<ver>-alpine` (cargo + rustup copied
  into a Hadron-toolchain stage, `-C target-feature=-crt-static`), the pattern
  `Dockerfile.agent` already uses for cua-driver. Binaries link dynamically
  against Hadron's musl and the libraries this Dockerfile builds; every
  binary is checked with `ldd` for `not found`.
- **libclang** (bindgen, 7 components) comes from the existing `llvm` stage,
  used at build time only.
- **Alpine's APKBUILDs** are the reference for dependencies and build steps.
  They carry no musl patches for COSMIC itself.
- **Disk.** Cargo target dirs are several GB each. Builds use BuildKit cache
  mounts for the registry and target dirs, so nothing but installed files
  reaches a layer.
- **Accounts on Kairos.** `/etc` is rebuilt from cloud-config at boot, so an
  account created at runtime does not survive. The installer TUI keeps
  creating the user; cosmic-initial-setup runs in its normal (non-OEM) mode
  after the first login and never creates accounts.
- **polkit** is built with the duktape JS backend (not mozjs). cosmic-osd is
  the polkit agent.
- **Display manager.** The cosmic variant enables greetd + cosmic-greeter on
  tty1 the way the other variants enable ly, with the same live/install-mode
  guard, and the kairos-splash drop-in orders the splash before it.

## New dependencies to build

Build-time: just, libclang (llvm stage), intltool, desktop-file-utils,
appstream-glib, iso-codes. Runtime: greetd, pop-launcher, polkit (duktape),
accountsservice, xdg-desktop-portal, Xwayland, libpulse (client library
only), oniguruma, dav1d, fonts COSMIC defaults to (Open Sans, Noto Sans Mono),
hicolor icon theme.

## Verification

- Build-time: each component's binary runs `--help`/`--version` or at least
  `ldd` clean; the image build fails otherwise.
- Boot-UX checks extended for the cosmic image (greeter wired, splash ordered
  before it, release file `FLAVOR=cosmic`).
- VM: install, graphical login, first-run setup appears, Store lists Flathub,
  a Flatpak app installs and starts, Settings > Date & Time accepts a change
  after a polkit prompt, update notification appears.

## Left to the builder

Component build order, how stages are split, which optional Cargo features to
enable, and the exact Xwayland source (XLibre's or upstream xwayland).

## As built (2026-11-03)

Verified by installing the ISO in QEMU and using it: graphical login with the
user's display name, the COSMIC session (panel, dock, launcher, icons), the
full first-run setup (accessibility, language, keyboard, timezone, theme,
layout, workspaces, shortcuts), COSMIC Settings, COSMIC Terminal, COSMIC Store
listing Flathub, and the polkit password prompt for a timezone change.

Gaps found on the way, and how each is handled:

- **No systemd-localed in Hadron** (built with `-D localed=false`). The setup
  wizard's language page gave up entirely; it is patched (sed in the
  Dockerfile) to treat a missing `locale1` as "no system locale". Choosing a
  language sets COSMIC's own language, not the system locale. The real fix is
  enabling localed in Hadron; follow-hadron.yml picks that up once released.
- **No `locale` command on musl.** musl-locales provides it, with Alpine's patch
  listing every language, suffixed `.UTF-8` because the wizard keeps only those.
- **No hicolor `index.theme`.** Every app icon fell back to a gear.
- **cosmic-osd execs `/usr/libexec/polkit-agent-helper-1`** (compiled in), while
  polkit installs the helper under `/usr/lib/polkit-1`; a symlink bridges it.
- **Release builds are not wired yet.** A cold COSMIC build takes hours and
  more disk than a stock GitHub runner has (LLVM for bindgen, 21 Rust
  components); release.yml still builds sway, i3 and agent only.

Still unverified: installing a Flatpak from the Store UI (the catalogue
loads; the install button was not exercised), Xwayland apps, screen sharing
through the portal, and suspend/resume.
