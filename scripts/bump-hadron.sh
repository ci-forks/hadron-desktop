#!/usr/bin/env bash
# Move the build to another Hadron release, or check the pins agree.
#
#   scripts/bump-hadron.sh v0.6.4     # rewrite every pin to v0.6.4
#   scripts/bump-hadron.sh --check    # fail unless every pin is the same
#   scripts/bump-hadron.sh --current  # print the pinned version
#
# The base image and the toolchain must come from the same Hadron release (same
# musl/GCC ABI), and the agent overlay builds against the i3 image, so the one
# version lives in several places that must never disagree:
#   Makefile          HADRON_VERSION ?= vX
#   Dockerfile        ARG HADRON_VERSION=vX
#   Dockerfile.agent  ARG HADRON_VERSION=vX
#   test/agent/Dockerfile.compat  ARG HADRON_VERSION=vX  (agent gate fixtures)
set -euo pipefail
cd "$(dirname "$0")/.."

pins() {
    grep -oP '^HADRON_VERSION\s*\?=\s*\K\S+' Makefile
    grep -oP '^ARG HADRON_VERSION=\K\S+' Dockerfile
    grep -oP '^ARG HADRON_VERSION=\K\S+' Dockerfile.agent
    grep -oP '^ARG HADRON_VERSION=\K\S+' test/agent/Dockerfile.compat
}

check() {
    local n v
    n="$(pins | wc -l)"
    v="$(pins | sort -u)"
    if [ "$n" -ne 4 ] || [ "$(printf '%s\n' "$v" | wc -l)" -ne 1 ]; then
        echo "Hadron pins disagree or are missing (want one value in 4 places):" >&2
        echo "  Makefile:         $(grep -E '^HADRON_VERSION' Makefile || echo missing)" >&2
        echo "  Dockerfile:       $(grep -E '^ARG HADRON_VERSION=' Dockerfile || echo missing)" >&2
        echo "  Dockerfile.agent: $(grep -E '^ARG HADRON_VERSION=' Dockerfile.agent || echo missing)" >&2
        echo "  Dockerfile.compat: $(grep -E '^ARG HADRON_VERSION=' test/agent/Dockerfile.compat || echo missing)" >&2
        return 1
    fi
    printf '%s\n' "$v"
}

case "${1:-}" in
    --check)   check >/dev/null && echo "Hadron pins agree: $(check)" ;;
    --current) check ;;
    v[0-9]*)
        new="$1"
        [[ "$new" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "not a release tag: $new" >&2; exit 2; }
        check >/dev/null
        sed -i -E "s/^(HADRON_VERSION[[:space:]]*\?=[[:space:]]*).*/\1$new/" Makefile
        sed -i -E "s/^(ARG HADRON_VERSION=).*/\1$new/" Dockerfile Dockerfile.agent test/agent/Dockerfile.compat
        [ "$(check)" = "$new" ] || { echo "bump did not apply cleanly" >&2; exit 1; }
        echo "Hadron pinned to $new"
        ;;
    *) sed -n '2,7p' "$0" >&2; exit 2 ;;
esac
