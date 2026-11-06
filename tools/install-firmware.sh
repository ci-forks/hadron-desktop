#!/bin/sh
# Install part of an unpacked linux-firmware release, compressed with zstd.
#
#   install-firmware.sh SRC DEST PREFIX...
#
# SRC is the unpacked release tarball, DEST the firmware directory to fill
# (for example /firmware/usr/lib/firmware). Installs every file and link that
# SRC/WHENCE lists under a path starting with one of the PREFIXes, the way
# upstream's `copy-firmware.sh --zstd` installs all of them: File entries are
# compressed to <name>.zst, RawFile entries are copied as they are, and Link
# entries become symlinks (the tarball has no symlinks of its own; most
# nvidia/<chip> directories, for example, exist only as links). The release
# copy-firmware.sh has no option to install a subset, hence this script.
#
# A PREFIX that matches no File entry fails the install: upstream moves
# directories (iwlwifi-*.ucode moved to intel/iwlwifi/), and a pattern that
# silently matches nothing ships an image without that hardware's firmware.
set -eu

[ "$#" -ge 3 ] || { echo "usage: $0 SRC DEST PREFIX..." >&2; exit 2; }
src="$1" dest="$2"
shift 2
mkdir -p "$dest"
dest="$(cd "$dest" && pwd)"
cd "$src"
[ -f WHENCE ] || { echo "install-firmware: no WHENCE in $src" >&2; exit 1; }

tab="$(printf '\t')"
for prefix in "$@"; do
  # One tab-separated line per entry: kind, path, link target. index() and
  # not a regex, so a prefix is matched literally.
  entries="$(awk -v p="$prefix" '
    /^(RawFile|File):/ {
      kind = $1; sub(/:$/, "", kind)
      line = $0; sub(/^[A-Za-z]+: */, "", line); gsub(/"/, "", line)
      if (index(line, p) == 1) printf "%s\t%s\t\n", kind, line
    }
    /^Link:/ {
      line = $0; sub(/^Link: */, "", line); gsub(/"/, "", line)
      n = split(line, a, / -> /)
      if (n == 2 && index(a[1], p) == 1) printf "Link\t%s\t%s\n", a[1], a[2]
    }' WHENCE)"
  if ! printf '%s\n' "$entries" | grep -q '^\(Raw\)\{0,1\}File'; then
    echo "install-firmware: WHENCE lists no file under '$prefix'" >&2
    exit 1
  fi

  # Files first, so every link below can see its target.
  printf '%s\n' "$entries" | while IFS="$tab" read -r kind path target; do
    case "$kind" in
      File)
        mkdir -p "$dest/$(dirname "$path")"
        zstd --compress --quiet --force "$path" -o "$dest/$path.zst"
        ;;
      RawFile)
        mkdir -p "$dest/$(dirname "$path")"
        cp "$path" "$dest/$path"
        ;;
    esac
  done
  # Twice, because a link may point at another link that comes later in
  # WHENCE; only the second pass reports a link it cannot make.
  for pass in 1 2; do
    printf '%s\n' "$entries" | while IFS="$tab" read -r kind path target; do
      [ "$kind" = Link ] || continue
      dir="$dest/$(dirname "$path")"
      mkdir -p "$dir"
      resolved="$(cd "$dir" && realpath -m -s "$target")"
      if [ -e "$resolved" ]; then
        # A directory link, or a link to a RawFile.
        ln -sfn "$target" "$dest/$path"
      elif [ -e "$resolved.zst" ]; then
        ln -sfn "$target.zst" "$dest/$path.zst"
      elif [ "$pass" = 2 ]; then
        # Its target is outside the prefixes being installed.
        echo "install-firmware: skipping link $path -> $target (target not installed)" >&2
      fi
    done
  done
done
