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

do_install() {
    # recover cleanly from a previous crashed run (stale mounts/nbd)
    cleanup
    modprobe nbd max_part=8
    [ -f "$DISK" ] || qemu-img create -f qcow2 "$DISK" 200G
    if [ ! -s /sys/block/nbd0/pid ] 2>/dev/null; then qemu-nbd -c "$NBD" "$DISK"; fi

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
    lua lockfile.lua 0 "$D/main-324.apkindex" "$D/community-324.apkindex" -- \
        system.lua ../../vm/vm-layer.lua layers/machine.lua layers/user.lua \
        > "$VMDIR/generation-0.json"

    mkdir -p "$MNT"
    "$REPO/executor/kodos-exec" -actions "$VMDIR/actions.json" \
        -metadata "$VMDIR/generation-0.json" -root "$MNT" \
        -secrets "$VMDIR/secrets.json"
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
    boot)    do_boot ;;
    all)     trap cleanup EXIT; do_install; do_boot ;;
    *) echo "usage: vm/run.sh [install|boot|fresh]"; exit 2 ;;
esac
