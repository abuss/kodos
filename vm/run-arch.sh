#!/bin/sh
# run-arch.sh — qcow2 end-to-end test for the ARCH backend (milestone 1):
# install vm/arch-config.lua into a VM disk with the executor, then boot it.
#   vm/run-arch.sh          install + boot (Ctrl-a x quits qemu)
#   vm/run-arch.sh install  just install   vm/run-arch.sh boot   just boot
#   vm/run-arch.sh fresh    delete the disk first
#   vm/run-arch.sh guest '<cmd>'   run <cmd> inside the BOOTED guest (ssh 2222)
#   vm/run-arch.sh guest-file F [DIR]  copy F into the guest
# Everything is idempotent like run.sh: re-running install re-partitions
# nothing and re-creates the generation as designed.
#
# Trust chain: the sync DBs are fetched AND signature-verified against the
# HOST's pacman keyring BEFORE the lockfile pins anything — pacman itself is
# never invoked by the executor; it is the trust oracle only.
set -eu
VMDIR=$(cd "$(dirname "$0")" && pwd)
REPO=$(dirname "$VMDIR")
D=/tmp/opencode
DISK=$VMDIR/arch-disk.qcow2
# repo-local VM root: NEVER inside the host's own live storage (see run.sh)
MNT=$VMDIR/mnt-arch
NBD=/dev/nbd0
ARCH_LUA=$REPO/vm/arch-config.lua

FRESH=0
if [ "${1:-}" = fresh ]; then FRESH=1; fi
PHASE=${1:-all}; [ "$PHASE" = fresh ] && PHASE=all

if [ "$(id -u)" -ne 0 ]; then
    # the in-guest phases are a plain ssh client: they need no root here
    case "$PHASE" in
        guest|guest-file) ;;
        *) exec sudo "$0" "$@" ;;
    esac
fi
if [ "$FRESH" = 1 ]; then rm -f "$DISK"; fi

cleanup() {
    # Only touch OURS on an unknown host: an nbd mount is ours if its target
    # is under the VM root (match realpath too; never assume host layout).
    # Disconnects need a qemu-nbd cmdline proof of OUR disk (arch-disk).
    Mreal=$(readlink -f "$MNT" 2>/dev/null || echo "$MNT")
    awk -v m="$MNT" -v r="$Mreal" \
        '$1 ~ /^\/dev\/nbd/ && (index($2, m) == 1 || index($2, r) == 1) {print $2}' \
        /proc/mounts | while read -r mm; do
        # demote first: never propagate an unmount outward to the host
        mount --make-rprivate "$mm" >/dev/null 2>&1 || true
        umount -R "$mm" >/dev/null 2>&1 || umount -l -R "$mm" >/dev/null 2>&1 || true
    done
    proven=""
    for i in 0 1 2 3 4 5 6 7; do
        [ -e "/sys/block/nbd$i/pid" ] || continue
        p=$(cat "/sys/block/nbd$i/pid" 2>/dev/null) || continue
        grep -qa "arch-disk" "/proc/$p/cmdline" 2>/dev/null || continue
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
}

attach_nbd() {
    # run.sh doctrine: NEVER disconnect an nbd we cannot prove is ours;
    # a wedged dead connection just burns one index, take the next free one.
    modprobe nbd max_part=8 || { echo "ERROR: modprobe nbd failed"; exit 1; }
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

# DB snapshot. TRUST REALITY (measured Nov 2026): official Arch sync DBs are
# UNSIGNED — no core.db.sig exists on any mirror; Arch's own default
# "SigLevel = Required DatabaseOptional" documents it (package sigs required,
# DB sigs optional). So the oracle is pacman itself: a real `pacman -Sy`
# against exactly these Server lines (tmp DBPath; the host's own repos are
# never touched) does the fetch + validation, and we lift the desc records
# out of pacman's own synced DB files. pacman is still never invoked on the
# install path itself.
fetch_dbs() {
    command -v pacman >/dev/null || { echo "ERROR: the arch harness needs host pacman as the sync-DB trust oracle"; exit 1; }
    mkdir -p "$D/arch/db" "$D/arch/cache"
    [ -n "${SUDO_USER:-}" ] && chown "$SUDO_USER" "$D" "$D/arch" "$D/arch/db" "$D/arch/cache" 2>/dev/null || true
    M=https://geo.mirror.pkgbuild.com
    cat > "$D/arch/pacman.conf" <<EOF
[options]
DBPath = $D/arch/db/
CacheDir = $D/arch/cache/
GPGDir = /etc/pacman.d/gnupg/
LogFile = $D/arch/pacman.log
SigLevel = Required DatabaseOptional
LocalFileSigLevel = Optional
[core]
Server = $M/core/os/x86_64
[extra]
Server = $M/extra/os/x86_64
EOF
    for r in core extra; do
        # ALWAYS resync: the mirror is rolling, and a cached snapshot drifts
        # (real incident: pcsclite 2.5.3 -> 2.5.4 between runs -> 404). The
        # spec's mitigation is refetch-immediately-before-exec; pacman -Sy is
        # seconds when nothing moved. Residual race (DB synced, tarball not
        # yet uploaded) is the known ceiling: upgrade path = TimeMachine pin.
        echo "syncing $r DB snapshot via pacman (trust oracle: TLS + SigLevel rules)"
        pacman -Sy --config "$D/arch/pacman.conf" "$r" || { echo "ERROR: pacman -Sy $r failed"; exit 1; }
        # the DB tarball's members are <pkg-ver>/desc (no <repo>.desc member)
        tar -xzOf "$D/arch/db/$r.db" --wildcards '*/desc' > "$D/arch/$r.desc"
        # we run under sudo: leave snapshots writable by the real user
        [ -n "${SUDO_USER:-}" ] && chown "$SUDO_USER" "$D/arch/$r.desc" 2>/dev/null || true
    done
}

do_install() {
    # recover cleanly from a previous crashed run (stale mounts/nbd)
    cleanup
    [ -f "$DISK" ] || qemu-img create -f qcow2 "$DISK" 200G
    attach_nbd

    # test-only secrets (same creds as the Alpine round; throwaway box)
    if [ ! -f "$VMDIR/secrets.json" ]; then
        rh=$(openssl passwd -6 'kodos-root-test')
        uh=$(openssl passwd -6 'kodos-user-test')
        printf '{"wifi.home.psk": "vm-test-not-a-real-psk", "users.root.hashed_password": "%s", "users.abuss.hashed_password": "%s"}\n' "$rh" "$uh" > "$VMDIR/secrets.json"
        chmod 600 "$VMDIR/secrets.json"
        echo "wrote vm/secrets.json (test passwords: kodos-root-test / kodos-user-test)"
    fi

    cd "$REPO/executor/lua"
    lua generate.lua "$ARCH_LUA" > "$VMDIR/arch-actions.json"
    # actions carry the config's nbd0 device names; swap in the index we
    # actually attached (foreign/wedged nbd0 may have forced us higher)
    if [ "${NBD#/dev/nbd}" != 0 ]; then
        sed -i "s|nbd0|nbd${NBD#/dev/nbd}|g" "$VMDIR/arch-actions.json"
    fi

    fetch_dbs
    # closure lock straight from the desc snapshots (no apk-tools on arch:
    # alpm.lua resolves against exactly these DBs)
    lua lockfile.lua 0 "$D/arch/core.desc" "$D/arch/extra.desc" -- \
        "$ARCH_LUA" > "$VMDIR/arch-generation-0.json"

    mkdir -p "$MNT"
    "$REPO/executor/kodos" exec -actions "$VMDIR/arch-actions.json" \
        -metadata "$VMDIR/arch-generation-0.json" -root "$MNT" \
        -secrets "$VMDIR/secrets.json"
    sync
    cleanup
}

# ====== in-guest runner: the e2e round drives the booted VM over ssh ======
# vm/run-arch.sh boot forwards host 2222 -> guest 22 (qemu hostfwd). Root ssh
# login is enabled in vm/arch-config.lua services.ssh for this throwaway box;
# password is the public test cred vm/secrets.json.
GUEST_HOST=127.0.0.1
GUEST_PORT=2222
GUEST_USER=root
GUEST_PASS=kodos-root-test
GUEST_SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10"

guest_ssh() { # $* = one command string, run by the guest's /bin/sh
    if command -v sshpass >/dev/null 2>&1; then
        sshpass -p "$GUEST_PASS" ssh $GUEST_SSH_OPTS -p "$GUEST_PORT" \
            "$GUEST_USER@$GUEST_HOST" "$*"
    else
        # no sshpass here: same command, one manual keystroke more. Type the
        # guest password (kodos-root-test) when ssh asks.
        echo "(host has no sshpass: ssh will prompt for $GUEST_PASS)" >&2
        ssh $GUEST_SSH_OPTS -p "$GUEST_PORT" "$GUEST_USER@$GUEST_HOST" "$*"
    fi
}

do_guest() {
    [ $# -ge 1 ] || { echo "usage: vm/run-arch.sh guest '<cmd>'"; exit 2; }
    guest_ssh "$*"
}

do_guest_file() {
    [ $# -ge 1 ] || { echo "usage: vm/run-arch.sh guest-file LOCAL [REMOTE]"; exit 2; }
    [ -r "$1" ] || { echo "no such file: $1"; exit 1; }
    rem=${2:-/root/}
    case $rem in */) rem="$rem$(basename "$1")";; esac
    guest_ssh "cat > '$rem'" < "$1"
    echo "sent $1 -> $GUEST_USER@$GUEST_HOST:$rem"
}

do_boot() {
    [ -f "$DISK" ] || { echo "no $DISK: run 'vm/run-arch.sh install' first"; exit 1; }
    # OVMF probing is distro- AND qemu-version-sensitive (run.sh doctrine).
    QARGS=""
    for pair in \
        "/usr/share/edk2-ovmf/x64/OVMF_CODE.4m.fd /usr/share/edk2-ovmf/x64/OVMF_VARS.4m.fd" \
        "/usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_VARS_4M.fd" \
        "/usr/share/edk2/ovmf/OVMF_CODE.4m.fd /usr/share/edk2/ovmf/OVMF_VARS.4m.fd"; do
        CODE=${pair% *}; VARS=${pair#* }
        [ -r "$CODE" ] && [ -r "$VARS" ] || continue
        [ -f "$VMDIR/arch-vars.4m.fd" ] || cp "$VARS" "$VMDIR/arch-vars.4m.fd"
        QARGS="-machine q35 -drive if=pflash,format=raw,readonly=on,file=$CODE -drive if=pflash,format=raw,file=$VMDIR/arch-vars.4m.fd"
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
    # netdev=n0 + explicit virtio-net-pci property (run.sh doctrine: a NIC
    # without netdev= talks to nothing).
    echo "booting (quit: Ctrl-a x; login: abuss / kodos-user-test, then 'su -')"
    exec qemu-system-x86_64 -m 4096 -smp 2 \
        -drive file="$DISK",if=virtio,format=qcow2 \
        -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 \
        -device virtio-net-pci,netdev=n0 \
        $QARGS \
        -nographic
}

case "$PHASE" in
    install) trap cleanup EXIT; do_install ;;
    guest)      shift; do_guest "$@" ;;
    guest-file) shift; do_guest_file "$@" ;;
    debug)   trap cleanup EXIT; cleanup; attach_nbd; mkdir -p "$MNT"
             mount "${NBD}p2" "$MNT"
             G=$MNT/kod/generation
             echo "--- live etc entry types (hardlinks expected):"
             for f in group passwd; do
                 echo "$f: $(stat -Lc '%F nlink=%h ino=%i' "$MNT/etc/$f" 2>&1) store_ino=$(stat -Lc %i "$G/0/etc_store/$f" 2>&1)"
             done
             echo "--- wants links (systemd enables expected):"
             find "$MNT/etc/systemd/system" -maxdepth 2 -name '*.wants' -exec ls -l {} + 2>/dev/null || true
             echo "--- shell below; exit to detach"
             exec su -s /bin/sh "${SUDO_USER:-root}" ;;
    boot)    do_boot ;;
    all)     trap cleanup EXIT; do_install; do_boot ;;
    *) echo "usage: vm/run-arch.sh [install|boot|fresh|guest '<cmd>'|guest-file LOCAL [REMOTE]]"; exit 2 ;;
esac
