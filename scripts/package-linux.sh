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
# appimagetool requires a .desktop (and matching icon) at the AppDir root;
# also keep a copy under usr/share/applications for desktop integration / nfpm.
desktop_root="$appdir/nodeshell.desktop"
desktop="$appdir/usr/share/applications/nodeshell.desktop"
mkdir -p "$appdir/usr/bin" "$appdir/usr/share/applications"
cp "$bin" "$appdir/usr/bin/nodeshell"
cat > "$desktop_root" <<EOF
[Desktop Entry]
Name=NodeShell
Exec=nodeshell
Type=Application
Icon=nodeshell
Categories=Utility;
EOF
cp "$desktop_root" "$desktop"
cp build/appicon.png "$appdir/nodeshell.png"
ln -sfn nodeshell.png "$appdir/.DirIcon"
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
# nfpm v2 renamed the pacman packager to archlinux.
nfpm pkg --config "$out/nfpm.yaml" --packager archlinux --target "$out/$name.pkg.tar.zst"
echo "Packaged $name: $out/$name.AppImage $out/$name.deb $out/$name.pkg.tar.zst"
