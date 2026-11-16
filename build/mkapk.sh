#!/bin/sh
# build/mkapk.sh — build kodos binary and package it as an apk into vm/repo/
# Produces: vm/repo/x86_64/kodos-<PKGVER>-r<PKGREL>.apk + rebuilds APKINDEX
# with BOTH fixture apks (dummy + kodos).
set -eu
REPO=$(cd "$(dirname "$0")/.." && pwd)
APK="$REPO/vm/apk-tools-static"
OUTDIR="$REPO/vm/repo/x86_64"
mkdir -p "$OUTDIR"

PKGVER=${PKGVER:-0.1.0}
PKGREL=${PKGREL_OVERRIDE:-$(git -C "$REPO" rev-list --count HEAD 2>/dev/null || echo 0)}
PKGFILE="kodos-${PKGVER}-r${PKGREL}.apk"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

# Build static Go binary
mkdir -p "$STAGE/usr/bin"
(cd "$REPO/executor" && CGO_ENABLED=0 go build -o "$STAGE/usr/bin/kodos" .)

# Write .PKGINFO
BINSIZE=$(wc -c < "$STAGE/usr/bin/kodos")
cat > "$STAGE/.PKGINFO" << PKGINFO
pkgname = kodos
pkgver = ${PKGVER}-r${PKGREL}
arch = x86_64
size = ${BINSIZE}
pkgdesc = Kodos declarative system manager
url = https://github.com/kodos
depend = util-linux
builddate = $(date +%s 2>/dev/null || echo 0)
packager = kodos
PKGINFO

# Build apk (tar.gz of stage tree; --owner/--group are GNU tar flags,
# available on dev hosts; avoids leaking the invoking uid into the archive)
(cd "$STAGE" && tar czf "$OUTDIR/$PKGFILE" \
    --owner=0 --group=0 \
    .PKGINFO \
    ./usr/bin/kodos)

# Rebuild APKINDEX over BOTH apks
"$APK" index --allow-untrusted --no-warnings \
    -o "$OUTDIR/APKINDEX.tar.gz" \
    "$OUTDIR"/dummy-*.apk \
    "$OUTDIR/$PKGFILE"

echo "vm/repo/x86_64/$PKGFILE and APKINDEX.tar.gz ready"
tar -xzOf "$OUTDIR/APKINDEX.tar.gz" APKINDEX
