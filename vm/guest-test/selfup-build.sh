#!/bin/sh
# vm/guest-test/selfup-build.sh — HOST side of the self-upgrade round:
# build a NEWER kodos apk (default 0.2.0-r0) into vm/repo and copy it into the
# booted guest's own repo dir. Nothing else happens here: the index refresh
# and the apply that consumes it are the guest's job (30-selfup.sh).
set -eu
REPO=$(cd "$(dirname "$0")/../.." && pwd)
PKGVER=${PKGVER:-0.2.0}
APK="$REPO/vm/repo/x86_64/kodos-${PKGVER}-r0.apk"

cd "$REPO"
PKGVER=$PKGVER sh build/mkapk.sh
"$REPO/vm/run.sh" guest-file "$APK"
echo "now run in the guest:  vm/run.sh guest \"\$(cat vm/guest-test/30-selfup.sh)\""
