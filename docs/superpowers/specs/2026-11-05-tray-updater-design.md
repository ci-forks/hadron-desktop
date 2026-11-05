# Tray updater

## Intent

Today a user learns about a new release from a desktop notification that says
"open a terminal and run `hadron-update upgrade`". The tray updater replaces
that with an icon in the system tray of every desktop variant. The icon shows
when a new release is out, installs it on request, shows progress, and asks
for the reboot. From the same menu an admin can install another release, switch
to another desktop variant, or (advanced) install any OCI image they name.

## Goals

- A tray icon in sway, i3, COSMIC and niri, started with the session.
- One menu for every flow:
  - a status line (current release, state)
  - "Install vX.Y.Z" when a newer release is out
  - "Other releases ▸": every published release of this flavor, older ones too
  - "Switch desktop ▸": sway, i3, cosmic, niri (never agent), behind a warning
  - "Advanced…": install an OCI image reference the user types
  - "Restart now" when a reboot is pending
- Progress while the image downloads and installs, and a reboot prompt at the
  end.
- Only members of the `admin` group can install anything, decided by polkit.
  Admins are not asked for a password.
- After a downgrade or a pinned install, the updater stops offering "latest".

## Non-goals

- The agent variant. It is an unattended appliance with no admin user. It gets
  no tray, and its inherited `hadron-update notify` line goes away. It keeps the
  CLI.
- Upgrading the recovery partition.
- Automatic (unattended) upgrades.
- Private images. Inspection is anonymous, so an image that needs registry
  credentials is refused with a sentence saying so.

## Decisions (made by the user)

1. **Authorization:** admin only, through polkit. The polkit rule returns YES
   for `admin` and NO for everyone else, so no polkit agent is needed.
2. **Advanced mode** (typed OCI reference) is on by default. It can be turned
   off with `advanced=false` in `/etc/hadron-desktop/update.conf`.
3. **Switching desktop** is in the main menu, behind a warning that per-user
   desktop settings may not carry over and the login screen may change.
4. **Downgrade** is allowed. Installing anything other than the latest release
   pins the system to it; "Install latest" removes the pin.
5. **i3 gets a real tray icon.** i3bar only hosts XEmbed icons, so the i3 image
   adds snixembed, which shows StatusNotifierItem icons (ours and any app's) in
   i3bar. Sway already hosts them through waybar's tray module.
6. **Agent:** no tray, no notify.
7. **Releases get OCI labels now and cosign signatures** (signatures are
   published now; verifying them in the updater can follow).

## Architecture

Two parts, both in the existing `update/` Go module and built in the existing
`update-build` stage. The module gains `github.com/godbus/dbus/v5` and
`fyne.io/systray` (both pure Go on Linux: no cgo, no GTK).

### hadron-updated (root)

- A system D-Bus service, `io.kairos.HadronUpdate1`, started by D-Bus
  activation through `hadron-updated.service` (`SystemdService=`). Bus
  activation needs no `systemctl enable`, which matters because Kairos' `/etc`
  overlay hides unit links baked into the image.
- A D-Bus policy file allows anyone to call it. Each mutating method asks
  polkit (`CheckAuthorization`, subject `system-bus-name` of the caller) for
  the action `io.kairos.hadron-update.apply` or `io.kairos.hadron-update.reboot`.
  A rules file grants both to `admin`. Read-only methods need no
  authorization.
- Methods: `Status`, `Check`, `ListReleases`, `Inspect(ref)`,
  `Apply(ref, digest)`, `Cancel`, `Reboot`, `SetPin(ref|"")`.
- Signals: `StateChanged(state, detail)` and `Progress(line)`.
- One job at a time. The daemon owns the job, so logging out does not kill an
  upgrade. It exits after some idle minutes when no job runs.
- It runs `kairos-agent upgrade --source oci:<ref>@<digest>` and forwards its
  output lines as progress.
- State lives in `/run/hadron-update/` (job state) and
  `/var/lib/hadron-update/pin` (the pin).

States: idle → checking → up-to-date | update-available → inspecting →
applying → reboot-pending | error. While reboot-pending, `Apply` is refused: a
second upgrade would push the never-booted image into the passive slot and
overwrite the known-good one. Once `kairos-agent upgrade` has started, a
failure also ends in reboot-pending, not error (see "Failed install" below).

### hadron-update tray (user)

- `hadron-update tray`, an SNI item through `fyne.io/systray`. It replaces the
  `hadron-update notify` autostart line in sway, i3, COSMIC and niri. It keeps
  sending the one notification per new release that `notify` sends today.
- It talks only to the daemon. The daemon does the GitHub and registry calls,
  and every check runs as root on one schedule, not once per user.
- If no SNI watcher is on the bus yet (the bar starts after the tray), it
  retries until one appears.
- It exits at once when `FLAVOR=agent`.
- Dialogs (the switch-desktop warning, "Advanced…", "Restart now?") open the
  variant's terminal running `hadron-update` subcommands, so the tray needs no
  GUI toolkit. The CLI talks to the same daemon.

### Image checks (`Inspect`)

Every flow ends as an OCI reference. "Install latest", the release list and the
desktop list only fill it in. `Inspect` then:

1. Parses the reference: tag or `@sha256:` digest. (Fixes today's `splitRef`,
   which splits a digest reference at the wrong colon.)
2. Resolves the manifest and the digest, and checks for `linux/amd64`.
3. Reads the image labels: `io.kairos.hadron-desktop.flavor`,
   `.version` and `.hadron-version` (new, added by the release). An image
   without them is shown as "not a Hadron Desktop image" and can be installed
   only from "Advanced…", after a second confirmation.
4. Refuses `flavor=agent`. That image renames the owner account and locks the
   password.

`Apply` takes the digest that `Inspect` returned, so the system installs the
exact image the user reviewed. Images from repositories other than
`ghcr.io/kairos-community/hadron-desktop` are accepted only through
"Advanced…".

### Per variant

| Variant | Tray host | Change |
|---|---|---|
| sway | waybar `tray` module (exists) | autostart `tray`; add polkit |
| i3 | i3bar + snixembed (new) | autostart snixembed and `tray`; add polkit |
| cosmic | status-area applet (exists) | autostart `tray` (XDG autostart) |
| niri | Noctalia `tray` widget (exists) | autostart `tray` |
| agent | none | remove the inherited `notify` line |

polkit is already in COSMIC and niri. Sway and i3 copy the same `polkit` stage
and get the `polkitd` system user.

### Release changes

- `LABEL`s on the final image: flavor, version, Hadron version.
- cosign keyless signatures for every `<flavor>-<tag>` image, pushed next to
  it.

## Calls made after review (by Claude, not the user)

- **Trusted by name:** a reference the daemon built itself, from
  `ghcr.io/kairos-community/hadron-desktop` and a `<flavor>-<tag>` where the tag
  is in GitHub's published release list, skips the label check. Releases
  published before the labels existed stay installable. The release list comes
  from GitHub releases, not registry tags, because images are pushed before
  their release is published.
- **Agent marker:** an image is "agent" if its label says so or its tag starts
  with `agent-`. Both are refused.
- **Agent image:** `Dockerfile.agent` removes the daemon, its D-Bus and polkit
  files and the tray autostart it inherits from i3. On agent, the CLI keeps
  today's direct path (`sudo` and `kairos-agent upgrade`), run by an operator.
- **Cancel:** allowed while checking and inspecting. Once `kairos-agent upgrade`
  runs, the job cannot be cancelled and the menu hides "Cancel", because a kill
  between the A/B swap and the image write can leave no good slot.
- **Pin:** written only when `Apply` succeeds, for a release that is not the
  latest or for an "Advanced…" image. While pinned, no new-release notification
  is sent, the menu still shows "Install latest (vX.Y.Z)", and installing it
  removes the pin. "Switch desktop" installs the latest release of the target
  flavor and sets no pin.
- **Module:** `update/` stops being stdlib-only; the `update-build` stage
  comment changes with it.
- **Failed install:** kairos-agent moves active to passive and the new image
  to active before its after-upgrade hook, and returns that hook's error. A
  failure can therefore come after the swap. Once `kairos-agent upgrade` has
  started, every outcome keeps the reboot marker in `/run/hadron-update/` and
  ends in reboot-pending; the marker says `started`, `installed` or `failed`,
  and Status reports it as `pending`. After a failure or an interrupted write
  the tray and the CLI say "restart before trying again", and `Apply` and
  `RebootPrevious` stay refused until the restart. Only a failure before
  kairos-agent starts (a refusal, the network, a moved tag) ends in error.
- **Releases only, unless Advanced:** every image that is not trusted by name
  is refused without advanced mode, labelled or not: unlabelled as "not a
  Hadron Desktop image", labelled as "not a published Hadron Desktop release".
  The CLI always inspects without the override and asks the second question
  (default No, never answered by `--yes`) before it applies with it.
- **update.conf:** `advanced=` is read without regard to case. true, yes, 1
  and on turn advanced mode on; false, no, 0 and off turn it off; any other
  value, or a file that exists but cannot be read, turns it off and is
  logged. No file or no key: on.
- **Idle exit:** `Status` does not count as activity, and a watcher polls
  `Status` only while a job runs, so a running tray does not keep the root
  service up. The newest release tag is kept in `/run/hadron-update/latest`
  for the next instance.
- **Flatpak callers:** a mutating call from a process in a Flatpak sandbox
  (`/proc/<pid>/root/.flatpak-info`) is refused before polkit is asked: the
  app runs as the user, so the `admin` rule would let it install. The process
  comes from the bus (`GetConnectionCredentials`: the pidfd when the bus sends
  one, else the pid). The check fails closed: the root must resolve (not a
  zombie), and the pidfd's pid, or the pid's start time, must be the same
  before and after. The polkit rule itself is unchanged.
- **Registry token service:** only an https token realm is fetched; the daemon
  runs as root and the realm comes from a registry any user can name.

## Constraints

- Everything builds from source on the Hadron toolchain (musl). snixembed
  needs Vala and GTK3: GTK3 and libdbusmenu are built today; the Vala compiler
  is a new stage, used at build time only.
- Upstream kairos-agent A/B behaviour: an upgrade from active moves active to
  passive; from passive, passive is left alone.

## Left to the builder

- Menu wording, icons (light/dark), the idle timeout and the check schedule.
- How progress maps to the menu (label plus tooltip; kairos-agent prints no
  percentage).
- Whether `hadron-update notify` stays as an alias of `tray --no-icon`.
- Tests: the daemon logic behind an interface for kairos-agent and the
  registry, so states, the pin and the refusals are unit-tested without root.
