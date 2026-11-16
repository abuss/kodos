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
# The stick = the kodos INSTALL ISO (mkinstall-iso.sh: Alpine standard ISO
# re-mastered with xorriso, payload grafted read-only at /kodos) dd'd onto the
# whole device: isohybrid, boots BIOS+UEFI. Boot the stick, bring up
# Ethernet, then find the payload the live auto-mounted:
#   D=$(find /media /run/media -maxdepth 4 -name run-install.sh | head -1)
#   cp -a "${D%/*}" /root/kodos && sh /root/kodos/run-install.sh
#
# The earlier design (Alpine ISO dd + carved ext4 KODOS partition) died on
# its FIRST physical round (2026-11-16), three independent ways: sgdisk
# refuses the ISO's hybrid-DOS table ("Invalid partition data!" — parted
# works); the payload partition is then unmountable on the live (the
# automounter holds the WHOLE disk as ISO9660 at /media/vda and busybox
# mount/losetup exclusive-opens on its partitions fail EBUSY); and it was
# never needed: the installer edits/works from its copy under /root/kodos
# anyway. Burn the ISO; the payload travels inside it.
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
    # only the apk THIS commit built (vm/repo accumulates old versions)
    KPKG="kodos-${PKGVER:-0.1.0}-r${PKGREL_OVERRIDE:-$(git -C "$REPO" rev-list --count HEAD)}.apk"
    [ -f "$REPO/vm/repo/x86_64/$KPKG" ] || { echo "ERROR: mkapk did not produce $KPKG"; exit 1; }
    cp "$REPO/vm/repo/x86_64/$KPKG" "$OUT/repo/x86_64/"
    # parted + util-linux for the LIVE installer host (the standard ISO does
    # not ship them, the executor's storage ops need them): pinned to the
    # snapshot index versions, offline from the payload repo
    for pk in parted util-linux; do
        PV=$(awk -v p="$pk" '$0=="P:"p{f=1} f&&/^V:/{print substr($0,3); exit}' "$CACHE/main-324.apkindex")
        [ -s "$CACHE/$pk-$PV.apk" ] || {
            mkdir -p "$CACHE"
            curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/$pk-$PV.apk" \
                -o "$CACHE/$pk-$PV.apk"
        }
        cp "$CACHE/$pk-$PV.apk" "$OUT/repo/x86_64/"
    done
    # payload-local index over exactly what we ship (kodos + parted; no vm fixture)
    "$REPO/vm/apk-tools-static" index --allow-untrusted --no-warnings \
        -o "$OUT/repo/x86_64/APKINDEX.tar.gz" "$OUT"/repo/x86_64/*.apk
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

echo "ALL DATA ON $DEV WILL BE DESTROYED (kodos install USB, Alpine $REL)."
printf 'type the device name to confirm: '
read -r CONFIRM
[ "$CONFIRM" = "$DEV" ] || { echo "not confirmed; aborted"; exit 1; }

KISO=$CACHE/kodos-install-$REL.iso
sh "$(dirname "$0")/mkinstall-iso.sh"
[ -s "$KISO" ] || { echo "ERROR: mkinstall-iso.sh produced no $KISO"; exit 1; }
echo "dd $KISO -> $DEV"
dd if="$KISO" of="$DEV" bs=4M status=progress oflag=sync
sync

echo
echo "USB ready. Full walkthrough: /kodos/README.md in the ISO (also on the stick)."
echo "Boot the target from it (BIOS or UEFI, Secure Boot OFF), on the live system:"
echo "  ifconfig eth0 up && udhcpc -i <if>   # network is required"
echo "  D=\$(find /media /run/media -maxdepth 4 -name run-install.sh | head -1)"
echo "  cp -a \"\${D%/*}\" /root/kodos"
echo "  vi /root/kodos/config/machine.lua    # the /dev/... disk line (lsblk)"
echo "  sh /root/kodos/run-install.sh --dry-run"
echo "  sh /root/kodos/run-install.sh"
