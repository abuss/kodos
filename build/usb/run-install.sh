#!/bin/sh
# build/usb/run-install.sh — bare-metal bootstrap, run ON the booted install
# USB (Alpine standard live: root login, Ethernet up via `udhcpc` or
# setup-alpine). Payload layout on the KODOS ext4 partition (make it with
# build/usb/mkinstall-usb.sh, mount it with `mount -L KODOS /usb`):
#
#   bin/kodos  repo/x86_64/  indexes/  config/{machine.lua,target-config.lua}
#   run-install.sh
#
# The flow is the VM-proven install sequence (vm/run.sh do_install) minus the
# nbd indirection: storage ops act on the real disk named in the config.
#   kodos lua generate.lua  ->  kodos lua lockfile.lua  ->  kodos exec
set -eu

PAYLOAD=$(cd "$(dirname "$0")" && pwd)
KODOS=$PAYLOAD/bin/kodos
CFG=$PAYLOAD/config/machine.lua
LOCAL_REPO=$PAYLOAD/repo/x86_64/APKINDEX.tar.gz
WORK=/tmp/kodos-install
TARGET_ROOT=${TARGET_ROOT:-/mnt/kodos-target}

DRY="" YES="" DEV=""
while [ $# -gt 0 ]; do
    case $1 in
        --dry-run) DRY=1 ;;
        --yes) YES=1 ;;
        --device) shift; DEV=${1:-} ;;
        *) echo "usage: run-install.sh [--dry-run] [--yes] [--device /dev/sdX]"; exit 2 ;;
    esac
    shift
done

[ -x "$KODOS" ] || { echo "ERROR: no $KODOS (payload copied wrong?)"; exit 1; }
[ -f "$CFG" ] || { echo "ERROR: no $CFG"; exit 1; }
[ -f "$LOCAL_REPO" ] || { echo "ERROR: no $LOCAL_REPO"; exit 1; }
command -v blkid >/dev/null || { echo "ERROR: blkid missing (util-linux); boot the ALPINE standard stick, not some other stick"; exit 1; }
# the executor's storage ops shell out to parted + mkfs.ext4; the live ISO
# ships neither. parted rides in the payload repo (offline); the rest resolve
# from the mirror (network is an install assumption anyway).
MISSING=""
command -v parted    >/dev/null || MISSING="$MISSING parted"
command -v mkfs.ext4 >/dev/null || MISSING="$MISSING e2fsprogs"
# busybox blkid/mount lack the util-linux interfaces the executor uses;
# lsblk is the sentinel (no busybox applet for it)
command -v lsblk     >/dev/null || MISSING="$MISSING util-linux"
if [ -n "$MISSING" ]; then
    echo "installing from payload repo + mirror:$MISSING"
    apk add --allow-untrusted \
        --repository "$PAYLOAD/repo" \
        --repository https://dl-cdn.alpinelinux.org/alpine/v3.24/main \
        --repository https://dl-cdn.alpinelinux.org/alpine/v3.24/community \
        $MISSING || { echo "ERROR: apk add$MISSING failed"; exit 1; }
fi

# ---- target disk: the one the config layer names (or --device) ----
CFGDEV=$(grep -o '"/dev/[a-z0-9/]*"' "$CFG" | head -1 | tr -d '"')
[ -n "$DEV" ] || DEV=$CFGDEV
[ -n "$DEV" ] || { echo "ERROR: no /dev/... device in $CFG"; exit 1; }

# refuse to install onto the stick we are running from
SRC=$(findmnt -n -o SOURCE "$PAYLOAD" 2>/dev/null || true)
if [ -n "$SRC" ]; then
    USBDISK=$(lsblk -n -o PKNAME "$SRC" 2>/dev/null | head -1)
    [ -n "$USBDISK" ] || USBDISK=$(basename "$SRC" | sed 's/p[0-9]*$//; s/[0-9]*$//')
    if [ "/dev/$USBDISK" = "$DEV" ]; then
        echo "ERROR: $DEV is the USB stick this script runs from. Pick the"
        echo "machine's own disk in $CFG (lsblk) and re-run."
        exit 1
    fi
fi

if [ -b "$DEV" ]; then
    echo "target disk: $DEV"
elif [ -n "$DRY" ]; then
    echo "WARN: $DEV is not a block device here (dry-run continues)"
else
    echo "ERROR: $DEV is not a block device on this machine."
    echo "lsblk and edit the device line in $CFG (or use --device)."
    exit 1
fi

# ---- erase confirmation ----
if [ -z "$DRY" ] && [ -z "$YES" ]; then
    echo
    echo "parted on $DEV: an existing kodos table is kept, an EMPTY disk gets"
    echo "the gpt+esp/root/home/swap layout, and ALL data on other partitions"
    echo "of a foreign table is left alone but the flow may still format named"
    echo "partitions. Think twice."
    printf 'type yes to continue: '
    read -r ANS
    [ "$ANS" = yes ] || { echo "aborted"; exit 1; }
fi

# ---- the proven sequence ----
rm -rf "$WORK"; mkdir -p "$WORK"
# dry-run never touches the real disk: plan against a scratch dir instead
[ -n "$DRY" ] && TARGET_ROOT=$WORK/target
mkdir -p "$TARGET_ROOT"

# 3.24 index snapshots: staged on the stick, else refetch (network assumption)
snapshot() { # $1 = main|community; echoes the snapshot path
    f=$PAYLOAD/indexes/$1-324.apkindex
    [ -s "$f" ] || {
        f=$WORK/$1-324.apkindex
        echo "fetching $1 index snapshot" >&2
        curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/$1/x86_64/APKINDEX.tar.gz" \
            | tar -xzO APKINDEX > "$f"
    }
    echo "$f"
}
IM=$(snapshot main)
IC=$(snapshot community)

"$KODOS" lua generate.lua system.lua "$CFG" > "$WORK/actions.json"
# --device differing from the config: rewrite the stream (the proven vm/run.sh
# nbd-index sed, generalized): partition/format/mount ops act on DEV.
if [ "$DEV" != "$CFGDEV" ]; then
    sed "s|$CFGDEV|$DEV|g" "$WORK/actions.json" > "$WORK/actions.json.tmp"
    mv "$WORK/actions.json.tmp" "$WORK/actions.json"
fi
# lockfile.lua's closure solver wants an ABSOLUTE apk-tools (same resolution
# kodos apply does in-guest): the live stick's native apk, env-overridable.
[ -n "${KODOS_APK:-}" ] || KODOS_APK=$(command -v apk)
[ -n "$KODOS_APK" ] && [ -x "$KODOS_APK" ] || {
    echo "ERROR: no apk-tools for the closure lock (KODOS_APK=/path)"; exit 1; }
export KODOS_APK
# copy_tree src must be where the KODOS partition IS mounted (config was
# written assuming /usb/repo) — same actions.json sed idea vm/run.sh uses
# for nbd indices. (busybox-safe: no GNU sed -i dependency.)
sed "s|/usb/repo|$PAYLOAD/repo|g" "$WORK/actions.json" > "$WORK/actions.json.tmp"
mv "$WORK/actions.json.tmp" "$WORK/actions.json"
"$KODOS" lua lockfile.lua 0 "$IM" "$IC" "$LOCAL_REPO" -- \
    system.lua "$CFG" > "$WORK/generation-0.json"

# ---- secrets: shipped payload file, or prompt a root password into a local one
SECRETS=$PAYLOAD/config/secrets.json
if [ ! -f "$SECRETS" ] && [ -z "$DRY" ]; then
    command -v openssl >/dev/null || {
        echo "ERROR: no $SECRETS and no openssl to hash a root password."; exit 1; }
    printf 'root password (empty = console login WITHOUT password): '
    read -r P1
    if [ -n "$P1" ]; then
        h=$(openssl passwd -6 "$P1")
        printf '{"users.root.hashed_password": "%s"}\n' "$h" > "$WORK/secrets.json"
        chmod 600 "$WORK/secrets.json"
        SECRETS=$WORK/secrets.json
    else
        SECRETS=
    fi
fi

# ---- exec ----
set -- -actions "$WORK/actions.json" -metadata "$WORK/generation-0.json" \
    -root "$TARGET_ROOT"
[ -n "$SECRETS" ] && [ -f "$SECRETS" ] && set -- "$@" -secrets "$SECRETS"
[ -n "$DRY" ] && set -- "$@" -dry-run
"$KODOS" exec "$@"

if [ -n "$DRY" ]; then
    echo "dry-run done: nothing was written to $DEV"
    exit 0
fi

# keep the secrets sidecar where the machine's own apply reads it (same dir as
# config.lua; executor wrote /etc/kodos/config.lua via the files section)
if [ -n "$SECRETS" ]; then
    mkdir -p "$TARGET_ROOT/etc/kodos"
    cp "$SECRETS" "$TARGET_ROOT/etc/kodos/secrets.json"
    chmod 600 "$TARGET_ROOT/etc/kodos/secrets.json"
fi

sync
umount -R "$TARGET_ROOT" 2>/dev/null || true
echo
echo "installed. reboot, boot the kodos generation from grub, log in, and"
echo "self-manage from here on:"
echo "    kodos status; edit /etc/kodos/config.lua; kodos apply"
