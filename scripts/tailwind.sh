#!/usr/bin/env sh
# Download (once) and run the pinned Tailwind standalone CLI. The version and checksum
# live here and nowhere else; the Makefile runs this inside the dev container and CI
# runs it directly.
set -e
VERSION=v4.3.3
SHA256=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a
BIN=${TAILWIND_BIN:-.cache/bin/tailwindcss}

if [ ! -x "$BIN" ]; then
  mkdir -p "$(dirname "$BIN")"
  curl -sSL -o "$BIN" "https://github.com/tailwindlabs/tailwindcss/releases/download/$VERSION/tailwindcss-linux-x64"
  echo "$SHA256  $BIN" | sha256sum -c - >/dev/null
  chmod +x "$BIN"
fi
exec "$BIN" -i internal/web/static/src/app.css -o internal/web/static/app.css --minify "$@"
