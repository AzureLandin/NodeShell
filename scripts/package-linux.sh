#!/usr/bin/env bash
set -euo pipefail
bin="$1"          # build/bin/nodeshell
out="$2"          # build/bin
name="$3"         # NodeShell-<version>-linux-amd64
arch="${4:-amd64}"
version="${5:-}"
if [ -z "$version" ]; then
  version="$(node -p "require('./package.json').version" 2>/dev/null || echo '0.0.0')"
fi
appdir="$out/$name.AppDir"
desktop="$appdir/usr/share/applications/nodeshell.desktop"
mkdir -p "$appdir/usr/bin"
cp "$bin" "$appdir/usr/bin/nodeshell"
mkdir -p "$appdir/usr/share/applications"
cat > "$desktop" <<EOF
[Desktop Entry]
Name=NodeShell
Exec=nodeshell
Type=Application
Icon=nodeshell
Categories=Utility;
EOF
cp build/appicon.png "$appdir/nodeshell.png"
cat > "$appdir/AppRun" <<EOF
#!/usr/bin/env bash
exec "\$(dirname "\$0")/usr/bin/nodeshell" "\$@"
EOF
chmod +x "$appdir/AppRun"
# appimagetool ships as an AppImage; ubuntu-24.04 lacks FUSE, extract-and-run avoids it
APPIMAGE_EXTRACT_AND_RUN=1 appimagetool "$appdir" "$out/$name.AppImage"
# deb/pacman via nfpm
cat > "$out/nfpm.yaml" <<EOF
name: nodeshell
arch: $arch
platform: linux
version: $version
maintainer: AzureLandin
description: NodeShell SSH client
homepage: https://github.com/AzureLandin/Simple-SSH-Client
license: MIT
contents:
  - src: $bin
    dst: /usr/bin/nodeshell
  - src: $desktop
    dst: /usr/share/applications/nodeshell.desktop
  - src: build/appicon.png
    dst: /usr/share/icons/nodeshell.png
EOF
nfpm pkg --config "$out/nfpm.yaml" --packager deb --target "$out/$name.deb"
nfpm pkg --config "$out/nfpm.yaml" --packager pacman --target "$out/$name.pkg.tar.zst"
echo "Packaged $name: $out/$name.AppImage $out/$name.deb $out/$name.pkg.tar.zst"
