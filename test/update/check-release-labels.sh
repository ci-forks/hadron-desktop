#!/usr/bin/env bash
# Prove scripts/release-manifest.sh refuses an image whose isotope
# labels disagree with the flavor, line and tag being released. Builds tiny
# `FROM scratch` images, so no Isotope image is needed.
#
#   test/update/check-release-labels.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../scripts/release-manifest.sh
source "$root/scripts/release-manifest.sh"
# The script's die() exits; run each check in a subshell to read its status.

tmp="$(mktemp -d)"
images=(none good goodvm wrong nogpu badfw server)
trap 'for i in "${images[@]}"; do docker rmi -f "isotope-label-test:$i" >/dev/null 2>&1 || true; done; rm -rf "$tmp"' EXIT

build() { # tag, label lines...
  local tag="$1"; shift
  { echo 'FROM scratch'; for l in "$@"; do echo "LABEL $l"; done; } > "$tmp/Dockerfile"
  docker build -q -t "$tag" "$tmp" >/dev/null
}
fail=0
expect() { # name, want-exit, image, flavor, gpu, tag
  local out rc=0
  out="$( (check_image_labels "$3" "$4" "$5" "$6") 2>&1)" || rc=$?
  if [ "$rc" -eq "$2" ]; then echo "ok   $1${out:+ -- $out}"; else echo "FAIL $1 (exit $rc, want $2): $out"; fail=1; fi
}

ns=io.kairos.isotope
build isotope-label-test:none   'x=y'
build isotope-label-test:good   $ns.flavor=i3 $ns.version=v1.2.3 $ns.gpu=full $ns.firmware=true
build isotope-label-test:goodvm $ns.flavor=i3 $ns.version=v1.2.3 $ns.gpu=vm $ns.firmware=false
build isotope-label-test:wrong  $ns.flavor=sway $ns.version=v1.2.2 $ns.gpu=full $ns.firmware=true
build isotope-label-test:nogpu  $ns.flavor=i3 $ns.version=v1.2.3
build isotope-label-test:badfw  $ns.flavor=i3 $ns.version=v1.2.3 $ns.gpu=full $ns.firmware=false
build isotope-label-test:server $ns.flavor=server $ns.version=v1.2.3 $ns.gpu=full $ns.firmware=true

expect "unlabeled image is refused"        1 isotope-label-test:none   i3 full v1.2.3
expect "wrong flavor is refused"           1 isotope-label-test:wrong  i3 full v1.2.2
expect "wrong version is refused"          1 isotope-label-test:wrong  sway full v1.2.3
expect "image without a line is refused"   1 isotope-label-test:nogpu  i3 full v1.2.3
expect "vm image as full is refused"       1 isotope-label-test:goodvm i3 full v1.2.3
expect "full image as vm is refused"       1 isotope-label-test:good   i3 vm v1.2.3
expect "full image without firmware fails" 1 isotope-label-test:badfw  i3 full v1.2.3
expect "matching full labels pass"         0 isotope-label-test:good   i3 full v1.2.3
expect "matching vm labels pass"           0 isotope-label-test:goodvm i3 vm v1.2.3
expect "server image as a desktop is refused" 1 isotope-label-test:server sway full v1.2.3
expect "matching server labels pass"       0 isotope-label-test:server server full v1.2.3
exit "$fail"
