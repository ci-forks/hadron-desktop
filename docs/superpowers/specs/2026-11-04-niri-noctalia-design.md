# niri + Noctalia desktop variant

## Intent

The third choice next to COSMIC (for people new to Linux) and Sway/i3 (for
people who already know a tiling window manager). niri is a scrollable-tiling
Wayland compositor: windows sit in columns on an endless horizontal strip, so
opening a window never squeezes the others. Noctalia is a complete shell around
it: bar, dock, launcher, control center, notifications, lock screen,
wallpapers, and a polkit password prompt. Together they are the "modern
tiling" look that the current Wayland crowd uses, polished out of the box
instead of assembled from waybar, fuzzel, mako and swaylock.

## Goals

- `DESKTOP=niri` builds a fourth variant from the same Dockerfile, on the same
  Hadron pin, from source against Hadron's musl.
- niri v26.04 and Noctalia v5.4.1. Noctalia v5 is C++23 on Wayland and OpenGL ES,
  with no Qt or GTK (v4 was a Quickshell/Qt shell).
- A complete desktop after login with no configuration: Noctalia's defaults,
  a curated `/etc/niri/config.kdl`.
- The same key habits as the Sway variant: Super+Enter terminal, Super+D apps,
  Super+Shift+Q close, Super+Shift+E power menu, Super+F1 cheat sheet.
- X11 apps work (xwayland-satellite, which niri starts on demand).
- Flatpak apps get file dialogs (xdg-desktop-portal + the GTK backend).
- `hadron-update notify` works: Noctalia serves org.freedesktop.Notifications.

## Non-goals (this first variant)

- Screen sharing. niri implements it through xdg-desktop-portal-gnome, which
  needs GTK4 and libadwaita.
- A graphical greeter. ly is the login screen, as in the Sway variant, so the
  installer's preseeded login and the splash ordering are unchanged.
- An app store. `flatpak` on the command line, as in Sway and i3.

## Decisions

- **Login:** ly on tty1 runs `niri-session`, which starts niri as a systemd
  user service (graphical-session.target, autostart).
- **Configuration:** `/etc/niri/config.kdl` is niri's own commented default
  with targeted changes (see the header of that file). niri uses it until the
  user creates `~/.config/niri/config.kdl`. Noctalia has no system-wide config;
  its defaults are used and the user changes them in its Settings window.
- **Build:** niri, xwayland-satellite and librsvg are Rust and reuse COSMIC's
  build environment (`cosmic-build`). librsvg needs cargo-c, which is compiled
  there. Noctalia's other dependencies are C/C++ stages on the toolchain:
  sdbus-c++, libsecret, libsodium, libqalculate (with GMP and MPFR), md4c,
  nlohmann-json, toml++, libical, libwebp, libjxl (with brotli, highway and
  lcms2), and stb's headers.
- **polkit** setup in the default stage is shared by COSMIC and niri.

## Verification

- Build time: every Rust binary and Noctalia pass an `ldd` check;
  `niri --version` runs.
- Boot-UX checks for the niri image: `niri validate` accepts the shipped
  config, Noctalia runs and finds its assets, xwayland-satellite is on PATH,
  the polkit helper is setuid, the GTK portal is installed.
- VM: install, ly login, niri with Noctalia's bar and dock, launcher,
  control center, a terminal, an update notification.

## As built (2026-11-05)

Verified by installing the ISO in QEMU: ly offers the niri session with the
user preselected; niri starts Noctalia, which draws the bar and wallpaper; the
launcher finds apps and its calculator works (libqalculate); the control
center, notifications (the Hadron welcome), the power menu, the lock screen
and unlocking with the password all work; Super+F1 opens the cheat sheet.

The QEMU host has no GPU render node, so the guest has only Mesa's software
renderer, which niri refuses (`src/backend/tty.rs`). The VM run used a niri
built with that check removed and software EGL registered as the primary
renderer, run from the test user's home. The image ships the unmodified niri:
it needs a working GPU driver, as on any real machine.

Found by booting it, and fixed:

- **Unlock always failed** ("cannot retrieve authentication info"):
  `unix_chkpwd` was not setuid, so pam_unix running as the user cannot read
  /etc/shadow. Every screen locker hits this, COSMIC's too; the default stage
  now sets it for all variants, and a boot-UX check guards it.
- **Noctalia's polkit agent is off by default.** Privileged requests fell back
  to a terminal prompt or failed. `hadron-noctalia` starts Noctalia with
  `polkit_agent = true` (and the overview backdrop) from
  `/usr/share/hadron-desktop/noctalia-defaults.toml`.
- **The first-login welcome could be lost**: hadron-welcome tried once after 3
  seconds; a shell still starting did not own the notification name yet. It
  now retries for 30 seconds.
- **No foot terminfo**: foot.ini sets `term=xterm-256color`, as on Sway.
- **No cursor theme**: Adwaita's cursors, set as the default theme.
- **niri was 129 MB** with line-table debug info; it is stripped (25 MB).

Not verified: typing a password into Noctalia's polkit dialog. The agent
registers and opens its panel for each request, but under software rendering
the panel takes longer to draw than timedated's 25-second polkit timeout. Also
not verified: X11 apps through xwayland-satellite, Flatpak file dialogs, and
suspend.

Seen on the way, not fixed here: polkitd 126 aborts when an unprivileged user
calls CheckAuthorization with a `unix-session` subject. Applications send
`system-bus-name` or `unix-process` subjects, which work.
