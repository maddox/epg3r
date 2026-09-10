#!/usr/bin/env sh
# Download (once) and run the pinned Tailwind standalone CLI for this machine's
# architecture. The version and checksums live here and nowhere else; the Makefile
# runs this inside the dev container and CI runs it directly.
set -e
VERSION=v4.3.3
case "$(uname -m)" in
  x86_64|amd64)  ARCH=x64;   SHA256=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
  aarch64|arm64) ARCH=arm64; SHA256=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
  *) echo "tailwind.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
# The cache path names everything that determines the binary, so a version bump or a
# different architecture downloads afresh rather than failing a checksum.
BIN=${TAILWIND_BIN:-.cache/bin/tailwindcss-$VERSION-linux-$ARCH}

if [ ! -x "$BIN" ]; then
  mkdir -p "$(dirname "$BIN")"
  curl -sSL -o "$BIN.part" "https://github.com/tailwindlabs/tailwindcss/releases/download/$VERSION/tailwindcss-linux-$ARCH"
  echo "$SHA256  $BIN.part" | sha256sum -c - >/dev/null || { rm -f "$BIN.part"; echo "tailwind.sh: checksum mismatch downloading tailwind $VERSION" >&2; exit 1; }
  chmod +x "$BIN.part" && mv "$BIN.part" "$BIN"
fi
exec "$BIN" -i internal/web/static/src/app.css -o internal/web/static/app.css --minify "$@"
