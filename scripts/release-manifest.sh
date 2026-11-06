#!/usr/bin/env bash
# Generate a deterministic build manifest for one release image.
#
# Usage: release-manifest.sh <flavor> <gpu> <release-tag> <image> <iso-asset-path> <out.json>
#
#   flavor          sway | i3 | cosmic | niri | agent
#   gpu             full | vm: the image line (agent: vm)
#   release-tag     the release tag being published, e.g. v0.4.0
#   image           the final OCI image, e.g. i3-desktop:dev
#   iso-asset-path  path to the ISO; its sibling .sha256 must already exist
#   out.json        where to write the manifest
#
# Every field is read back from the thing that produced it -- the Makefile, the
# Dockerfiles, the built image -- rather than hardcoded here, so a manifest
# cannot quietly drift from what was actually built. Any field that cannot be
# resolved is a hard failure, EXCEPT the ones that legitimately do not apply to
# a flavor: those are emitted as JSON null rather than as an empty string, so a
# consumer can tell "not applicable" from "we failed to look".
#
# Only i3 and agent run the XLibre X server. Sway, COSMIC and niri are Wayland
# compositors (COSMIC and niri run X apps through Xwayland), so they carry no
# XLibre version.
# Only the agent image carries Cua and AT-SPI.
set -euo pipefail

die() { echo "release-manifest: $*" >&2; exit 1; }

# hadron-update decides an image is a Hadron Desktop one from these labels (the
# keys equal the constants in update/policy.go), so an image whose labels
# disagree with where it is published would make every installed system
# mis-detect it. Fail the release instead.
label_flavor=io.kairos.hadron-desktop.flavor
label_version=io.kairos.hadron-desktop.version
label_gpu=io.kairos.hadron-desktop.gpu
label_firmware=io.kairos.hadron-desktop.firmware

image_label() { # image, key
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" \
    || die "could not inspect the labels of $1"
}

# The line decides the tag the image is pushed as (<flavor>-<tag> or
# <flavor>-vm-<tag>), and installed systems follow the line their image says.
# A full image carries the firmware, a vm image does not.
check_image_labels() {
  local img="$1" want_flavor="$2" want_gpu="$3" want_version="$4" got want_firmware=false
  got="$(image_label "$img" "$label_flavor")" || exit 1
  [ "$got" = "$want_flavor" ] \
    || die "image $img has label $label_flavor='$got' but is being released as flavor '$want_flavor'"
  got="$(image_label "$img" "$label_version")" || exit 1
  [ "$got" = "$want_version" ] \
    || die "image $img has label $label_version='$got' but is being released as tag '$want_version'"
  got="$(image_label "$img" "$label_gpu")" || exit 1
  [ "$got" = "$want_gpu" ] \
    || die "image $img has label $label_gpu='$got' but is being released on the '$want_gpu' line"
  [ "$want_gpu" = full ] && want_firmware=true
  got="$(image_label "$img" "$label_firmware")" || exit 1
  [ "$got" = "$want_firmware" ] \
    || die "image $img has label $label_firmware='$got' but the '$want_gpu' line ships firmware=$want_firmware"
}

# Sourced (by the label test): define the functions and stop.
[[ "${BASH_SOURCE[0]}" != "$0" ]] && return 0

[ "$#" -eq 6 ] || die "usage: $0 <flavor> <gpu> <release-tag> <image> <iso-asset-path> <out.json>"
flavor="$1" gpu="$2" release_tag="$3" image="$4" iso_path="$5" out="$6"

case "$flavor" in
  sway|i3|cosmic|niri|agent) ;;
  *) die "unknown flavor: $flavor (want sway, i3, cosmic, niri, or agent)" ;;
esac
case "$flavor/$gpu" in
  agent/vm | sway/full | sway/vm | i3/full | i3/vm | cosmic/full | cosmic/vm | niri/full | niri/vm) ;;
  *) die "unknown line for $flavor: $gpu (want full or vm; agent is vm only)" ;;
esac
[ -f "$iso_path" ]           || die "no ISO at $iso_path"
[ -f "$iso_path.sha256" ]    || die "no checksum at $iso_path.sha256"
command -v jq >/dev/null     || die "jq is required"
command -v docker >/dev/null || die "docker is required"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Makefile: `BASE_IMAGE ?= ghcr.io/kairos-io/hadron:$(HADRON_VERSION)`. Asked of
# make itself, so the reference is the resolved one, not the template.
base_ref="$(make -s -C "$repo_root" print-base-image 2>/dev/null || true)"
[ -n "$base_ref" ] || die "could not read BASE_IMAGE from the Makefile"

# The base must be in the local image store, carrying the registry digest it was
# pulled at. BuildKit does NOT put a build's base image there, so the caller is
# responsible for `docker pull`ing it before the build -- see the "Pull the
# Hadron base" step in .github/workflows/release.yml. Inspect failures are
# reported rather than swallowed: `2>/dev/null` plus `set -e` on a bare
# assignment used to abort this script with no output at all.
base_inspect="$(
  docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$base_ref" 2>&1
)" || die "could not inspect $base_ref (pull it before building): $base_inspect"
base_digest="$(printf '%s\n' "$base_inspect" | head -n1 | cut -d@ -f2)"
[[ "$base_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "could not resolve a registry digest for $base_ref"

image_id="$(docker image inspect --format '{{.Id}}' "$image")" \
  || die "could not inspect the built image $image"
check_image_labels "$image" "$flavor" "$gpu" "$release_tag"
firmware=false
[ "$gpu" = full ] && firmware=true
iso_sha256="$(cut -d' ' -f1 < "$iso_path.sha256")"
[ -n "$iso_sha256" ] || die "empty checksum in $iso_path.sha256"

# Bare filename, not the build-time path: the release job verifies the manifest
# from inside the flat `release/` download dir, where every asset sits together.
iso_name="$(basename "$iso_path")"

git_commit="$(cd "$repo_root" && git rev-parse HEAD)"

# Dockerfile: `ARG XLIBRE_VERSION=...`. Only the i3 and agent images have it.
xlibre_version=null
if [ "$flavor" = "i3" ] || [ "$flavor" = "agent" ]; then
  raw="$(grep -oP '^ARG XLIBRE_VERSION=\K\S+' "$repo_root/Dockerfile" | head -n1 || true)"
  [ -n "$raw" ] || die "could not read ARG XLIBRE_VERSION from the Dockerfile"
  xlibre_version="$(jq -n --arg v "$raw" '$v')"
fi

# Dockerfile.agent: `ARG CUA_COMMIT=...` appears in more than one stage. They
# must agree, or the manifest would name a revision only half the image was
# built from.
cua_revision=null
atspi_version=null
if [ "$flavor" = "agent" ]; then
  mapfile -t cua_all < <(grep -oP '^ARG CUA_COMMIT=\K\S+' "$repo_root/Dockerfile.agent" | sort -u)
  [ "${#cua_all[@]}" -eq 1 ] \
    || die "Dockerfile.agent declares ${#cua_all[@]} distinct CUA_COMMIT values; expected exactly one"
  cua_revision="$(jq -n --arg v "${cua_all[0]}" '$v')"

  raw="$(grep -oP '^ARG ATSPI_VERSION=\K\S+' "$repo_root/Dockerfile.agent" | head -n1 || true)"
  [ -n "$raw" ] || die "could not read ARG ATSPI_VERSION from Dockerfile.agent"
  atspi_version="$(jq -n --arg v "$raw" '$v')"
fi

jq -n \
  --arg release_tag "$release_tag" \
  --arg git_commit "$git_commit" \
  --arg hadron_base_reference "$base_ref" \
  --arg hadron_base_digest "$base_digest" \
  --arg image_id "$image_id" \
  --arg iso "$iso_name" \
  --arg iso_sha256 "$iso_sha256" \
  --arg desktop_flavor "$flavor" \
  --arg gpu "$gpu" \
  --argjson firmware "$firmware" \
  --argjson xlibre_version "$xlibre_version" \
  --argjson cua_revision "$cua_revision" \
  --argjson atspi_version "$atspi_version" \
  '{
     release_tag: $release_tag,
     git_commit: $git_commit,
     hadron_base_reference: $hadron_base_reference,
     hadron_base_digest: $hadron_base_digest,
     image_id: $image_id,
     iso: $iso,
     iso_sha256: $iso_sha256,
     desktop_flavor: $desktop_flavor,
     gpu: $gpu,
     firmware: $firmware,
     xlibre_version: $xlibre_version,
     cua_revision: $cua_revision,
     atspi_version: $atspi_version
   }' > "$out"

echo "release-manifest: wrote $out" >&2
