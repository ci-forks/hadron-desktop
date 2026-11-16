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
- Back up everything on a machine that survives a reboot, to a USB disk or
  a server, encrypted, with only the changes sent after the first backup.
- Or write the backup to one file, to copy and keep anywhere.
- Restore a whole machine onto a fresh Isotope install: the users and their
  passwords, the hostname, the homes, SSH keys, extensions and settings.
- Or restore only a user's files, apps and extensions into a machine that
  already has its users.
- Optional daily or weekly backups with no user action.
- The same on the four desktop variants and the server edition.

## Non-goals

- A graphical backup app. The Isotope menu opens the command in a terminal,
  as it does for updates.
- Backing up the system image. It is signed and can be downloaded again; the
  release is recorded in the backup so a restore can say what it came from.
- Flatpak app binaries: the apps are installed again from Flathub. Docker
  and Podman storage only on request (`--with-containers`): it is often
  large, and copying it while it runs is not consistent.
- Paths Kairos does not keep across a reboot (`/var`, `/etc` and `/srv` are
  rebuilt at every boot, except the persistent paths). They are not state.
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
    --with-containers          also back up Docker's and Podman's storage
isotope backup                 back up now
    --export FILE              write the backup to one file instead
    --with-containers          also Docker's and Podman's storage, this time
isotope backup status          target, schedule, last backup and its result
isotope backup list            the snapshots in the target
isotope backup schedule daily|weekly|off
isotope restore [SNAPSHOT]     restore from the target (default: latest)
    --from TARGET|FILE         a target this machine does not know yet, or an
                               exported file
    --only homes,apps,extensions
    --user OLD=NEW             with --only homes: OLD's files into NEW's home
    --host NAME                when several machines share the target
```

TARGET is an absolute directory (`/run/media/anna/Backup/isotope`, also
`local:/path`) or a restic repository URL (`sftp:`, `rest:`, `s3:`, `b2:`,
`azure:`, `gs:`, `swift:`, `rclone:`). A relative path is refused: restic
would put the repository in the current directory. `init` creates only the
last component of a directory, and refuses a parent that is missing or on a
tmpfs, so a disk that is not mounted is an error rather than a new folder on
the internal disk or in memory. Credentials for a remote target (for
example `AWS_ACCESS_KEY_ID`) go in `/usr/local/isotope-backup/env`, one
`NAME=value` per line; `init` says so for a URL target.

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

`/usr/local/isotope-backup/` (root, 0755), on the persistent partition.
Not `/var/lib`: Kairos rebuilds `/var` at every boot, and only the paths in
its persistent list survive.

- `config` (0644, so `isotope status` can read it as any user; it holds no
  secret): `target=`, `schedule=` (`off`, `daily`, `weekly`),
  `containers=`, `last=` (Unix time of the last successful backup),
  `result=` (`ok`, `partial: ...`, or the error of the last attempt).
- `password` (0600): the repository password.
- `env` (0600, optional): credentials for the target.
- `cache/`: restic's cache.
- `manifest/` is written before each backup and backed up with it:
  `release` (a copy of `/etc/isotope/release`), `hostname`, `encryption`
  (the installer's marker mode, or `none`), `users` (name and uid of every
  home), `flatpak-system` and `flatpak-user-<name>` (installed app IDs and
  their remote), `extensions` (installed system extensions and where each
  came from).

### What is backed up

Everything that survives a reboot. On Kairos that is the persistent
partition, mounted at `/usr/local`, where every persistent path is a bind
mount of `/usr/local/.state/<path>.bind` (`/home` is `.state/home.bind`,
`/etc/ssh` is `.state/etc-ssh.bind`), and `/oem`, the installer's
cloud-config: the users with their password hashes, the hostname, the
keyboard, the encryption settings. Without:

- in each home: `.cache`, `.local/share/Trash`, `.local/share/flatpak`,
  `.var/app/*/cache`, and any directory with a `CACHEDIR.TAG`;
- `/var/log` and `lost+found`;
- unless `--with-containers`: `/var/lib/docker`, `/var/lib/containerd` and
  each home's `.local/share/containers`;
- the backup's own cache, password and credentials, a restore's staging
  directory, and a directory target or export inside the partition, which
  would otherwise be backed up into itself.

Each snapshot is tagged `isotope` and with the hostname. After a backup,
`restic forget --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --prune`
keeps the repository from growing without end, for this host's snapshots
only. restic's exit status 3 (some files could not be read) still wrote a
snapshot of the rest: it counts as a partial backup, and pruning runs.

### Export

`isotope backup --export FILE` makes a restic repository next to FILE (on
the same disk, with the same disk checks as `init`), backs up into it, packs
it into FILE as an uncompressed tar (restic already compresses and
encrypts), and removes the repository. The password is the machine's
backup password when it has one, so the user keeps one password; otherwise
a new one, shown once. `isotope restore --from FILE` unpacks it into a
directory only root can enter (next to the file, or under
`/usr/local/isotope-backup`), refusing any entry that is not a plain file
or directory or that leaves the directory, and restores from it with the
same code as from a target.

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

The default is a **whole-machine restore**, for a fresh install: the user
installs Isotope, signs in, runs `isotope restore --from TARGET|FILE` and
types the password. It:

1. Resolves the snapshot once (the newest with tag `isotope`; with several
   hosts in the target, `--host` must say which), restores its manifest
   into `/usr/local/.isotope-restore` (root, 0700) and shows what it will
   do. It asks first (`--yes` skips the question).
2. Refuses when the backed-up disk's encryption mode differs from this
   one's: the old `/oem` would not unlock the new disk. The user can then
   reinstall with the same choice, or restore with `--only`.
3. Restores `/usr/local` and `/oem` into the staging directory, stops
   Docker, and moves the persistent partition's files into place. The
   staging directory is on the same partition, so these are renames, made
   with directory file descriptors that never follow a link (a user's home
   is on it). A file in the backup replaces the same file; other files
   stay. The owners are the backup's: `/oem` brings back the same users,
   with the same IDs, at the next boot.
4. Copies `/oem` over (another partition, only root's).
5. Keeps the typed password as the machine's backup password, since the
   backup does not hold it, and reinstalls the Flatpak apps.
6. Restarts the machine.

With `--only homes,apps,extensions`, it restores into the users this
machine already has instead:

- **homes**: for each user in the manifest, the local user with the same
  name, or the one `--user` names; a user with no local match is skipped
  and listed. The files are restored into the staging directory, given to
  the local user there (where no user can change them), then moved into
  `/usr/local/.state/home.bind/<user>` the same way as above.
- **apps**: `flatpak install --noninteractive` from the remote each app
  came from, system apps as root and user apps as their (mapped) user.
- **extensions**: each one installed again through `isotope ext install`.

A failure in one part is listed and does not stop the others. Ctrl-C
removes the staging directory, an unpacked export and the typed password,
which is kept under `/run`.

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
- Tests with a real restic, skipped when restic is not on `PATH`; CI
  installs the pinned restic for them: a backup and an `--only homes`
  restore into another user, an export and a whole-machine restore of it,
  and the refusal on a different disk encryption.
- `test/backup/check-backup.sh IMAGE` checks a built image: `isotope`,
  the three links, restic's version, the enabled timer.

## Left to the builder

- The exact wording of the messages, in the style of `isotope upgrade`.
- How the restore progress is shown (restic's own output is fine).
