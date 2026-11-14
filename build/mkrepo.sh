#!/bin/sh
# build/mkrepo.sh — build a fixture local apk repo at vm/repo/
# Produces: vm/repo/x86_64/dummy-1.0-r0.apk + vm/repo/x86_64/APKINDEX.tar.gz
# apk -X expects <repo>/x86_64/APKINDEX.tar.gz layout (confirmed via probe).
set -eu
REPO=$(cd "$(dirname "$0")/.." && pwd)
APK="$REPO/vm/apk-tools-static"
OUTDIR="$REPO/vm/repo/x86_64"
mkdir -p "$OUTDIR"

# Stage a minimal valid .PKGINFO for the dummy package
PKG=$(mktemp -d)
trap 'rm -rf "$PKG"' EXIT
cat > "$PKG/.PKGINFO" << 'PKGINFO'
pkgname = dummy
pkgver = 1.0-r0
arch = x86_64
size = 4096
pkgdesc = Kodos test fixture package
url = https://example.com
builddate = 1700000000
packager = kodos
PKGINFO

# Build unsigned .apk (tar.gz of the metadata tree)
# --allow-untrusted accepts unsigned packages throughout the kodos flow
(cd "$PKG" && tar czf "$OUTDIR/dummy-1.0-r0.apk" .PKGINFO)

# Build APKINDEX.tar.gz from the .apk
"$APK" index --allow-untrusted --no-warnings \
    -o "$OUTDIR/APKINDEX.tar.gz" \
    "$OUTDIR/dummy-1.0-r0.apk"

echo "vm/repo/x86_64/dummy-1.0-r0.apk and APKINDEX.tar.gz ready"
tar -xzOf "$OUTDIR/APKINDEX.tar.gz" APKINDEX
