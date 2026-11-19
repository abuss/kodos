#!/bin/sh
# test-arch-media.sh — host-side checks for the Arch install media payload.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
TDIR=${TMPDIR:-/tmp/opencode}
STAGE=$TDIR/arch-stage-test
DESC_DIR=${ARCH_DESC_DIR:-/tmp/opencode/arch}
mkdir -p "$TDIR"

# Task 4 replaces these with the real installer and docs; staging copies them.
: > "$REPO/build/usb/arch-run-install.sh"
: > "$REPO/build/usb/README-arch.md"

sh -n "$REPO/build/usb/mkinstall-arch-usb.sh" || {
    echo "FAIL: staging script syntax"; exit 1;
}

if ARCH_DESC_DIR= PACMAN_BIN=/nonexistent STAGE_ONLY=1 OUT="$STAGE" \
        sh "$REPO/build/usb/mkinstall-arch-usb.sh" \
        > "$TDIR/arch-stage-no-pacman.log" 2>&1; then
    echo "FAIL: missing pacman was accepted"; exit 1
fi
grep -qi pacman "$TDIR/arch-stage-no-pacman.log" || {
    echo "FAIL: missing-pacman error does not name pacman"; exit 1
}

ARCH_DESC_DIR=$DESC_DIR STAGE_ONLY=1 OUT="$STAGE" \
    sh "$REPO/build/usb/mkinstall-arch-usb.sh" \
    > "$TDIR/arch-stage.log" 2>&1

for f in bin/kodos indexes/arch/core.desc indexes/arch/extra.desc \
         config/arch-machine.lua repo/x86_64/APKINDEX.tar.gz; do
    [ -s "$STAGE/$f" ] || { echo "FAIL: missing staged $f"; exit 1; }
done

cat > "$TDIR/fake-pacman.sh" <<'EOF'
#!/bin/sh
set -eu
conf=
while [ $# -gt 0 ]; do
    if [ "$1" = --config ]; then shift; conf=${1:-}; fi
    shift
done
db=$(sed -n 's/^DBPath = //p' "$conf" | head -1)
[ -n "$db" ] || { echo "fake pacman: no DBPath" >&2; exit 1; }
mkdir -p "$db/sync"
: > "$db/sync/core.db"
: > "$db/sync/extra.db"
exit 0
EOF
chmod +x "$TDIR/fake-pacman.sh"

if ARCH_DESC_DIR= PACMAN_BIN="$TDIR/fake-pacman.sh" STAGE_ONLY=1 OUT="$STAGE" \
        sh "$REPO/build/usb/mkinstall-arch-usb.sh" \
        > "$TDIR/arch-stage-empty-db.log" 2>&1; then
    echo "FAIL: empty sync DB was accepted"; exit 1
fi
grep -qi 'desc' "$TDIR/arch-stage-empty-db.log" || {
    echo "FAIL: empty-DB error does not name desc"; exit 1
}

# Restore a real staged payload for the later tasks.
ARCH_DESC_DIR=$DESC_DIR STAGE_ONLY=1 OUT="$STAGE" \
    sh "$REPO/build/usb/mkinstall-arch-usb.sh" \
    > "$TDIR/arch-stage-restore.log" 2>&1

echo "test-arch-media staging: ok"
