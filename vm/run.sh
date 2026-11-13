#!/bin/sh
# run.sh — qcow2 end-to-end test: install the layered eszkoz config into a
# VM disk with the executor, then boot it.
#   vm/run.sh          install + boot (Ctrl-a x quits qemu)
#   vm/run.sh install  just install   vm/run.sh boot   just boot
#   vm/run.sh fresh    delete the disk first
# Everything is idempotent: re-running install re-partitions nothing
# (partition table/fs checks) and re-creates generation N+... as designed.
set -eu
VMDIR=$(cd "$(dirname "$0")" && pwd)
REPO=$(dirname "$VMDIR")
D=/tmp/opencode
DISK=$VMDIR/vm-disk.qcow2
# repo-local VM root: NEVER pick a path that may live inside the host's own
# live storage (e.g. /var/tmp canonicalized into /kod/store on a kodos host)
MNT=$VMDIR/mnt
MNT_LEGACY=/var/tmp/kodos-vm-root
NBD=/dev/nbd0

FRESH=0
if [ "${1:-}" = fresh ]; then FRESH=1; fi
PHASE=${1:-all}; [ "$PHASE" = fresh ] && PHASE=all

if [ "$(id -u)" -ne 0 ]; then exec sudo env APK_BIN="${APK_BIN:-}" "$0" "$@"; fi
if [ "$FRESH" = 1 ]; then rm -f "$DISK"; fi

cleanup() {
    # Only touch OURS on an unknown host: an nbd mount is ours if its target
    # is under the VM root (match also realpath — hosts vary, e.g. /var/tmp
    # may be a symlink; never assume the host's layout). Disconnects need a
    # qemu-nbd cmdline proof of our disk.
    Mreal=$(readlink -f "$MNT" 2>/dev/null || echo "$MNT")
    Lreal=$(readlink -f "$MNT_LEGACY" 2>/dev/null || echo "$MNT_LEGACY")
    awk -v m="$MNT" -v r="$Mreal" -v l="$MNT_LEGACY" -v lr="$Lreal" \
        '$1 ~ /^\/dev\/nbd/ && (index($2, m) == 1 || index($2, r) == 1 ||
            index($2, l) == 1 || index($2, lr) == 1) {print $2}' \
        /proc/mounts | while read -r mm; do
        # demote first: never propagate an unmount outward to the host
        mount --make-rprivate "$mm" >/dev/null 2>&1 || true
        umount -R "$mm" >/dev/null 2>&1 || umount -l -R "$mm" >/dev/null 2>&1 || true
    done
    proven=""
    for i in 0 1 2 3 4 5 6 7; do
        [ -e "/sys/block/nbd$i/pid" ] || continue
        p=$(cat "/sys/block/nbd$i/pid" 2>/dev/null) || continue
        grep -qa "vm-disk" "/proc/$p/cmdline" 2>/dev/null || continue
        proven="$proven $i"
    done
    if [ -n "$proven" ]; then
        alt=$(echo $proven | tr ' ' '|')
        awk -v re="^/dev/nbd(${alt})p?[0-9]*$" '$1 ~ re {print $1}' /proc/swaps |
            while read -r s; do swapoff "$s" >/dev/null 2>&1 || true; done
        for i in $proven; do
            for t in 1 2 3 4 5; do
                qemu-nbd -d "/dev/nbd$i" >/dev/null 2>&1 && break
                sleep 1
            done
        done
    fi
    # remove OUR (now-empty) legacy mountpoint from wherever it pointed
    rmdir "$MNT_LEGACY" 2>/dev/null || true
}

attach_nbd() {
    # The host may run anything: NEVER disconnect an nbd we cannot prove is
    # ours, and do not assume nbd0 is free — a wedged dead connection (only
    # reboot clears it) just burns one index; use the next free device.
    modprobe nbd max_part=8 || { echo "ERROR: modprobe nbd failed"; exit 1; }
    # a host that preloaded nbd without max_part gets no nbdXpY partition
    # nodes at all: try to take over the module, else fail loudly.
    if [ "$(cat /sys/module/nbd/parameters/max_part 2>/dev/null || echo 0)" -lt 8 ]; then
        modprobe -r nbd >/dev/null 2>&1 || true
        modprobe nbd max_part=8 2>/dev/null || true
        mp=$(cat /sys/module/nbd/parameters/max_part 2>/dev/null || echo 0)
        [ "$mp" -ge 8 ] || { echo "ERROR: nbd loaded with max_part=$mp (someone else's module state): partition nodes will not exist"; exit 1; }
    fi
    [ -e /dev/nbd0 ] || sleep 1   # udev may be slow to create the nodes
    for i in 0 1 2 3 4 5 6 7; do
        d=/dev/nbd$i
        [ -e "$d" ] || continue
        # in use (live or wedged): leave it alone entirely
        [ -e "/sys/block/nbd$i/pid" ] && continue
        qemu-nbd -c "$d" "$DISK" >/dev/null 2>&1 || continue
        ok=0
        for t in 1 2 3; do
            if dd if="$d" of=/dev/null bs=512 count=34 skip=1 status=none 2>/dev/null; then ok=1; break; fi
            sleep 1
        done
        if [ "$ok" = 1 ]; then NBD=$d; return 0; fi
        qemu-nbd -d "$d" >/dev/null 2>&1 || true
    done
    echo "ERROR: no free+readable /dev/nbd0-7 (foreign or wedged ones are left alone)."
    echo "Ours die with the script; if a wedge burned every index, reboot clears them."
    exit 1
}

do_install() {
    # recover cleanly from a previous crashed run (stale mounts/nbd)
    cleanup
    [ -f "$DISK" ] || qemu-img create -f qcow2 "$DISK" 200G
    attach_nbd

    # test-only secrets (vm is a throwaway box; never reuse these patterns)
    if [ ! -f "$VMDIR/secrets.json" ]; then
        rh=$(openssl passwd -6 'kodos-root-test')
        uh=$(openssl passwd -6 'kodos-user-test')
        printf '{"wifi.home.psk": "vm-test-not-a-real-psk", "users.root.hashed_password": "%s", "users.abuss.hashed_password": "%s"}\n' "$rh" "$uh" > "$VMDIR/secrets.json"
        echo "wrote vm/secrets.json (test passwords: kodos-root-test / kodos-user-test)"
    fi

    cd "$REPO/example/eszkoz"
    lua generate.lua system.lua ../../vm/vm-layer.lua layers/machine.lua \
        layers/user.lua > "$VMDIR/actions.json"
    # actions carry the config's nbd0 device names; swap in the index we
    # actually attached (foreign/wedged nbd0 may have forced us higher)
    if [ "${NBD#/dev/nbd}" != 0 ]; then
        sed -i "s|nbd0|nbd${NBD#/dev/nbd}|g" "$VMDIR/actions.json"
    fi
    # /tmp/opencode snapshots can vanish (tmp cleanup): refetch like drift.sh
    for r in main community; do
        [ -s "$D/$r-324.apkindex" ] || {
            echo "refetching $r index snapshot"
            mkdir -p "$D"
            curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz" \
                | tar -xzO APKINDEX > "$D/$r-324.apkindex"
            # we run under sudo: leave snapshots writable by the real user
            [ -n "${SUDO_USER:-}" ] && chown "$SUDO_USER" "$D" "$D/$r-324.apkindex"
        }
    done
    lua lockfile.lua 0 "$D/main-324.apkindex" "$D/community-324.apkindex" -- \
        system.lua ../../vm/vm-layer.lua layers/machine.lua layers/user.lua \
        > "$VMDIR/generation-0.json"

    mkdir -p "$MNT"
    # static apk-tools: repo-local copy (vm/apk-tools-static) or APK_BIN;
    # never rely on a host-path guess for an unknown distro
    APK=${APK_BIN:-$VMDIR/apk-tools-static}
    [ -x "$APK" ] || { echo "ERROR: no static apk-tools at $APK (APK_BIN=/path vm/run.sh)"; exit 1; }
    "$REPO/executor/kodos-exec" -actions "$VMDIR/actions.json" \
        -metadata "$VMDIR/generation-0.json" -root "$MNT" \
        -secrets "$VMDIR/secrets.json" \
        -apk "$APK"
    sync
    cleanup
}

do_boot() {
    [ -f "$DISK" ] || { echo "no $DISK: run 'vm/run.sh install' first"; exit 1; }
    # Firmware handling is distro- AND qemu-version-sensitive: recent qemu
    # refuses -bios with CODE-only OVMF images (needs pflash unit=0/1), and
    # some distros only ship split CODE/VARS, others combined OVMF.fd.
    # Treat the host as unknown: probe the known layouts.
    QARGS=""
    for pair in \
        "/usr/share/edk2-ovmf/x64/OVMF_CODE.4m.fd /usr/share/edk2-ovmf/x64/OVMF_VARS.4m.fd" \
        "/usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd" \
        "/usr/share/edk2/ovmf/OVMF_CODE.4m.fd /usr/share/edk2/ovmf/OVMF_VARS.4m.fd"; do
        CODE=${pair% *}; VARS=${pair#* }
        [ -r "$CODE" ] && [ -r "$VARS" ] || continue
        [ -f "$VMDIR/vm-vars.4m.fd" ] || cp "$VARS" "$VMDIR/vm-vars.4m.fd"
        QARGS="-machine q35 -drive if=pflash,format=raw,readonly=on,file=$CODE -drive if=pflash,format=raw,file=$VMDIR/vm-vars.4m.fd"
        break
    done
    if [ -z "$QARGS" ]; then
        BIOS=""
        for c in /usr/share/edk2-ovmf/x64/OVMF.4m.fd /usr/share/OVMF/OVMF.fd \
                 /usr/share/edk2/ovmf/OVMF.fd; do
            [ -r "$c" ] && { BIOS=$c; break; }
        done
        [ -n "$BIOS" ] || { echo "ERROR: no OVMF firmware found (install edk2-ovmf/ovmf)"; exit 1; }
        QARGS="-bios $BIOS"
    fi
    echo "booting (quit: Ctrl-a x; login: abuss / kodos-user-test, then 'su -')"
    exec qemu-system-x86_64 -m 4096 -smp 2 \
        -drive file="$DISK",if=virtio,format=qcow2 \
        -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 \
        -device virtio-net-pci \
        $QARGS \
        -nographic
}

case "$PHASE" in
    install) trap cleanup EXIT; do_install ;;
    debug)   trap cleanup EXIT; cleanup; attach_nbd; mkdir -p "$MNT"
             mount "${NBD}p2" "$MNT"
             G=$MNT/kod/generation
             echo "--- live etc entry types (hardlinks expected):"
             for f in group passwd; do
                 echo "$f: $(stat -Lc '%F nlink=%h ino=%i' "$MNT/etc/$f" 2>&1) store_ino=$(stat -Lc %i "$G/0/etc_store/$f" 2>&1)"
             done
             echo "--- shell below; exit to detach"
             exec su -s /bin/sh "${SUDO_USER:-root}" ;;
    boot)    do_boot ;;
    all)     trap cleanup EXIT; do_install; do_boot ;;
    *) echo "usage: vm/run.sh [install|boot|fresh]"; exit 2 ;;
esac
