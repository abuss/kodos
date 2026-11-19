#!/bin/sh
# test-arch-media.sh — host-side checks for the Arch install media payload.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
TDIR=${TMPDIR:-/tmp/opencode}
STAGE=$TDIR/arch-stage-test
PAYLOAD=$STAGE
DESC_DIR=${ARCH_DESC_DIR:-/tmp/opencode/arch}
export ARCH_DESC_DIR=$DESC_DIR
mkdir -p "$TDIR"

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

sh -n "$REPO/build/usb/mkinstall-arch-iso.sh"
ISO=$TDIR/kodos-arch-install-test.iso
STAGE=$STAGE STAGE_SCRIPT="$REPO/build/usb/mkinstall-arch-usb.sh" \
    sh "$REPO/build/usb/mkinstall-iso.sh" "$ISO" >/dev/null
[ -s "$ISO" ] || { echo "no Arch ISO"; exit 1; }
xorriso -indev "$ISO" -find /kodos -maxdepth 1 2>/dev/null | grep -q arch-run-install.sh \
    || { echo "ISO missing /kodos payload"; exit 1; }
git -C "$REPO" diff --exit-code -- build/usb/mkinstall-usb.sh build/usb/run-install.sh build/usb/config \
    || { echo "Alpine media changed"; exit 1; }
grep -Fq 'STAGE_SCRIPT:-$REPO/build/usb/mkinstall-usb.sh' "$REPO/build/usb/mkinstall-iso.sh" \
    || { echo "ISO builder default override missing"; exit 1; }

DRY=$("$PAYLOAD/arch-run-install.sh" --dry-run --device /dev/nbd99 2>&1)
printf '%s\n' "$DRY" | grep -q 'DRY-RUN configure_repos' \
  || { echo "dry-run has no repo op"; exit 1; }
printf '%s\n' "$DRY" | grep -q 'https://mirror.rackspace.com/archlinux' \
  || { echo "dry-run lost Arch mirror"; exit 1; }
printf '%s\n' "$DRY" | grep -q 'DRY-RUN install_packages' \
  || { echo "dry-run has no package op"; exit 1; }
if printf '%s\n' "$DRY" | grep -qE 'APKINDEX|"type": "apk"|file:///'; then
  echo "Arch dry-run leaked apk/local-repo ops"; exit 1
fi
mkdir -p "$TDIR/fakebin"
printf '#!/bin/sh\n[ "$1" = -n -o "$1" = --mountpoint ] && echo /dev/nbd99\nexit 0\n' > "$TDIR/fakebin/findmnt"
printf '#!/bin/sh\necho nbd99\nexit 0\n' > "$TDIR/fakebin/lsblk"
chmod +x "$TDIR/fakebin/findmnt" "$TDIR/fakebin/lsblk"
if PATH="$TDIR/fakebin:$PATH" "$PAYLOAD/arch-run-install.sh" --dry-run --device /dev/nbd99 2>&1 | grep -q 'USB stick'; then
  : # expected refusal
else
  echo "installer did not refuse the live stick"; exit 1
fi

echo "test-arch-media installer: ok"
