#!/usr/bin/env sh
# Vendor the one typeface the generated art draws with, into internal/art/data. The result
# is committed, so a build needs nothing from the network and the binary carries the face
# with it — there is no font on a distroless image to fall back to.
#
# Barlow Condensed Bold, SIL OFL 1.1. Condensed because the same plate has to hold "BUF"
# and "SOUTHEASTERN LOUISIANA"; the static instance because x/image/font/sfnt does not
# instance variable-font axes, which rules out most of the obvious alternatives.
#
# The version and its checksum live here and nowhere else. Re-run after changing either.
set -e
VERSION=1.4
SHA256=e476562ec9c1e16cf16475895b511f08c804f438cc9a9f80a44ea50a0eeb5b65
BASE=https://raw.githubusercontent.com/google/fonts/main/ofl/barlowcondensed
OUT=internal/art/data

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -sSL -o "$TMP/font.ttf" "$BASE/BarlowCondensed-Bold.ttf"
echo "$SHA256  $TMP/font.ttf" | sha256sum -c - >/dev/null || {
  echo "font.sh: checksum mismatch downloading Barlow Condensed $VERSION" >&2; exit 1; }
curl -sSL -o "$TMP/OFL.txt" "$BASE/OFL.txt"

mkdir -p "$OUT"
cp "$TMP/font.ttf" "$OUT/BarlowCondensed-Bold.ttf"
cp "$TMP/OFL.txt" "$OUT/OFL.txt"   # the license travels with the font it covers
ls -l "$OUT"
