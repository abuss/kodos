# Bare-metal install USB

How to put kodos on a real machine: build a stick on a dev host, boot the
target from it, run the installer. The stick is an Alpine 3.24 live system
(the installer host) plus a `KODOS` partition carrying the payload (the
static `kodos` binary, its own apk in a local repo, index snapshots, and the
machine config you edit). Package installs download from the Alpine mirror —
**the target needs Ethernet during the install** (Wi-Fi can only be
configured *for the installed machine*, the live stick won't join a network
without it).

Everything below assumes you start from a checkout of this repo on the dev
host.

## 1. Create the USB (dev host)

Requirements: `go`, `curl`, `sgdisk` (gdisk), `mkfs.ext4` (e2fsprogs),
`xorriso` (ISO variant only); util-linux is not needed on the host. The
Alpine ISO (~350MB) and the 3.24 index snapshots download once and cache
under `build/usb/iso/`.

```sh
sh build/usb/mkinstall-usb.sh /dev/sdX
```

`/dev/sdX` is the WHOLE USB stick (`lsblk` to find it) — everything on it is
erased, and the script makes you type the device name again to confirm. It:

1. builds the payload (`kodos` static binary + `kodos.apk` local repo +
   index snapshots + `build/usb/config/` + `run-install.sh`),
2. `dd`s the Alpine standard ISO to the start of the stick,
3. carves a 1 GB ext4 partition labelled `KODOS` in the space after the ISO
   and copies the payload in.

To inspect the payload without touching a device:

```sh
STAGE_ONLY=1 sh build/usb/mkinstall-usb.sh    # -> build/usb/stage/
```

### Alternative: a single ISO file

`build/usb/mkinstall-iso.sh` (host-side) produces
`build/usb/iso/kodos-install-<rel>.iso`: the same Alpine base re-mastered
with xorriso (original isohybrid/BIOS/EFI boot info preserved) with the
payload grafted read-only at `/kodos`. Use it when a single file is the
right shape: VM/CD media, or ISO-only USB writers (Etcher/Ventoy). The
difference from `mkinstall-usb.sh`: the payload there is a writable ext4
partition (edit configs on the stick); here it is read-only, so boot, then:

```sh
cp -a /media/cdrom/kodos /root/kodos      # the live system auto-mounts its ISO
vi /root/kodos/config/machine.lua         # disk device (or keep it and use --device)
sh /root/kodos/run-install.sh --dry-run
sh /root/kodos/run-install.sh --device /dev/vda   # rewrites the stream (like vm/run.sh's nbd sed)
```

Proven end-to-end on plain qemu (no sudo): ISO live → 37-op install onto a
virtio disk → reboot → UEFI `Booting 'Kodos generation 0'` → OpenRC boot on
linux-lts → login → `kodos status` current -> generation 0. Headless VM
testing note: the bare-metal config boots to the *video* console; for
serial-visible boots add `serial_console = true` + a `"console=ttyS0"`
kernel arg to both config files first (exactly what vm/vm-layer.lua does).

## 2. Boot the target from the stick

The stick image is isohybrid: it boots on **BIOS or UEFI** firmware. Notes:

- **Disable Secure Boot.** The installed system's bootloader is
  `grub-install --removable --target=x86_64-efi` — the unshimmed EFI
  fallback path, which Secure Boot refuses. (Legacy-BIOS *firmware* can boot
  the stick fine, but the *installed system* gets no BIOS bootloader:
  UEFI-booted machines only.)
- Use the firmware boot menu (usually F12/F8/Esc at power-on) and pick the
  USB stick.
- Alpine live logs you in as **root with no password** on the console.

## 3. Bring up the payload + network (on the live stick)

```sh
ifconfig eth0 up && udhcpc -i eth0     # or: setup-alpine (pick defaults, "None" for disk)
mount -L KODOS /usb
```

## 4. Edit the machine config (before installing)

`/usb/config/machine.lua` decides what gets installed. The two things to
check every time:

- **the disk**: `storage = { devices = { disk0 = { device = "/dev/sda" } } }`
  — run `lsblk` and point it at the machine's real disk. NEVER the USB stick
  (the installer double-checks and refuses anyway).
- **host_name**.

Also worth knowing:

- Partition layout comes from the base profile (ESP 512MB, root 64GB, home
  remainder, 34GB swap) — placeholder sizes; adjust the layer if the real
  disk disagrees.
- Kernel: the Alpine default variant is `linux-lts` + `linux-firmware`,
  right for real hardware. No override needed.
- Users come from the eszkoz base profile (e.g. `abuss`). Accounts without a
  password hash are created *without a password*: give the root password at
  the install prompt, and add hashes for other users via
  `/usb/config/secrets.json` (`{"users.NAME.hashed_password":
  "<openssl passwd -6 ...>"}`, kept out of git) if you want them usable.
- Wi-Fi for the installed machine: add a `connections` entry with
  `psk = { secret = "wifi.x.psk" }` in the layer and the psk in that same
  secrets.json.

## 5. Install

```sh
sh /usb/run-install.sh --dry-run     # prints the full plan, writes nothing
sh /usb/run-install.sh               # typed `yes`, then it installs
```

The installer runs the proven sequence: generate the action stream, version-
lock it against the index snapshots + local repo, then execute: partition →
format → mount → ship the repo to `/kod/repo` → install ~100 packages from
the mirror → render config → generation 0 + grub → done. It prompts for the
root password (empty = console login without password) unless you shipped
`config/secrets.json`; that file also lands on the machine.

If it aborts, the flow is idempotent: an existing kodos partition table is
kept, existing filesystems are fsck-repaired instead of reformatted — fix
the config and re-run.

## 6. First boot + day-2 management

```sh
reboot            # pull the stick; grub entry "Kodos generation 0" boots
```

Login as root (or the console account). The machine now manages itself:

```sh
kodos status                  # generations + which is current
vi /etc/kodos/config.lua      # the machine's OWN config (repo points at /kod/repo)
kodos apply --dry-run && kodos apply
kodos rollback                # boot the previous generation
kodos self-update             # after bumping kodos.apk into /kod/repo + applying
```

Verify at first boot: `kodos status` shows generation 0 current,
`ls /kod/generation`, `uname -r` = the linux-lts version.

## Troubleshooting

- `ERROR: blkid missing` — you booted something that is not the Alpine
  standard stick (the installer checks util-linux).
- `ERROR: ... is the USB stick this script runs from` — wrong disk name in
  `machine.lua`; `lsblk`, edit, re-run.
- `$DEV is not a block device` — same; the config must name a disk that
  exists on THIS machine.
- No network: `udhcpc -i <iface>` (`ip link` to list interfaces); the
  install downloads from `dl-cdn.alpinelinux.org`.
- The grub menu has exactly **one** entry: the current generation (by
  design — generation switching is `kodos rollback`, not a grub submenu).
  If that entry fails to boot: live USB → `mount /dev/sda2 /mnt/x &&
  mount /dev/sda1 /mnt/x/boot`, `for f in dev proc sys; do mount --rbind
  /$f /mnt/x/$f; done`, then `chroot /mnt/x kodos rollback`.
