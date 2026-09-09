#!/usr/bin/env sh
# Vendor the Web Awesome components the UI uses, into internal/web/static/wa. Only the
# components named here and the chunks they import are kept: the whole package is ~13MB
# and all but a fraction of it is components, React wrappers and type declarations this
# app never asks for. The result is committed, so a build needs nothing from the network.
#
# The version, its checksum and the component list live here and nowhere else. Re-run
# after changing any of them.
set -e
VERSION=3.12.0
SHA256=8fb34b5d18c0161bf934d264d39dae649aabd8f4e31135e9cd8bfbae5fa3078d
COMPONENTS="dropdown dropdown-item dialog divider icon popup"
OUT=internal/web/static/wa

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -sSL -o "$TMP/wa.tgz" "https://registry.npmjs.org/@awesome.me/webawesome/-/webawesome-$VERSION.tgz"
echo "$SHA256  $TMP/wa.tgz" | sha256sum -c - >/dev/null || {
  echo "webawesome.sh: checksum mismatch downloading web awesome $VERSION" >&2; exit 1; }
tar xzf "$TMP/wa.tgz" -C "$TMP"
SRC=$TMP/package/dist-cdn

rm -rf "$OUT"
mkdir -p "$OUT"
# Follow the imports from each component so the chunks it needs come with it.
python3 - "$SRC" "$OUT" $COMPONENTS <<'PY'
import os, re, shutil, sys
src, out, components = sys.argv[1], sys.argv[2], sys.argv[3:]
seen = set()
def walk(path):
    path = os.path.normpath(path)
    if path in seen or not os.path.exists(path):
        return
    seen.add(path)
    body = open(path, encoding="utf-8", errors="ignore").read()
    for a, b in re.findall(r'from\s*"([^"]+)"|import\s*"([^"]+)"', body):
        rel = a or b
        if rel.startswith("."):
            walk(os.path.join(os.path.dirname(path), rel))
for c in components:
    walk(os.path.join(src, "components", c, c + ".js"))
for path in sorted(seen):
    dst = os.path.join(out, os.path.relpath(path, src))
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    shutil.copy2(path, dst)
print(len(seen), "files")
PY
# Only the cascade layer order the components rely on, and the theme tokens. Web
# Awesome's native.css and utilities.css restyle bare elements across the page, which is
# Tailwind's job here.
mkdir -p "$OUT/styles/themes"
cp "$SRC/styles/layers.css" "$OUT/styles/layers.css"
cp "$SRC/styles/themes/default.css" "$OUT/styles/themes/default.css"
cp "$TMP/package/LICENSE.md" "$OUT/LICENSE.md"
du -sh "$OUT"
