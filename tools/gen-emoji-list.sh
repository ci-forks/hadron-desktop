#!/bin/sh
# Write the emoji list that hadron-emoji shows, from Unicode's emoji-test.txt.
#
#   tools/gen-emoji-list.sh emoji-test.txt > emoji.txt
#
# One line per fully-qualified emoji, in the file's order (CLDR order, which
# groups faces, people, animals and so on), as "<emoji> <name>":
#
#   1F600 ; fully-qualified # 😀 E1.0 grinning face  ->  😀 grinning face
#
# Components (skin tones and hair on their own), minimally-qualified and
# unqualified forms are left out: each has a fully-qualified form in the list
# already. The Dockerfile emoji stage runs this at build time.
set -eu

[ $# -eq 1 ] || { echo "usage: $0 emoji-test.txt" >&2; exit 2; }

sed -n 's/^[0-9A-F][0-9A-F ]*; fully-qualified *# \([^ ][^ ]*\) E[0-9][0-9.]* \(..*\)$/\1 \2/p' "$1"
