#!/bin/sh
# Build one COSMIC component from its epoch tag and install it under $COSMIC_OUT.
#
#   cosmic-build.sh <repo> [just recipe]
#
# Used by the cosmic stages of the Dockerfile, one RUN per component, so a
# failure re-runs only that component. Every component's justfile takes
# `rootdir` and honours CARGO_TARGET_DIR, which the Dockerfile points at a
# BuildKit cache mount shared by all components: they pin the same libcosmic
# and iced revisions within an epoch, so most of the dependency graph compiles
# once. Nothing from the target dir reaches a layer.
#
# Every installed ELF is checked with ldd; a library that only the build stage
# had would otherwise surface as a desktop that fails to start.
set -eu

repo=$1
recipe=${2:-install}
: "${COSMIC_VERSION:?}" "${COSMIC_OUT:?}"

cd /build
rm -rf "$repo"
mkdir "$repo"
curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors \
    "https://github.com/pop-os/${repo}/archive/refs/tags/epoch-${COSMIC_VERSION}.tar.gz" |
    tar -xz --strip-components=1 -C "$repo"
cd "$repo"

# vergen asks git for these; there is no .git in a tarball.
export VERGEN_GIT_COMMIT_DATE="1970-01-01" VERGEN_GIT_SHA="epoch-${COSMIC_VERSION}"

cargo build --release --locked ${COSMIC_CARGO_ARGS:-}
mkdir -p "$COSMIC_OUT"
just rootdir="$COSMIC_OUT" "$recipe"

find "$COSMIC_OUT" -type f -perm -u+x | while read -r f; do
    if file "$f" 2>/dev/null | grep -q ELF || head -c4 "$f" | grep -q ELF; then
        if ldd "$f" 2>&1 | grep -q 'not found'; then
            echo "missing libraries for $f:" >&2
            ldd "$f" | grep 'not found' >&2
            exit 1
        fi
    fi
done

cd /build
rm -rf "$repo"
