#!/bin/sh
# build/usb/mkinstall-iso.sh — build a single-file kodos install ISO.
#
#   sh build/usb/mkinstall-iso.sh [out.iso]
#     -> build/usb/iso/kodos-install-<rel>.iso
#
# The Alpine standard ISO re-mastered with xorriso, payload grafted at /kodos
# (identical payload to mkinstall-usb.sh, staged by reusing its STAGE_ONLY
# mode), and the ORIGINAL boot info re-created: isohybrid MBR/GPT, isolinux
# BIOS entry, efi.img EFI entry. One file you can mount in any VM, burn on
# any medium, or dd to a stick.
#
# Difference vs mkinstall-usb.sh: the payload there is a writable ext4
# partition on the stick (edit configs in place); here /kodos is READ-ONLY
# (ISO 9660), so the config-editing flow copies the payload off the media
# first — see the ISO section of README.md.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
REL=${ALPINE_REL:-3.24.4}
CACHE=$REPO/build/usb/iso
BASE=$CACHE/alpine-standard-$REL-x86_64.iso
URL=https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/x86_64/alpine-standard-$REL-x86_64.iso
STAGE=$REPO/build/usb/stage
OUT=${1:-$CACHE/kodos-install-$REL.iso}

command -v xorriso >/dev/null || { echo "ERROR: xorriso needed on the host"; exit 1; }
[ -s "$BASE" ] || { mkdir -p "$CACHE"; curl -f -o "$BASE" "$URL"; }

# payload: the same staging the USB script does (also caches ISO + indexes)
OUT=$STAGE STAGE_ONLY=1 sh "$REPO/build/usb/mkinstall-usb.sh" >/dev/null
echo "payload staged ($(du -sh "$STAGE" | cut -f1))"

# boot info of the base ISO, verbatim (Alpine 3.24: isolinux BIOS + EFI
# entry + isohybrid MBR copied from the ISO itself, cyl-align 64/32)
TREE=$(mktemp -d /tmp/kodos-iso-tree.XXXXXX)
trap 'rm -rf "$TREE"' EXIT
xorriso -osirrox on -indev "$BASE" -extract / "$TREE" >/dev/null
chmod -R u+rwX "$TREE"        # extracted ISO perms are read-only
cp -a "$STAGE/." "$TREE/kodos/"

xorriso -as mkisofs \
    -V "alpine-std $REL x86_64" \
    -isohybrid-mbr --interval:local_fs:0s-15s:zero_mbrpt,zero_gpt:"$BASE" \
    -partition_cyl_align on -partition_offset 0 -partition_hd_cyl 64 \
    -partition_sec_hd 32 --mbr-force-bootable -iso_mbr_part_type 0x00 \
    -c /boot/syslinux/boot.cat \
    -b /boot/syslinux/isolinux.bin -no-emul-boot -boot-load-size 4 -boot-info-table \
    -eltorito-alt-boot -e /boot/grub/efi.img -no-emul-boot -isohybrid-gpt-basdat \
    -R -J -iso-level 3 \
    -o "$OUT" "$TREE" >/dev/null

echo "ISO written: $OUT ($(du -h "$OUT" | cut -f1))"
xorriso -indev "$OUT" 2>/dev/null | grep -iE "Isofile|Total" | head -3 || true
xorriso -indev "$OUT" -find /kodos -maxdepth 1 2>/dev/null | head -8 || true
