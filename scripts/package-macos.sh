#!/usr/bin/env bash
set -euo pipefail
bin="$1"          # build/bin/nodeshell.app
out="$2"          # build/bin
name="$3"         # NodeShell-<version>-macos-arm64
sig_identity="${4:-}"  # optional signing identity; empty → ad-hoc test product
if [ -n "$sig_identity" ]; then
  codesign --force --deep --sign "$sig_identity" --options runtime "$bin"
else
  # Ad-hoc sign the app bundle (no signing material configured → test product)
  codesign --force --deep --sign - "$bin"
fi
# ZIP
ditto -c -k --keepParent "$bin" "$out/$name.zip"
# DMG
hdiutil create -volname "NodeShell" -srcfolder "$bin" -ov -format UDZO "$out/$name.dmg"
echo "Packaged $name: $out/$name.zip $out/$name.dmg"
