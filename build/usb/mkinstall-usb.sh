#!/bin/sh
# build/usb/mkinstall-usb.sh — build the bare-metal kodos install USB.
#
#   sh build/usb/mkinstall-usb.sh /dev/sdX     # whole stick, typed confirm, ERASED
#   STAGE_ONLY=1 sh build/usb/mkinstall-usb.sh # stage payload dir only (testing)
#
# Payload staged under build/usb/stage (OUT=... overrides): bin/kodos (static,
# from executor/), repo/x86_64 (kodos.apk + APKINDEX via build/mkapk.sh),
# indexes/ (3.24 main+community snapshots, cached in build/usb/iso/), config/
# (bare-metal layer + target config), run-install.sh.
#
# The stick = Alpine standard ISO dd'd (El Torito: UEFI+BIOS boot from the ISO
# filesystem) + one ext4 partition labelled KODOS carved into the space after
# the ISO, holding the payload. Boot the stick, bring up Ethernet, then:
#   mount -L KODOS /usb && /usb/run-install.sh
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
REL=${ALPINE_REL:-3.24.4}
CACHE=$REPO/build/usb/iso
OUT=${OUT:-$REPO/build/usb/stage}
ISO=$CACHE/alpine-standard-$REL-x86_64.iso
ISO_URL=https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/x86_64/alpine-standard-$REL-x86_64.iso

stage_payload() {
    rm -rf "$OUT"
    mkdir -p "$OUT/bin" "$OUT/repo/x86_64" "$OUT/indexes" "$OUT/config"
    (cd "$REPO/executor" && CGO_ENABLED=0 go build -o "$OUT/bin/kodos" .)
    sh "$REPO/build/mkapk.sh" >/dev/null   # -> vm/repo/x86_64 (apk + index)
    cp "$REPO"/vm/repo/x86_64/kodos-*.apk "$REPO/vm/repo/x86_64/APKINDEX.tar.gz" \
        "$OUT/repo/x86_64/"
    for r in main community; do             # lockfile.lua index snapshots
        [ -s "$CACHE/$r-324.apkindex" ] || {
            mkdir -p "$CACHE"
            curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz" \
                | tar -xzO APKINDEX > "$CACHE/$r-324.apkindex"
        }
        cp "$CACHE/$r-324.apkindex" "$OUT/indexes/"
    done
    cp "$REPO"/build/usb/config/*.lua "$OUT/config/"
    cp "$REPO"/build/usb/run-install.sh "$REPO"/build/usb/README.md "$OUT/"
    echo "payload staged in $OUT:"
    find "$OUT" -mindepth 1 -maxdepth 2 | sort
}

stage_payload
if [ -n "${STAGE_ONLY:-}" ]; then
    echo "STAGE_ONLY: no device touched"
    exit 0
fi

DEV=${1:-}
[ -b "$DEV" ] || { echo "usage: $0 /dev/sdX  (the WHOLE USB stick; it is erased)"; exit 2; }
if lsblk -n -o MOUNTPOINT "$DEV" 2>/dev/null | grep -q '[^[:space:]]'; then
    echo "ERROR: $DEV (or a partition of it) is mounted. Not the stick you mean?"
    exit 1
fi
command -v sgdisk >/dev/null || { echo "ERROR: sgdisk (gdisk pkg) needed on the host"; exit 1; }
command -v mkfs.ext4 >/dev/null || { echo "ERROR: mkfs.ext4 (e2fsprogs) needed on the host"; exit 1; }

echo "ALL DATA ON $DEV WILL BE DESTROYED (kodos install USB, Alpine $REL)."
printf 'type the device name to confirm: '
read -r CONFIRM
[ "$CONFIRM" = "$DEV" ] || { echo "not confirmed; aborted"; exit 1; }

[ -s "$ISO" ] || { mkdir -p "$CACHE"; curl -f -o "$ISO" "$ISO_URL"; }
echo "dd $ISO -> $DEV"
dd if="$ISO" of="$DEV" bs=4M status=progress oflag=sync
sync

# The ISO is isohybrid: its backup GPT sits at the old image end. Relocate it
# to the stick's end (sgdisk -e), then carve the payload partition.
sgdisk -e "$DEV" >/dev/null
sgdisk -n 0:0:+1G -t 0:0700 -c 0:KODOS "$DEV" >/dev/null
sgdisk -G "$DEV" >/dev/null 2>&1 || true    # re-randomize GUIDs off the ISO's
udevadm settle 2>/dev/null || sleep 2
NEW=$(lsblk -nro NAME "$DEV" | grep -v "^${DEV}$" | tail -1)
[ -n "$NEW" ] || { echo "ERROR: no partition appeared on $DEV"; exit 1; }
mkfs.ext4 -q -L KODOS "$NEW"

MNT=/mnt/kodos-usb-payload
mkdir -p "$MNT"
mount "$NEW" "$MNT"
cp -a "$OUT/." "$MNT/"
sync
umount "$MNT"

echo
echo "USB ready. Full walkthrough: /usb/README.md (also on the stick)."
echo "Boot the target from it (BIOS or UEFI, Secure Boot OFF), on the live system:"
echo "  udhcpc -i <if>            # or setup-alpine, network is required
  mount -L KODOS /usb
  vi /usb/config/machine.lua     # host_name + the /dev/... disk line (lsblk)
  sh /usb/run-install.sh --dry-run
  sh /usb/run-install.sh"
