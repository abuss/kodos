#!/bin/sh
# build/usb/arch-run-install.sh — Arch target bootstrap, run ON the booted
# Alpine live stick. The live host is Alpine 3.24; the TARGET is Arch, installed
# through the Arch backend. Find the read-only payload and copy it first:
#   D=$(find /media /run/media -maxdepth 4 -name arch-run-install.sh | head -1)
#   cp -a "${D%/*}" /root/kodos
#   sh /root/kodos/arch-run-install.sh --dry-run
#
# Flow: generate.lua -> lockfile.lua (Arch desc snapshots) -> kodos exec.
# No pacman and no APK repo on the live/install path: pacman was only the
# build-time DB trust oracle for the desc files in indexes/arch/.
set -eu

PAYLOAD=$(cd "$(dirname "$0")" && pwd)
KODOS=$PAYLOAD/bin/kodos
CFG=$PAYLOAD/config/arch-machine.lua
WORK=/tmp/kodos-arch-install
TARGET_ROOT=${TARGET_ROOT:-/mnt/kodos-target}

DRY="" YES="" DEV=""
while [ $# -gt 0 ]; do
    case $1 in
        --dry-run) DRY=1 ;;
        --yes) YES=1 ;;
        --device) shift; DEV=${1:-} ;;
        *) echo "usage: $0 [--dry-run] [--yes] [--device /dev/sdX]"; exit 2 ;;
    esac
    shift
done

[ -x "$KODOS" ] || { echo "ERROR: no $KODOS (payload copied wrong?)"; exit 1; }
[ -f "$CFG" ] || { echo "ERROR: no $CFG"; exit 1; }
command -v blkid >/dev/null || { echo "ERROR: blkid missing (util-linux); boot the ALPINE standard stick, not some other stick"; exit 1; }

# These apks equip the ALPINE LIVE HOST only (the executor's storage ops shell
# out to parted/mkfs.ext4/lsblk). They are never installed on the Arch target.
MISSING=""
command -v parted    >/dev/null || MISSING="$MISSING parted"
command -v mkfs.ext4 >/dev/null || MISSING="$MISSING e2fsprogs"
command -v lsblk     >/dev/null || MISSING="$MISSING util-linux"
if [ -n "$MISSING" ] && [ -z "$DRY" ]; then
    echo "installing from payload repo + mirror:$MISSING"
    apk add --allow-untrusted \
        --repository "$PAYLOAD/repo" \
        --repository https://dl-cdn.alpinelinux.org/alpine/v3.24/main \
        --repository https://dl-cdn.alpinelinux.org/alpine/v3.24/community \
        $MISSING || { echo "ERROR: apk add$MISSING failed"; exit 1; }
elif [ -n "$MISSING" ]; then
    echo "WARN: dry-run would install live tools:$MISSING" >&2
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
    echo "the gpt+esp/root layout, and ALL data on other partitions of a"
    echo "foreign table is left alone but the flow may still format named"
    echo "partitions. Think twice."
    printf 'type yes to continue: '
    read -r ANS
    [ "$ANS" = yes ] || { echo "aborted"; exit 1; }
fi

# ---- the sequence ----
rm -rf "$WORK"; mkdir -p "$WORK"
# dry-run never touches the real disk: plan against a scratch dir instead
if [ -n "$DRY" ]; then TARGET_ROOT=$WORK/target; fi
mkdir -p "$TARGET_ROOT"

"$KODOS" lua generate.lua "$CFG" > "$WORK/actions.json"
# --device differing from the config: rewrite the stream (partition/format/
# mount ops must act on DEV, not the placeholder in the config).
if [ "$DEV" != "$CFGDEV" ]; then
    sed "s|$CFGDEV|$DEV|g" "$WORK/actions.json" > "$WORK/actions.json.tmp"
    mv "$WORK/actions.json.tmp" "$WORK/actions.json"
fi

# Arch closure lock: the desc snapshots are the only index source. No KODOS_APK,
# no index refresh, no local repo — those are Alpine-only concerns.
"$KODOS" lua lockfile.lua 0 "$PAYLOAD/indexes/arch/core.desc" \
    "$PAYLOAD/indexes/arch/extra.desc" -- "$CFG" > "$WORK/generation-0.json"

# ---- secrets: shipped payload file, else prompt for root + install user ----
SECRETS=$PAYLOAD/config/secrets.json
if [ ! -f "$SECRETS" ] && [ -z "$DRY" ]; then
    command -v openssl >/dev/null || {
        echo "ERROR: no $SECRETS and no openssl to hash passwords."; exit 1; }
    printf 'root password (empty = account locked, no password login): '
    stty -echo 2>/dev/null || true
    IFS= read -r P1 || true
    stty echo 2>/dev/null || true
    printf '\nabuss password (empty = no password): '
    stty -echo 2>/dev/null || true
    IFS= read -r P2 || true
    stty echo 2>/dev/null || true
    printf '\n'
    H1=""; H2=""
    [ -n "$P1" ] && H1=$(openssl passwd -6 "$P1")
    [ -n "$P2" ] && H2=$(openssl passwd -6 "$P2")
    printf '{\n  "users.root.hashed_password": "%s",\n  "users.abuss.hashed_password": "%s"\n}\n' \
        "$H1" "$H2" > "$WORK/secrets.json"
    chmod 600 "$WORK/secrets.json"
    SECRETS=$WORK/secrets.json
fi

# ---- exec ----
set -- -actions "$WORK/actions.json" -metadata "$WORK/generation-0.json" \
    -root "$TARGET_ROOT"
if [ -n "$SECRETS" ] && [ -f "$SECRETS" ]; then
    set -- "$@" -secrets "$SECRETS"
fi
if [ -n "$DRY" ]; then set -- "$@" -dry-run; fi
"$KODOS" exec "$@"

if [ -n "$DRY" ]; then
    echo "dry-run done: nothing was written to $DEV"
    exit 0
fi

# static binary only — no local Arch repo, no kodos.apk, no self-update
mkdir -p "$TARGET_ROOT/usr/bin" "$TARGET_ROOT/etc/kodos"
install -m 0755 "$PAYLOAD/bin/kodos" "$TARGET_ROOT/usr/bin/kodos"
if [ -n "$SECRETS" ] && [ -f "$SECRETS" ]; then
    install -m 0600 "$SECRETS" "$TARGET_ROOT/etc/kodos/secrets.json"
fi

sync
umount -R "$TARGET_ROOT" 2>/dev/null || true
echo
echo "installed. reboot, boot the kodos generation from grub, log in, and check:"
echo "    kodos status; ls -l /usr/bin/kodos /etc/kodos/secrets.json"
echo "day-2 in-place apply is not wired for Arch; rebuild media for config changes"
