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
MNT=/var/tmp/kodos-vm-root
NBD=/dev/nbd0

FRESH=0
if [ "${1:-}" = fresh ]; then FRESH=1; fi
PHASE=${1:-all}; [ "$PHASE" = fresh ] && PHASE=all

if [ "$(id -u)" -ne 0 ]; then exec sudo "$0" "$@"; fi
if [ "$FRESH" = 1 ]; then rm -f "$DISK"; fi

cleanup() {
    # device-keyed, NOT path-keyed: this host is itself a kodos machine, so
    # /var/tmp canonicalizes through /kod/store/... and the mount table's
    # path differs from the one we passed to mount. Unmount by device.
    awk '$1 ~ "^/dev/nbd" {print $2}' /proc/mounts | while read -r m; do
        # demote first: never propagate an unmount outward to the host
        mount --make-rprivate "$m" >/dev/null 2>&1 || true
        umount -R "$m" >/dev/null 2>&1 || umount -l -R "$m" >/dev/null 2>&1 || true
    done
    awk '$1 ~ "^/dev/nbd" {print $1}' /proc/swaps | while read -r s; do
        swapoff "$s" >/dev/null 2>&1 || true
    done
    if [ -e /sys/block/nbd0/pid ] && [ -s /sys/block/nbd0/pid ]; then
        for i in 1 2 3 4 5; do
            qemu-nbd -d "$NBD" >/dev/null 2>&1 && break
            sleep 1
        done
    fi
}

attach_nbd() {
    modprobe nbd max_part=8 || { echo "ERROR: modprobe nbd failed"; exit 1; }
    # Always start from a clean slate. Skipping the attach because "nbd0 is
    # already connected" reuses whatever state it has — including a dead
    # server whose stale pid file still says "connected" (EIO forever).
    qemu-nbd -d "$NBD" >/dev/null 2>&1 || true
    i=0
    while [ -e /sys/block/nbd0/pid ] && [ $i -lt 5 ]; do sleep 1; i=$((i+1)); done
    if ! qemu-nbd -c "$NBD" "$DISK"; then
        echo "ERROR: cannot attach $NBD: wedged dead connection held by the kernel."
        echo "Recovery: sudo pkill -f qemu-nbd; sudo modprobe -r nbd && sudo modprobe nbd max_part=8"
        echo "If 'Module nbd is in use': the wedge survives until reboot — reboot."
        exit 1
    fi
    # readable probe: an nbd whose server died serves pure EIO; the fresh
    # attach above normally cures it, retries cover slow settling.
    ok=0
    for i in 1 2 3; do
        if dd if="$NBD" of=/dev/null bs=512 count=34 skip=1 status=none 2>/dev/null; then ok=1; break; fi
        sleep 1
    done
    if [ "$ok" != 1 ]; then
        echo "ERROR: $NBD attached but not readable (dead kernel state)."
        echo "Recovery: sudo pkill -f qemu-nbd; sudo modprobe -r nbd && sudo modprobe nbd max_part=8"
        echo "If 'Module nbd is in use': the wedge survives until reboot — reboot."
        exit 1
    fi
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
    # /tmp/opencode snapshots can vanish (tmp cleanup): refetch like drift.sh
    for r in main community; do
        [ -s "$D/$r-324.apkindex" ] || {
            echo "refetching $r index snapshot"
            mkdir -p "$D"
            curl -sf "https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz" \
                | tar -xzO APKINDEX > "$D/$r-324.apkindex"
        }
    done
    lua lockfile.lua 0 "$D/main-324.apkindex" "$D/community-324.apkindex" -- \
        system.lua ../../vm/vm-layer.lua layers/machine.lua layers/user.lua \
        > "$VMDIR/generation-0.json"

    mkdir -p "$MNT"
    "$REPO/executor/kodos-exec" -actions "$VMDIR/actions.json" \
        -metadata "$VMDIR/generation-0.json" -root "$MNT" \
        -secrets "$VMDIR/secrets.json" \
        -apk /home/abuss/Work/devel/kodos-musl/experiments/apk-usage/apk
    sync
    cleanup
}

do_boot() {
    [ -f "$DISK" ] || { echo "no $DISK: run 'vm/run.sh install' first"; exit 1; }
    echo "booting (quit: Ctrl-a x; login: abuss / kodos-user-test, then 'su -')"
    exec qemu-system-x86_64 -m 4096 -smp 2 \
        -drive file="$DISK",if=virtio,format=qcow2 \
        -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 \
        -device virtio-net-pci \
        -bios /usr/share/edk2-ovmf/x64/OVMF_CODE.4m.fd \
        -nographic
}

case "$PHASE" in
    install) trap cleanup EXIT; do_install ;;
    debug)   trap cleanup EXIT; cleanup; attach_nbd; mkdir -p "$MNT"
             mount /dev/nbd0p2 "$MNT"
             G=$MNT/kod/generation
             echo "--- live etc entry types (hardlinks expected):"
             for f in group passwd; do
                 echo "$f: $(stat -Lc '%F nlink=%h ino=%i' "$MNT/etc/$f" 2>&1) store_ino=$(stat -Lc %i "$G/0/etc_store/$f" 2>&1)"
             done
             echo "--- shell below; exit to detach"
             exec sudo -u "${SUDO_USER:-abuss}" -s ;;
    boot)    do_boot ;;
    all)     trap cleanup EXIT; do_install; do_boot ;;
    *) echo "usage: vm/run.sh [install|boot|fresh]"; exit 2 ;;
esac
