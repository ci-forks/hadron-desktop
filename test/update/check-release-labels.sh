#!/usr/bin/env bash
# Prove scripts/release-manifest.sh refuses an image whose hadron-desktop
# labels disagree with the flavor and tag being released. Builds tiny
# `FROM scratch` images, so no Hadron image is needed.
#
#   test/update/check-release-labels.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../scripts/release-manifest.sh
source "$root/scripts/release-manifest.sh"
# The script's die() exits; run each check in a subshell to read its status.

tmp="$(mktemp -d)"
trap 'docker rmi -f hadron-label-test:none hadron-label-test:good hadron-label-test:wrong >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT

build() { # tag, label lines...
  local tag="$1"; shift
  { echo 'FROM scratch'; for l in "$@"; do echo "LABEL $l"; done; } > "$tmp/Dockerfile"
  docker build -q -t "$tag" "$tmp" >/dev/null
}
fail=0
expect() { # name, want-exit, image, flavor, tag
  local out rc=0
  out="$( (check_image_labels "$3" "$4" "$5") 2>&1)" || rc=$?
  if [ "$rc" -eq "$2" ]; then echo "ok   $1${out:+ -- $out}"; else echo "FAIL $1 (exit $rc, want $2): $out"; fail=1; fi
}

build hadron-label-test:none  'x=y'
build hadron-label-test:good  io.kairos.hadron-desktop.flavor=i3 io.kairos.hadron-desktop.version=v1.2.3
build hadron-label-test:wrong io.kairos.hadron-desktop.flavor=sway io.kairos.hadron-desktop.version=v1.2.2

expect "unlabeled image is refused"    1 hadron-label-test:none  i3 v1.2.3
expect "wrong flavor is refused"       1 hadron-label-test:wrong i3 v1.2.2
expect "wrong version is refused"      1 hadron-label-test:wrong sway v1.2.3
expect "matching labels pass"          0 hadron-label-test:good  i3 v1.2.3
exit "$fail"
