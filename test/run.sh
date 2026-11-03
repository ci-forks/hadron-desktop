#!/usr/bin/env bash
# End-to-end test harness for the Hadron sway-desktop example.
#
#   build example image -> wrap with Kairos init -> inject test instrumentation
#   -> build bootable ISO with AuroraBoot -> boot headless in QEMU -> parse
#   "SWAYTEST:" markers off the serial console -> pass/fail.
#
# Usage:
#   test/run.sh                 # full run, milestone M0
#   MILESTONE=M1 test/run.sh    # assert up to M1
#   SKIP_BUILD=1 ...                                  # reuse existing images
#   SKIP_ISO=1   ...                                  # reuse existing ISO
#   KEEP=1       ...                                  # don't delete artifacts
#
# Exit code 0 = all asserted milestones passed, no FAILs. Non-zero otherwise.
set -uo pipefail

# --- locate repo root (one dir up from this script) -------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
EXAMPLE_DIR="$REPO_ROOT"        # the repo root IS the desktop image build context
cd "$REPO_ROOT"

MILESTONE="${MILESTONE:-M0}"
BASE_IMAGE="${BASE_IMAGE:-$(make -s -C "$REPO_ROOT" print-base-image)}"
AURORA_IMAGE="${AURORA_IMAGE:-quay.io/kairos/auroraboot:v0.29.0}"

# The production image already folds in the Kairos init layer (final Dockerfile
# stage), so it is bootable on its own — no separate wrap step.
PROD_IMAGE="sway-desktop:dev"
TEST_IMAGE="sway-desktop-test:dev"

WORK="$REPO_ROOT/build/sway-desktop"
ART="$SCRIPT_DIR/artifacts"
ISO_DIR="$WORK/iso"
CONSOLE="$ART/console-$MILESTONE.log"
SCRATCH="$WORK/scratch.img"
mkdir -p "$WORK" "$ART" "$ISO_DIR"

MEM="${MEM:-4096}"
CPUS="${CPUS:-4}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-600}"

log() { echo -e "\n\033[1;34m[harness]\033[0m $*"; }
err() { echo -e "\033[1;31m[harness] $*\033[0m" >&2; }

# ---------------------------------------------------------------------------
# 1. Build the example (production) image
# ---------------------------------------------------------------------------
if [ "${SKIP_BUILD:-0}" != "1" ]; then
  log "Building example image ($PROD_IMAGE, Kairos init layer folded in)"
  docker build --build-arg BASE_IMAGE="$BASE_IMAGE" -t "$PROD_IMAGE" "$EXAMPLE_DIR" || { err "example build failed"; exit 1; }

  log "Injecting test instrumentation ($TEST_IMAGE, milestone=$MILESTONE)"
  docker build -t "$TEST_IMAGE" -f "$EXAMPLE_DIR/test/Dockerfile.test" \
    --build-arg BASE_IMAGE="$PROD_IMAGE" \
    --build-arg MILESTONE="$MILESTONE" \
    "$EXAMPLE_DIR" || { err "test overlay build failed"; exit 1; }
fi

# ---------------------------------------------------------------------------
# 2. Build a bootable ISO with AuroraBoot
# ---------------------------------------------------------------------------
if [ "${SKIP_ISO:-0}" != "1" ]; then
  log "Building ISO with AuroraBoot"
  rm -f "$ISO_DIR"/*.iso
  docker run --rm --privileged \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$ISO_DIR":/output \
    "$AURORA_IMAGE" build-iso --output /output/ "docker:$TEST_IMAGE" \
    || { err "AuroraBoot ISO build failed"; exit 1; }
fi
ISO="$(ls -t "$ISO_DIR"/*.iso 2>/dev/null | head -1)"
[ -n "$ISO" ] || { err "no ISO produced"; exit 1; }
log "ISO: $ISO"

# ---------------------------------------------------------------------------
# 3. Boot headless in QEMU
# ---------------------------------------------------------------------------
log "Creating scratch disk + booting QEMU (timeout ${BOOT_TIMEOUT}s)"
rm -f "$CONSOLE"
qemu-img create -f raw "$SCRATCH" 64M >/dev/null
touch "$CONSOLE"

ACCEL=(); [ -e /dev/kvm ] && ACCEL=(-enable-kvm -cpu host)

qemu-system-x86_64 \
  "${ACCEL[@]}" \
  -m "$MEM" -smp "$CPUS" \
  -display none \
  -vga virtio \
  -serial "file:$CONSOLE" \
  -rtc base=utc,clock=rt \
  -netdev user,id=net0 -device virtio-net-pci,netdev=net0 \
  -audiodev none,id=snd0 -device intel-hda -device hda-output,audiodev=snd0 \
  -drive if=none,id=scratch,format=raw,file="$SCRATCH" \
  -device virtio-blk-pci,drive=scratch,serial=swayscratch \
  -cdrom "$ISO" \
  -boot d \
  >"$ART/qemu-$MILESTONE.log" 2>&1 &
QEMU_PID=$!

# ---------------------------------------------------------------------------
# 4. Wait for completion (DONE marker or QEMU exit or timeout)
# ---------------------------------------------------------------------------
deadline=$((SECONDS + BOOT_TIMEOUT))
while [ "$SECONDS" -lt "$deadline" ]; do
  if grep -q "SWAYTEST: DONE" "$CONSOLE" 2>/dev/null; then log "Guest reported DONE"; break; fi
  if ! kill -0 "$QEMU_PID" 2>/dev/null; then log "QEMU exited"; break; fi
  sleep 3
done
kill "$QEMU_PID" 2>/dev/null; wait "$QEMU_PID" 2>/dev/null

# Extract any screenshots the guest wrote to the scratch disk (raw tar at offset 0)
if tar -tf "$SCRATCH" >/dev/null 2>&1; then
  tar -xf "$SCRATCH" -C "$ART" 2>/dev/null && log "Extracted screenshot(s) to $ART"
fi

# ---------------------------------------------------------------------------
# 5. Evaluate markers
# ---------------------------------------------------------------------------
log "Markers from $CONSOLE:"
grep "SWAYTEST:" "$CONSOLE" 2>/dev/null | sed 's/^/    /' || true

if ! grep -q "SWAYTEST: BEGIN" "$CONSOLE" 2>/dev/null; then
  err "RESULT: FAIL — guest never reached the test (no BEGIN marker). See $CONSOLE / qemu-$MILESTONE.log"
  exit 2
fi
if grep -q "SWAYTEST: FAIL" "$CONSOLE" 2>/dev/null; then
  err "RESULT: FAIL — one or more assertions failed."
  exit 1
fi
if ! grep -q "SWAYTEST: DONE" "$CONSOLE" 2>/dev/null; then
  err "RESULT: FAIL — guest did not finish (no DONE marker; likely hung/timeout)."
  exit 3
fi
log "RESULT: PASS — milestone $MILESTONE green."
exit 0
