#!/usr/bin/env bash
set -euo pipefail
bin="$1"          # build/bin/nodeshell.app
out="$2"          # build/bin
name="$3"         # NodeShell-2.0.0-macos-arm64
# Ad-hoc sign the app bundle (no signing material configured → test product)
codesign --force --deep --sign - "$bin"
# ZIP
ditto -c -k --keepParent "$bin" "$out/$name.zip"
# DMG
hdiutil create -volname "NodeShell" -srcfolder "$bin" -ov -format UDZO "$out/$name.dmg"
# APPLE_SIGN injection point — notarization/staple would go here if configured
echo "Packaged $name: $out/$name.zip $out/$name.dmg"
