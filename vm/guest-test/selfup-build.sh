#!/bin/sh
# vm/guest-test/selfup-build.sh — HOST side of the self-upgrade round:
# build a NEWER kodos apk (default 0.2.0-r<commit-count>) into vm/repo and
# push BOTH the new apk AND the host-rebuilt APKINDEX.tar.gz into the booted
# guest's /kod/repo/x86_64/. The guest's apk-tools 3.0.9 reads v2 repos fine
# but its `apk index` refuses v2-no-datahash packages; shipping the host-built
# index is therefore required (see NEXT.md for the deferred v3-format note).
set -eu
REPO=$(cd "$(dirname "$0")/../.." && pwd)
PKGVER=${PKGVER:-0.2.0}
PKGREL=$(git -C "$REPO" rev-list --count HEAD 2>/dev/null || echo 0)
APK="$REPO/vm/repo/x86_64/kodos-${PKGVER}-r${PKGREL}.apk"
IDX="$REPO/vm/repo/x86_64/APKINDEX.tar.gz"

cd "$REPO"
PKGVER=$PKGVER sh build/mkapk.sh
"$REPO/vm/run.sh" guest-file "$APK"
"$REPO/vm/run.sh" guest-file "$IDX"
echo "pushed: $(basename "$APK") + APKINDEX.tar.gz"
echo "now run in the guest:  vm/run.sh guest \"\$(cat vm/guest-test/30-selfup.sh)\""
