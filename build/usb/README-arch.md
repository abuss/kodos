# Arch install USB / ISO

Put a bootable Arch generation on a real machine. The **live host is still the
Alpine 3.24 standard ISO**; the payload grafted at `/kodos` carries a static
`kodos` binary, Arch sync-DB `desc` snapshots, and a standalone Arch machine
config. Pacman is only the **build-time DB trust oracle** — it never runs on
the live stick or on the installed target. The target has **no local Arch
repo, no `kodos.apk`, and no `self-update`**: static `/usr/bin/kodos` only.

The target is milestone-1 parity: GRUB EFI, systemd, NetworkManager, ssh,
users. No desktop.

## 1. Create the USB or ISO (dev host)

Requirements: `go`, `curl`, `xorriso`, `tar`. Host `pacman` is used to sync
and verify the Arch DB snapshots with its own keyring; as non-root, stage the
desc files first and point staging at them:

```sh
# root pacman available (sudo sh ...): builds ISO + writes the whole stick
sudo sh build/usb/mkinstall-arch-usb.sh /dev/sdX

# non-root: reuse desc snapshots staged by an earlier pacman run
ARCH_DESC_DIR=/tmp/opencode/arch sh build/usb/mkinstall-arch-usb.sh /dev/sdX

# ISO alone (no device touched)
ARCH_DESC_DIR=/tmp/opencode/arch sh build/usb/mkinstall-arch-iso.sh
```

`ARCH_MIRROR=...` changes only the build-time desc source. The target's mirror
is in `config/arch-machine.lua` and must be edited separately if it differs.

`/dev/sdX` is the WHOLE USB stick (`lsblk` to find it) — everything on it is
erased, and the script makes you type the device name again. It:

1. stages `bin/kodos`, `indexes/arch/{core,extra}.desc`,
   `config/arch-machine.lua`, `arch-run-install.sh`, `README-arch.md`, and the
   Alpine live-tool apks under `build/usb/arch-stage/`;
2. builds `build/usb/iso/kodos-arch-install-<rel>.iso` with the Alpine boot
   info preserved and the payload read-only at `/kodos`;
3. `dd`s that ISO onto the stick.

Inspect the payload without touching a device:

```sh
ARCH_DESC_DIR=/tmp/opencode/arch STAGE_ONLY=1 sh build/usb/mkinstall-arch-usb.sh
```

The Alpine media path (`mkinstall-usb.sh`, `mkinstall-iso.sh`, `run-install.sh`,
`build/usb/config/`) is unchanged. The shared ISO builder only gained a
`STAGE_SCRIPT` env override with the Alpine script as its default.

## 2. Boot the target from the stick

The ISO is isohybrid: **BIOS or UEFI**.

- **Disable Secure Boot.** The installed GRUB uses the unshimmed EFI
  fallback path.
- Pick the stick in the firmware boot menu (F12/F8/Esc).
- Alpine live logs in as **root with no password** on the console.
- Use **wired Ethernet**: the live stick will not join Wi-Fi.

## 3. Bring up the payload + network (on the live stick)

```sh
ifconfig eth0 up && udhcpc -i eth0
D=$(find /media /run/media -maxdepth 4 -name arch-run-install.sh | head -1); D=${D%/*}
cp -a "$D" /root/kodos
```

The live auto-mounts its boot media, so no manual `mount` is needed.

## 4. Edit the machine config

`/root/kodos/config/arch-machine.lua` is standalone (no Alpine `system.lua`
layer). Check every time:

- **`storage.devices.disk0.device`**: the machine's real disk, never the USB
  stick. Identify disks with `cat /proc/partitions` before `lsblk` is
  installed. `--device /dev/sdX` overrides it for one run.
- **`system.host_name`**.
- `repos.base.mirror` defaults to `https://mirror.rackspace.com/archlinux`;
  change it if the target has a closer mirror. `core` + `extra` are the only
  branches.
- `files.mkinitcpio` deliberately drops `autodetect`: on bare metal it would
  record the build host's modules, not the target's. The larger initramfs is
  intentional.
- Passwords are never stored in the config. They are prompted at install time
  into a 0600 secrets sidecar, or shipped as `/root/kodos/config/secrets.json`
  with `{"users.root.hashed_password": "...", "users.abuss.hashed_password":
  "..."}` keys. Leaving a password empty leaves that account **locked**, not
  passwordless.

## 5. Install

```sh
sh /root/kodos/arch-run-install.sh --dry-run
sh /root/kodos/arch-run-install.sh          # typed `yes`
```

The dry-run prints the whole action stream (`DRY-RUN configure_repos`,
`DRY-RUN install_packages`, ...) and writes nothing to the target. The real
run partitions/formats/mounts the disk, locks packages against the archived
desc snapshots, renders config and generation 0 + GRUB, then installs the
static binary at `/usr/bin/kodos` and the secrets at
`/etc/kodos/secrets.json`.

**Existing partition table:** the flow never rewrites a table it did not write.
A disk whose table is not exactly the config's layout (different partition
count, or partitions not named `boot`/`root`) is refused, and a disk whose own
partition is mounted or is active swap is refused outright — that is what
protects the stick you booted from. To install over a foreign or half-done
table, add `--force-wipe`: it clears the table and every filesystem signature
on that disk before the layout is written, and the confirmation then makes you
type the device path instead of `yes`. Only the installers can wipe; a day-2
`kodos apply` never carries that field.

**Snapshot drift:** Arch is rolling. If the mirror no longer serves a locked
filename, the executor retries the same filename on `archive.archlinux.org`
and still verifies its SHA256. If the archive also fails, refresh the desc
snapshots on a pacman host and rebuild the media. The lock is fail-closed by
design.

## 6. First boot + checks

```sh
reboot       # pull the stick; GRUB entry "Kodos generation 0" boots
```

Log in as `root` (or `abuss`). Verify with `kodos status`,
`ls /kod/generation`, and `systemctl status NetworkManager ssh`.

For the first real hardware round, also check that the static binary and the
secrets sidecar landed in the active generation:

```sh
ls -l /usr/bin/kodos /etc/kodos/secrets.json
kodos status
kodos rollback && kodos status   # both files must still be there
```

`/etc/kodos/config.lua` (and its `arch-machine.lua` sibling) is a readable
record of the installed configuration. Day-2 in-place apply **is supported**:
`kodos apply` re-reads the config, fetches the live `core` and `extra` sync DBs
from the configured mirror over HTTPS, locks packages, and builds + activates a
new generation. Package fetch still verifies SHA256 and falls back to
`archive.archlinux.org` when the mirror drops a pinned file. Fresh installs ship
`ca-certificates` so the target can validate those HTTPS mirrors.

There is **no self-update** on Arch: `kodos apply` rebuilds the *system*
generation, not the `/usr/bin/kodos` binary. Apply does carry the active binary
into every new generation, so it stays available after generation changes and
rollback. To change `kodos` itself, replace `/usr/bin/kodos` directly from
fresh media.

`/root` and `/home` are single persistent directories on the live root, not
copies inside each generation. SSH keys, shell history and user files survive
applies and rollbacks. The first apply with this build migrates an older target
whose `/root`/`/home` were generation-scoped symlinks, preserving their data.

A target whose `/usr/bin/kodos` predates the embedded-CA-bundle fix cannot reach
the HTTPS mirror, so `kodos apply` fails with a TLS `x509` error. Replace the
binary from fresh media (Arch has no `self-update`); installing `ca-certificates`
alone is not enough — see Troubleshooting.

## Troubleshooting

- `ERROR: ... is the USB stick this script runs from` — wrong disk in
  `arch-machine.lua`; pick the machine's own disk with `lsblk` and re-run.
- `$DEV is not a block device` — same; the config must name a disk present on
  THIS machine.
- `ERROR: blkid missing` — you did not boot the Alpine standard stick.
- `lock_drift` / unresolved package — desc snapshots are stale; rebuild the
  media on a pacman host and re-run.
- `fetch <name>-<version>: HTTP 404` — the mirror dropped the pinned file;
  archive fallback is automatic. If it also fails, refresh media.
- `download: context deadline exceeded` on a big package (the kernel) — the
  download outran the per-package fetch ceiling on a slow link. First choice:
  rebuild the media with a fresh `pacman -Sy` snapshot (`ARCH_DESC_DIR` unset,
  so pacman is the DB oracle) so the mirror serves the pinned version directly
  and the archive fallback is not needed. For one slow run, raise the ceiling:
  `KODOS_PKG_FETCH_TIMEOUT=45m sh /root/kodos/arch-run-install.sh …`.
- `x509: certificate signed by unknown authority` on `kodos apply` — the target
  has **no on-disk CA bundle**. Arch's `ca-certificates` is a meta-package; the
  real bundle (`/etc/ca-certificates/extracted/tls-ca-bundle.pem`) is generated
  by `/usr/bin/update-ca-trust` from a **libalpm hook**, which kodos never runs
  (it extracts packages directly), leaving `/etc/ssl/certs/ca-certificates.crt`
  a dangling symlink. Current builds embed the Mozilla CA set, so no on-disk
  bundle or env var is needed — replace `/usr/bin/kodos` from fresh media. Older
  binaries cannot be fixed with `SSL_CERT_FILE` unless a real bundle exists on
  disk; generate one first with `/usr/bin/update-ca-trust` on the target.
- No network — `ip link` then `udhcpc -i <iface>`; the install needs the
  Arch mirror.
- `apk add` on the live host — only equips Alpine with `parted`, `mkfs.ext4`
  and `lsblk`; it never touches the Arch target.
- Boot stops at GRUB — Secure Boot is still on, or GRUB was installed to a
  non-removable path; re-check the firmware setting.
- Silent boot under `qemu -nographic` — the shipped config uses
  `console=tty0 console=ttyS0,115200` and enables `serial-getty@ttyS0`.
  Older media installed with `quiet` and no serial console: reinstall from
  fresh media, or patch `/boot/grub/grub.cfg` on the target's ESP as a test.
- Keyboard cannot edit the GRUB menu — older media rendered
  `terminal_input serial` only. Fresh media enables console input too when the
  kernel args contain `console=ttyS0`.
