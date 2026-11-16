# One `isotope` command, with backup and restore

## Why

Isotope has three user commands today: `isotope-update`, `isotope-ext` and
`isotope-recovery`. They are already one Go binary (`update/`), run under
three names. Each new feature would add one more name to learn. Backup and
restore is the next feature, so this is the moment to give the system one
command, `isotope`, with a subcommand per job, like `git` or `flatpak`.

Backup is the gap the update design leaves. Updates cannot break the system,
and a rollback is one reboot away, but nothing protects the files in `/home`
from a dead disk, a lost laptop or a reinstall.

## Goals

- `isotope <command>` for every user-facing job: `status`, `check`,
  `upgrade` (also `update`), `rollback`, `switch`, `reboot`, `ext`,
  `recovery`, `backup`, `restore`, `help`.
- Back up a whole machine to a USB disk or a server, encrypted, with only
  the changes sent after the first backup.
- Restore onto a fresh Isotope install: the files of each user, the saved
  Wi-Fi and VPN connections, the Flatpak apps and the system extensions.
- Optional daily or weekly backups with no user action.
- The same on the four desktop variants and the server edition.

## Non-goals

- A graphical backup app. The Isotope menu opens the command in a terminal,
  as it does for updates.
- Backing up the system image. It is signed and can be downloaded again; the
  release is recorded in the backup so a restore can say what it came from.
- Backing up Docker images and volumes, Distrobox containers, and Flatpak
  app binaries. Apps are reinstalled from Flathub; containers can be
  recreated. Docker data is often large, and copying it while it runs is not
  consistent.
- The agent edition. It is an appliance with no user files. Its image does
  not have restic, and `isotope backup` says so.

## Anti-goals

- A backup that cannot be restored because the password was only on the
  lost machine.
- A scheduled backup that writes to the internal disk because the USB disk
  was not plugged in.
- Removing `isotope-update`, `isotope-ext` or `isotope-recovery`. Systems
  installed before this change, scripts, the tray and people's habits use
  them.

## The command

`/usr/bin/isotope` is the binary. `isotope-update`, `isotope-ext` and
`isotope-recovery` are links to it and keep their behaviour exactly:
`isotope-update X` is `isotope X`, `isotope-ext X` is `isotope ext X`,
`isotope-recovery X` is `isotope recovery X`. The usage text, the
messages, the Isotope menu, the shortcut sheet, `/etc/motd` and the README
use `isotope …`. Messages start with `isotope:`.

`tray`, `notify` and `daemon` stay as subcommands for the session and
systemd, and stay out of the usage text. The D-Bus service, the polkit
actions and their names do not change.

`isotope update` is `isotope upgrade`: people type it.

## Backup

The backups are [restic](https://restic.net) repositories. restic encrypts
everything with the repository password, sends only new data, keeps
snapshots, and can write to a directory (a USB disk), SFTP, a REST server
and S3. It is built from source in the Dockerfile, pinned and followed by
Renovate, like cliphist.

Backup and restore run as root, through sudo like `isotope upgrade`, so
they can read every home and write the files back with their owners. Only
the `admin` group can run them.

### Commands

```
isotope backup init TARGET     make TARGET the backup target
isotope backup                 back up now
isotope backup status          target, schedule, last backup and its result
isotope backup list            the snapshots in the target
isotope backup schedule daily|weekly|off
isotope restore [SNAPSHOT]     restore from the target (default: latest)
    --from TARGET              a target this machine does not know yet
    --only homes,network,apps,extensions
    --user OLD=NEW             restore the files of OLD into NEW's home
```

TARGET is a directory (`/run/media/anna/Backup/isotope`; `init` creates
only its last component, so a disk that is not mounted is an error rather
than a new folder on the internal disk) or a restic
repository URL (`sftp:host:/path`, `rest:https://…`, `s3:…`). Credentials
for a remote target (for example `AWS_ACCESS_KEY_ID`) go in
`/var/lib/isotope-backup/env`, one `NAME=value` per line; `init` says so for
a URL target.

### The password

`init` creates the restic repository with a new random password in the
recovery key format (systemd's: 8 groups of 8 modhex characters, 256 bits),
shows it once, and asks the user to type one group back, as the installer
does for a recovery key. The password is also stored on the machine, so a
scheduled backup needs no one. **The backup can be restored only with this
password**: the screen says to write it down and keep it away from the
machine. `init --password-stdin` reads a chosen password instead (for
scripts, and for a target that already exists, which `init` then opens
instead of creating).

### State

`/var/lib/isotope-backup/` (root, 0755), on the persistent partition:

- `config` (0644, so `isotope status` can read it as any user; it holds no
  secret): `target=`, `schedule=` (`off`, `daily`, `weekly`), `last=`
  (Unix time of the last successful backup), `result=` (`ok`, or the error
  of the last attempt).
- `password` (0600): the repository password.
- `env` (0600, optional): credentials for the target.
- `manifest/` is written before each backup and backed up with it:
  `release` (a copy of `/etc/isotope/release`), `users` (name, uid and home
  of every user with a home in `/home`), `flatpak-system` and
  `flatpak-user-<name>` (installed app IDs and their remote), `extensions`
  (installed system extensions and where each came from, from
  `/var/lib/isotope-update/extensions/`).

### What is backed up

- `/home`, without caches and what can be downloaded again: `.cache`,
  `.local/share/Trash`, `.local/share/flatpak`, `.var/app/*/cache`,
  `.local/share/containers`, and any directory that has a `CACHEDIR.TAG`.
- `/etc/NetworkManager/system-connections` (Wi-Fi passwords, VPNs).
- `/var/lib/isotope-backup/manifest`.

Not `/oem`: it has the installer's password hashes and is written again by
the new install.

Each snapshot is tagged `isotope` and with the hostname. After a backup,
`restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --prune`
keeps the repository from growing without end, for this host's snapshots
only.

### Schedule

`isotope-backup.timer` is enabled in the image and starts
`isotope-backup.service` every day (`Persistent=true`, a random delay of up
to one hour). The service runs `isotope backup --scheduled`, which:

- does nothing when `schedule=off` or there is no target;
- does nothing on `weekly` when the last success is less than 7 days old;
- for a directory target, does nothing when the directory is not there: a
  USB disk that is not plugged in is not an error, and restic is never
  pointed at a path that might be on the internal disk (the target must
  already hold a restic repository: a `config` file);
- otherwise backs up and records the result.

A failed scheduled backup is recorded in `config` and in the journal, and
`isotope backup status` shows it. When the last success is more than 14
days old on a daily or weekly schedule, `isotope status` says so too.

## Restore

On a fresh install, the user signs in and runs
`isotope restore --from TARGET`, then types the password. Restore:

1. Reads the snapshot's manifest and shows what it will do: the release the
   backup came from, the homes and which local user each goes to, the
   number of connections, apps and extensions. It asks before doing
   anything (`--yes` skips the question).
2. **homes**: for each user in the manifest, the local user with the same
   name, or the one `--user` names. A user with no local match is skipped
   and listed. The files are restored into
   `/home/.isotope-restore/<snapshot>/` (same file system, so the moves are
   renames), then moved into the home: a restored file replaces a file of
   the same path, other files in the home stay. The moved files are then
   owned by the local user (`chown -R` by name, since uids can differ).
3. **network**: the connection files go back to
   `/etc/NetworkManager/system-connections` (0600, root), then `nmcli
   connection reload`.
4. **apps**: `flatpak install --noninteractive` from the remote each app
   came from, system apps as root and user apps as their user. An app that
   fails to install is listed and does not stop the restore.
5. **extensions**: each extension is installed again from its catalog,
   through the same code as `isotope ext install`. One that cannot be found
   is listed.

The target given with `--from` is stored as this machine's target only if
the user says yes at the end, with the password typed.

## On each edition

| | Desktops (4) | Server | Agent |
|---|---|---|---|
| `isotope` and the old links | yes | yes | yes; `ext`, `recovery` and `backup` refuse |
| restic, `backup`, `restore`, the timer | yes | yes | no |
| "Backup" in the Isotope menu | yes | no menu | no |

## Testing

- Go unit tests with a fake restic (`ISOTOPE_RESTIC`, like
  `ISOTOPE_UPDATE_AGENT`): the arguments of each restic call, the
  excludes, the schedule decisions, the missing-USB rule, the manifest, the
  user mapping and the moves of a restore, and that the old names dispatch
  as before.
- One test with a real restic, skipped when restic is not on `PATH`; CI
  installs the pinned restic for it. It backs up a temporary tree, removes
  it and restores it.
- `test/backup/check-backup.sh IMAGE` checks a built image: `isotope`,
  the three links, restic's version, the enabled timer.

## Left to the builder

- The exact wording of the messages, in the style of `isotope upgrade`.
- How the restore progress is shown (restic's own output is fine).
