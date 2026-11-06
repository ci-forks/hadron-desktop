# Two image lines: full for hardware, vm for virtual machines

## Goal

Releases up to v0.0.7 build every flavor with `GPU=vm FIRMWARE=false`: no Intel
or AMD GPU driver and no firmware, so on a normal desktop or laptop the screen
renders with llvmpipe and Intel Wi-Fi does not come up. The maintainers asked
for the standard ISO to work on normal machines (`GPU=full`), and for a
separate, smaller ISO for VMs.

## What a release publishes

| Line | Build args | ISO | Upgrade image |
| --- | --- | --- | --- |
| full | `GPU=full FIRMWARE=true` | `hadron-desktop-<flavor>-<tag>-amd64.iso` | `:<flavor>-<tag>` |
| vm | `GPU=vm` | `hadron-desktop-<flavor>-vm-<tag>-amd64.iso` | `:<flavor>-vm-<tag>` |
| agent | `GPU=vm` (forced in the Makefile) | `hadron-desktop-agent-<tag>-amd64.iso` | `:agent-<tag>` |

Nine ISOs per release, each with `.sha256`, SPDX SBOM and build manifest. The
manifest gains `gpu` and `firmware`. The agent stays a VM appliance on GPU=vm:
it runs in QEMU with no GPU, and its gates are written against that image.

## How a system stays on its line

- The `kairos` stage writes `GPU=full|vm` to `/etc/hadron-desktop/release`
  and labels the image `io.kairos.hadron-desktop.gpu` and `.firmware`.
  `release-manifest.sh` fails the release if the labels disagree with the line
  the job builds (and full without firmware, or vm with it, also fails).
- `Release.Line()` reads the key. `ImageFor` and `ReleaseRef` build
  `<flavor>-<tag>` for full and `<flavor>-vm-<tag>` for vm; agent ignores the
  line. `LoadRelease` refuses an unknown `GPU` value.
- `Classify` trusts both `<flavor>-<tag>` and `<flavor>-vm-<tag>` of a
  published release, and reports the line in `Verdict.GPU` (from the tag, or
  from the label for a labelled image). An image of the other line is treated
  like one of another flavor: allowed, with a warning in the CLI
  (`installFlow`), not a refusal.
- `hadron-update switch --gpu full|vm [FLAVOR]` and a tray item ("Switch to
  the virtual machine image" / "Switch to the hardware image") move a system
  to the other line. After the reboot the new image's own release file names
  the new line, so later upgrades follow it with no state on /var.

## Decisions

1. **Installs without a GPU key (v0.0.5 to v0.0.7, all GPU=vm) follow the full
   line.** Their installed hadron-update can only build `<flavor>-<tag>`, and
   it does not trust `<flavor>-vm-<tag>` (its Classify reads `vm-v0.0.8` as
   the tag), so the first upgrade lands on full whatever the new code says.
   Reading "no key" as full keeps the new code consistent with that. The full
   image also runs in a VM (llvmpipe and virgl are in both lines).
   Cost if wrong: a VM user downloads about 0.5 GB more per upgrade until they
   run `hadron-update switch --gpu vm`.
2. **Full keeps the existing tag and asset names; vm gets the `-vm` suffix.**
   The full line is "the" image the maintainers want users to get, and old
   updaters, links and docs keep working. Cost if wrong: renaming later breaks
   every installed updater, so this is hard to undo.
3. **The line is named by the build arg (`GPU`), not a new "edition" word.**
   It is what the build knob is called and what differs. FIRMWARE follows
   the line (`full` implies `true`) and is only recorded, not a second axis.
   Cost if wrong: a third line (say full without firmware) needs a new key
   and tag scheme.
4. **A cross-line image is allowed, with a warning.** Same rule as a flavor
   switch: both are published releases. Cost if wrong: a user on real hardware
   who picks the vm image loses Wi-Fi and the GPU driver until they roll back
   (the previous system stays as the fallback boot entry).
5. **Each line has its own registry cache** (`buildcache-<flavor>` for vm, as
   today, so pull requests keep reading it; `buildcache-<flavor>-full` for
   full). One shared cache would be rewritten by each line's job every
   release, and the other line would rebuild Mesa and everything above it.
   Cost if wrong: about 80 MB to a few GB more registry storage per flavor.
6. **COSMIC is prebuilt per half and per line (four jobs).** cosmic-build
   copies Mesa, so the stage key differs per GPU (checked in
   `tools/test_stage_key.py` against the real Dockerfile). Each has its own
   concurrency group, so the two lines do not wait for each other.
7. **LLVM stays one shared prebuild; SPIRV-Tools and llvm-spirv are not
   prebuilt.** The `llvm` stage does not depend on GPU. `llvm-stack-full`
   adds SPIRV-Tools and SPIRV-LLVM-Translator, which compile against the
   prebuilt LLVM (the `llvm` build context replaces the stage everywhere,
   including in `spirv-llvm-translator`). Estimate: 20 to 40 minutes on a
   4-vCPU runner, once per full job on a cold cache, then cached in
   `buildcache-<flavor>-full`. Cost if wrong: if it takes longer, the cold
   COSMIC full prebuild (about 2.5 h of COSMIC plus its dependencies) is the
   job closest to the 330-minute limit; the fix is a `llvm-stack-full-dist`
   prebuild beside `llvm-dist`.
8. **The local default stays `GPU=vm`.** A local build, the test harnesses
   and the agent gates are VM work; the release sets GPU explicitly.

## Firmware (separate PR: fix/firmware-layout)

Measured while doing this: `FIRMWARE=true` did not work on an installed
system. The blobs went to `/usr/local/lib/firmware`, which Hadron v0.6.4
mounts COS_PERSISTENT over (kairos-io/kairos#4290), and the `iwlwifi-*.ucode`
pattern matched nothing since linux-firmware moved those files to
`intel/iwlwifi/`. That PR installs a pinned linux-firmware release into
`/usr/lib/firmware`, zstd-compressed, with WHENCE links. Subset: 988 MB of
blobs become 501 MB (measured in an alpine container, 11 s). This PR does not
depend on it textually, but the full line only gives working Wi-Fi and AMD
graphics on an installed machine with it.

## Size, estimated

v0.0.5 vm ISOs: sway 497 MB, i3 487 MB, niri 556 MB, cosmic 856 MB (before
llvmpipe added libLLVM, about 125 MB uncompressed). Full adds about 500 MB of
zstd firmware (which the ISO's squashfs does not shrink further) and a few
tens of MB of Mesa drivers and libelf: sway, i3 and niri about 1.1 GB, cosmic
about 1.4 GB. Not measured: no full image was built for this change.

## CI cost, estimated

From the v0.0.5 run (LLVM 81 min, sway 58, niri 150, agent 88) and the v0.0.7
run (LLVM 137 min, COSMIC halves about 150 min each).

| | Before | After |
| --- | --- | --- |
| Jobs | 1 LLVM, 2 COSMIC, 4 builds, agent, publish | 1 LLVM, 4 COSMIC, 8 builds, agent, publish |
| Cold wall time | about 7 h (LLVM, then COSMIC, then the cosmic build) | about 7.5 h: the same chain, plus SPIRV on the full side |
| Cold runner time | about 15 h | about 30 h |
| Warm wall time | about 1.5 h (the agent gates) | about 1.5 h |
| Warm runner time | about 2.5 h | about 3.5 h |

Every job stays inside 330 minutes; the closest is the cold COSMIC full
prebuild (see decision 7). Twelve jobs run at once at the peak, within the
free-plan limit of 20.

## Not verified

- No full or vm image was built locally (disk). The first rehearsal tag is the
  test: the four full build jobs, the four COSMIC prebuilds, the asset checks.
- GPU rendering with iris or radeonsi on real hardware.
- The ISO sizes above.
