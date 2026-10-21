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
# hdiutil occasionally reports "Resource busy" on shared macOS runners.
# Retry after detaching a stale mount and removing the partial image.
dmg="$out/$name.dmg"
created=0
for attempt in 1 2 3; do
  rm -f "$dmg"
  if hdiutil create -volname "NodeShell" -srcfolder "$bin" -ov -format UDZO "$dmg"; then
    created=1
    break
  fi
  echo "hdiutil create failed (attempt $attempt), retrying..." >&2
  hdiutil detach "/Volumes/NodeShell" 2>/dev/null || true
  sleep 2
done
if [ "$created" -ne 1 ]; then
  echo "failed to create $dmg" >&2
  exit 1
fi
echo "Packaged $name: $out/$name.zip $dmg"
