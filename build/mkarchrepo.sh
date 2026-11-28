#!/bin/sh
# build/mkarchrepo.sh — build kodos and publish it as a LOCAL Arch-style
# package repo (a file:// mirror), so Arch machines self-manage kodos as a
# real package and every generation ships /usr/bin/kodos:
#
#   repos    = { kodos = { type = "arch",
#                          mirror = "file:///home/abuss/kodos-repo",
#                          branches = { "kodos" } } }
#   packages = { { repo = "kodos", name = "kodos" } }
#
# Produces: $OUTDIR/kodos/os/x86_64/kodos-<PKGVER>-<PKGREL>-x86_64.pkg.tar.zst
#           $OUTDIR/kodos/os/x86_64/kodos.db.tar.gz   (ALPM sync db)
# The executor's getRetry serves file:// URLs directly; %CHKSUM% is the real
# sha256, so the lockfile pin + verifySha256 chain stays intact.
set -eu
REPO=$(cd "$(dirname "$0")/.." && pwd)
OUTDIR=${OUTDIR:-$REPO/vm/archrepo}
DB=kodos                     # branch dir name inside the repo
PKGVER=${PKGVER:-0.1}
PKGREL=${PKGREL_OVERRIDE:-$(git -C "$REPO" rev-list --count HEAD 2>/dev/null || echo 0)}
VERSION="$PKGVER-$PKGREL"    # alpm shape: pkgver-pkgrel (numeric rel)
NAME="kodos-$VERSION-x86_64.pkg.tar.zst"
DEST="$OUTDIR/$DB/os/x86_64"
mkdir -p "$DEST"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/usr/bin"
(cd "$REPO/executor" && go build -o "$STAGE/usr/bin/kodos" .)
BINSIZE=$(wc -c < "$STAGE/usr/bin/kodos")

cat > "$STAGE/.PKGINFO" <<PKGINFO
pkgname = kodos
pkgver = $VERSION
pkgdesc = Kodos declarative system manager
url = https://github.com/abuss/kodos
size = $BINSIZE
arch = x86_64
depend = util-linux
builddate = $(date +%s)
packager = kodos
PKGINFO
tar --zstd -cf "$DEST/$NAME" -C "$STAGE" .PKGINFO ./usr/bin/kodos

SHA=$(sha256sum "$DEST/$NAME" | cut -d' ' -f1)
CSIZE=$(wc -c < "$DEST/$NAME")

DBDIR=$(mktemp -d)
mkdir -p "$DBDIR/$DB"
cat > "$DBDIR/$DB/desc" <<DESC
%FILENAME%
$DB/os/x86_64/$NAME
%NAME%
kodos
%VERSION%
$VERSION
%ARCH%
x86_64
%CSIZE%
$CSIZE
%ISIZE%
$BINSIZE
%CHKSUM%
$SHA
%DESC%
Kodos declarative system manager
%DEPENDS%
util-linux
DESC
tar czf "$DEST/$DB.db.tar.gz" -C "$DBDIR" "$DB"
rm -rf "$DBDIR"

echo "local arch repo ready: mirror=file://$OUTDIR branch=$DB pkg=kodos-$VERSION"
