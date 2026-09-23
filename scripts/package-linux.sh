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
# rpm: nfpm does no ELF dependency scanning, so derive requires from the
# binary's NEEDED sonames the way rpmbuild's auto-requires would — dnf then
# resolves GTK/WebKit by capability on any rpm distro. Only the rpm packager
# gets these; deb/arch keep their existing dependency metadata. readelf
# localizes its output (e.g. "共享库" for "Shared library"), so pin LC_ALL=C.
soname_suffix=""
if [ "$(LC_ALL=C readelf -h "$bin" | awk '/Class:/{print $2}')" = "ELF64" ]; then
  soname_suffix="()(64bit)"
fi
rpm_depends="$(LC_ALL=C readelf -d "$bin" | sed -ne 's/.*Shared library: \[\(.*\)\]/\1/p' | sed "s|\$|${soname_suffix}|" | sed 's/^/      - /')"
# deb/pacman/rpm via nfpm
cat > "$out/nfpm.yaml" <<EOF
name: nodeshell
arch: $arch
platform: linux
version: $version
maintainer: AzureLandin
description: NodeShell SSH client
homepage: https://github.com/AzureLandin/NodeShell
license: MIT
overrides:
  rpm:
    depends:
${rpm_depends}
contents:
  - src: $bin
    dst: /usr/bin/nodeshell
  - src: $desktop
    dst: /usr/share/applications/nodeshell.desktop
  - src: build/appicon.png
    dst: /usr/share/icons/hicolor/256x256/apps/nodeshell.png
EOF
nfpm pkg --config "$out/nfpm.yaml" --packager deb --target "$out/$name.deb"
# nfpm v2 renamed the pacman packager to archlinux.
nfpm pkg --config "$out/nfpm.yaml" --packager archlinux --target "$out/$name.pkg.tar.zst"
nfpm pkg --config "$out/nfpm.yaml" --packager rpm --target "$out/$name.rpm"
echo "Packaged $name: $out/$name.AppImage $out/$name.deb $out/$name.pkg.tar.zst $out/$name.rpm"
