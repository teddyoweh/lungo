#!/bin/sh
# Ships a Lungo release: builds the app for Apple silicon and Intel, signs it with the
# Developer ID, packs a DMG (for people) and a zip (for the app's own updater), writes
# latest.json, tags the commit and publishes it all as a GitHub release. Running copies of
# Lungo pick it up within a few hours (desktop/update.go reads latest.json).
#
#   scripts/release.sh 0.1.4 "What changed, in a line or two."
set -eu

V=${1:?usage: scripts/release.sh X.Y.Z "notes"}
NOTES=${2:-}
V=${V#v}
IDENTITY=${LUNGO_SIGN_IDENTITY:-"Developer ID Application: Teddy Oweh (W5M7LV7263)"}
REPO=teddyoweh/lungo
cd "$(dirname "$0")/.."
ROOT=$PWD
export PATH="$HOME/go/bin:$PATH"

case $V in *[!0-9.]* | "" ) echo "version must look like 1.2.3" >&2; exit 1 ;; esac
if git rev-parse "v$V" >/dev/null 2>&1; then echo "v$V is already tagged" >&2; exit 1; fi
if [ -n "$(git status --porcelain -- desktop/wails.json)" ]; then
	echo "desktop/wails.json has changes of its own: commit or undo them first" >&2
	exit 1
fi

# The version goes in wails.json (Info.plist) and in the binary. Only that file is committed:
# other sessions may have work in progress in this checkout, and none of it ships.
sed -i '' "s/\"productVersion\": \"[^\"]*\"/\"productVersion\": \"$V\"/" desktop/wails.json
git commit -q -m "Lungo $V" -- desktop/wails.json
git tag -a "v$V" -m "Lungo $V"

# The build is made from a clean copy of what is committed (a worktree of the tag), never
# from this checkout's files.
SRC=$(mktemp -d)
git worktree add -q --detach "$SRC" "v$V"
trap 'git worktree remove --force "$SRC" 2>/dev/null' EXIT
ln -s "$ROOT/desktop/frontend/node_modules" "$SRC/desktop/frontend/node_modules"
# wails checks this to decide whether to run npm install; the packages are the same ones.
[ -f "$ROOT/desktop/frontend/package.json.md5" ] && cp "$ROOT/desktop/frontend/package.json.md5" "$SRC/desktop/frontend/"
(cd "$SRC/desktop" && wails build -clean -platform darwin/universal -ldflags "-X main.Version=$V")
rm -rf desktop/build/bin && mkdir -p desktop/build && cp -R "$SRC/desktop/build/bin" desktop/build/bin
APP=desktop/build/bin/Lungo.app
codesign --force --deep --options runtime --timestamp --entitlements desktop/build/darwin/entitlements.plist --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"

mkdir -p dist
ZIP=dist/Lungo-$V-mac.zip
DMG=dist/Lungo-$V.dmg
rm -f "$ZIP" "$DMG"
ditto -c -k --keepParent "$APP" "$ZIP"
STAGE=$(mktemp -d)
ditto "$APP" "$STAGE/Lungo.app"
ln -s /Applications "$STAGE/Applications"
hdiutil create -quiet -volname Lungo -srcfolder "$STAGE" -ov -format UDZO "$DMG"
rm -rf "$STAGE"
codesign --timestamp --sign "$IDENTITY" "$DMG"

SUM=$(shasum -a 256 "$ZIP" | cut -d' ' -f1)
SIZE=$(stat -f%z "$ZIP")
python3 -I - "$V" "$NOTES" "https://github.com/$REPO/releases/download/v$V/Lungo-$V-mac.zip" "$SUM" "$SIZE" > dist/latest.json <<'PY'
import json, sys
v, notes, url, sha, size = sys.argv[1:]
print(json.dumps({"version": v, "notes": notes, "mac": {"url": url, "sha256": sha, "size": int(size)}}, indent=2))
PY

git push -q origin HEAD "v$V"
gh release create "v$V" "$DMG" "$ZIP" dist/latest.json -R "$REPO" --title "Lungo $V" --notes "${NOTES:-Lungo $V}

Universal (Apple silicon and Intel), signed with Developer ID. Running copies of Lungo update themselves; to install by hand, open the DMG and drag Lungo to Applications."
echo "released Lungo $V: https://github.com/$REPO/releases/tag/v$V"
