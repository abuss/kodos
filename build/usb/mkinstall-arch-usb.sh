#!/bin/sh
# build/usb/mkinstall-arch-usb.sh — stage (and later burn) Arch install media.
#
#   STAGE_ONLY=1 sh build/usb/mkinstall-arch-usb.sh   # stage payload only
#   sh build/usb/mkinstall-arch-usb.sh /dev/sdX       # whole stick, ERASED
#
# The booted live host is still the Alpine 3.24 standard ISO; the payload
# grafted at /kodos carries the static kodos binary, Arch sync-DB desc
# snapshots, and a standalone Arch machine config. Host pacman is the DB
# trust oracle; it never runs on the live host or install path.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
REL=${ALPINE_REL:-3.24.4}
CACHE=$REPO/build/usb/iso
OUT=${OUT:-$REPO/build/usb/arch-stage}
ARCH_MIRROR=${ARCH_MIRROR:-https://mirror.rackspace.com/archlinux}
PACMAN_BIN=${PACMAN_BIN:-pacman}
# Optional pre-staged desc snapshots: lets a non-root host stage media without
# sudo. Real builds leave this unset so pacman remains the DB trust oracle.
ARCH_DESC_DIR=${ARCH_DESC_DIR:-}
DBROOT=/tmp/opencode/arch-media
DBPATH=$DBROOT/db
PACMAN_CACHE=$DBROOT/cache

# ponytail: build-time snapshot only; rebuild media or restage desc files on a
# pacman host when the rolling mirror moves. No runtime oracle on Alpine live.
fetch_arch_dbs() {
    mkdir -p "$OUT/indexes/arch"
    if [ -n "$ARCH_DESC_DIR" ]; then
        for r in core extra; do
            [ -s "$ARCH_DESC_DIR/$r.desc" ] || {
                echo "ERROR: $ARCH_DESC_DIR/$r.desc missing or empty" >&2
                exit 1
            }
            cp "$ARCH_DESC_DIR/$r.desc" "$OUT/indexes/arch/$r.desc"
        done
        return
    fi
    command -v "$PACMAN_BIN" >/dev/null 2>&1 || {
        echo "ERROR: $PACMAN_BIN not found (host pacman is the sync DB trust oracle)" >&2
        exit 1
    }
    rm -rf "$DBROOT"
    mkdir -p "$DBPATH" "$PACMAN_CACHE" "$OUT/indexes/arch"
    cat > "$DBROOT/pacman.conf" <<EOF
[options]
DBPath = $DBPATH/
CacheDir = $PACMAN_CACHE/
GPGDir = /etc/pacman.d/gnupg/
LogFile = $DBROOT/pacman.log
SigLevel = Required DatabaseOptional
LocalFileSigLevel = Optional
[core]
Server = $ARCH_MIRROR/core/os/x86_64
[extra]
Server = $ARCH_MIRROR/extra/os/x86_64
EOF
    "$PACMAN_BIN" -Sy --config "$DBROOT/pacman.conf" >/dev/null
    for r in core extra; do
        dbf=$DBPATH/sync/$r.db
        [ -e "$dbf" ] || { echo "ERROR: pacman did not produce $r sync DB" >&2; exit 1; }
        tar -xzOf "$dbf" --wildcards '*/desc' > "$OUT/indexes/arch/$r.desc" 2>/dev/null || true
        [ -s "$OUT/indexes/arch/$r.desc" ] || {
            echo "ERROR: $r desc snapshot empty or missing from $dbf" >&2
            exit 1
        }
    done
}

# These apks equip the ALPINE LIVE HOST (parted/lsblk/mkfs.ext4 for the
# executor's storage ops). They are never installed on the Arch target.
stage_live_apks() {
    mkdir -p "$OUT/repo/x86_64"
    for pk in parted util-linux e2fsprogs; do
        pv=$(awk -v p="$pk" '$0=="P:"p{f=1} f&&/^V:/{print substr($0,3); exit}' \
            "$CACHE/main-324.apkindex")
        [ -n "$pv" ] || { echo "ERROR: $pk missing from Alpine index snapshot" >&2; exit 1; }
        [ -s "$CACHE/$pk-$pv.apk" ] || {
            mkdir -p "$CACHE"
            curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/$pk-$pv.apk" \
                -o "$CACHE/$pk-$pv.apk"
        }
        cp "$CACHE/$pk-$pv.apk" "$OUT/repo/x86_64/"
    done
    "$REPO/vm/apk-tools-static" index --allow-untrusted --no-warnings \
        -o "$OUT/repo/x86_64/APKINDEX.tar.gz" "$OUT"/repo/x86_64/*.apk
}

stage_payload() {
    rm -rf "$OUT"
    mkdir -p "$OUT/bin" "$OUT/indexes/arch" "$OUT/config"
    (cd "$REPO/executor" && CGO_ENABLED=0 go build -o "$OUT/bin/kodos" .)
    cp "$REPO"/build/usb/arch-config/*.lua "$OUT/config/"
    cp "$REPO"/build/usb/arch-run-install.sh "$REPO"/build/usb/README-arch.md "$OUT/"
    fetch_arch_dbs
    stage_live_apks
    echo "Arch payload staged in $OUT:"
    find "$OUT" -mindepth 1 -maxdepth 2 | sort
}

stage_payload
[ -z "${STAGE_ONLY:-}" ] || exit 0
