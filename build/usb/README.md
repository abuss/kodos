# Bare-metal install USB

How to put kodos on a real machine: build a stick on a dev host, boot the
target from it, run the installer. The stick is the kodos **install ISO**
(an Alpine 3.24 live system re-mastered with xorriso, with the payload —
the static `kodos` binary, index snapshots, a local repo of live-installer
tools, and the machine config you edit — grafted read-only at `/kodos`) `dd`'d
onto the whole stick. Package installs download from the Alpine mirror —
**the target needs Ethernet during the install** (Wi-Fi can only be
configured *for the installed machine*, the live stick won't join a network
without it).

Everything below assumes you start from a checkout of this repo on the dev
host.

## 1. Create the USB (dev host)

Requirements: `go`, `curl`, `xorriso`; util-linux is not needed on the host.
The Alpine ISO (~350MB) and the 3.24 index snapshots download once and cache
under `build/usb/iso/`.

```sh
sh build/usb/mkinstall-usb.sh /dev/sdX
```

`/dev/sdX` is the WHOLE USB stick (`lsblk` to find it) — everything on it is
erased, and the script makes you type the device name again to confirm. It:

1. stages the payload (`kodos` static binary + index snapshots + a local
   repo with live-installer tools + `build/usb/config/` + `run-install.sh`),
2. builds `build/usb/iso/kodos-install-<rel>.iso` (Alpine standard ISO
   re-mastered with xorriso: original isohybrid BIOS/EFI boot info
   preserved, payload grafted read-only at `/kodos`),
3. `dd`s that ISO onto the stick.

To inspect the payload without touching a device:

```sh
STAGE_ONLY=1 sh build/usb/mkinstall-usb.sh    # -> build/usb/stage/
```

The ISO alone (no stick) is also a first-class artifact — VM/CD media or
ISO-only writers (Etcher/Ventoy): `sh build/usb/mkinstall-iso.sh`.

> **Retired variant (2026-11-16 field round):** the earlier design — Alpine
> ISO `dd`'d plus a carved ext4 `KODOS` payload partition — is dead on both
> ends: `sgdisk` refuses the ISO's hybrid-DOS table (`Invalid partition
> data!`; `parted` works), and on the live the partition is *unmountable*:
> the automounter holds the whole disk as ISO9660 at `/media/vda`, and busybox
> `mount`/`losetup` exclusive-opens on its partitions fail EBUSY while that
> holds. It also solved nothing — the installer always works from the copy it
> makes under `/root/kodos`. Burn the ISO instead.

## 2. Boot the target from the stick

The burned ISO is isohybrid: it boots on **BIOS or UEFI** firmware. Notes:

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
ifconfig eth0 up && udhcpc -i eth0   # or: setup-alpine (defaults, "None" for disk)
D=$(find /media /run/media -maxdepth 4 -name run-install.sh | head -1); D=${D%/*}
cp -a "$D" /root/kodos
```

The live auto-mounts its boot media — burned-to-stick lands at
`/media/<devname>` (e.g. `/media/vda`), optical/VM CD at `/media/cdrom`; the
`find` above covers both, no manual mounting (busybox `mount -L` does not
exist on this live image anyway).

## 4. Edit the machine config (before installing)

`/root/kodos/config/machine.lua` decides what gets installed. The two things
to check every time:

- **the disk**: `storage = { devices = { disk0 = { device = "/dev/sda" } } }`
  — identify disks with `cat /proc/partitions` (`lsblk` does not exist on
  the live until run-install.sh installs util-linux). Point it at the
  machine's real disk. NEVER the USB stick (~370MB in /proc/partitions;
  the installer double-checks and refuses anyway).
- **host_name**.

Also worth knowing:

- Partition layout comes from the base profile (ESP 512MB, root 64GB, home
  remainder, 34GB swap) — placeholder sizes; the fixed parts already total
  ~99GB, so the target disk must be >= ~100GB (qemu test disks smaller than
  that fail with `no room on a NNMB disk`). Adjust the layer if the real
  disk disagrees.
- The machine config ships a trimmed `/etc/mkinitfs/mkinitfs.conf` (no
  `kms`): Alpine's default sweeps the GPU driver trees + all their firmware
  blobs into every initramfs (~100MB+), and minimal UEFI firmwares (e.g.
  NanoVM) refuse to load such a big initrd. Only disk/net drivers are
  needed before the root mount; the GPU comes up under openrc.
- Kernel: the Alpine default variant is `linux-lts` + `linux-firmware`,
  right for real hardware. No override needed.
- Users come from the eszkoz base profile (e.g. `abuss`). Accounts without a
  password hash are created *without a password*: give the root password at
  the install prompt, and add hashes for other users via
  `/root/kodos/config/secrets.json` (`{"users.NAME.hashed_password":
  "<openssl passwd -6 ...>"}`, kept out of git) if you want them usable.
- Wi-Fi for the installed machine: add a `connections` entry with
  `psk = { secret = "wifi.x.psk" }` in the layer and the psk in that same
  secrets.json.

## 5. Install

```sh
sh /root/kodos/run-install.sh --dry-run   # prints the full plan, writes nothing
sh /root/kodos/run-install.sh             # typed `yes`, then it installs
                                          # (add --device /dev/sdX to override the config)
```

The installer runs the proven sequence: generate the action stream, version-
lock it against the index snapshots, then execute: partition → format →
mount → install ~100 packages from the mirror → render config → generation
0 + grub → install the static `kodos` binary into that generation → done.
It prompts for the root password (empty = console login without password)
unless you shipped
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
vi /etc/kodos/config.lua      # the machine's OWN config
kodos apply --dry-run && kodos apply
kodos rollback                # boot the previous generation
```

`kodos` is a static `/usr/bin/kodos` carried into every new generation by
`kodos apply` — there is no `kodos.apk` and no self-update. To upgrade the
manager itself, replace that binary from fresh media (boot the stick, copy
its `bin/kodos` over the installed `/usr/bin/kodos`).

Verify at first boot: `kodos status` shows generation 0 current,
`ls /kod/generation`, `uname -r` = the linux-lts version, and
`ls -l /kod/generation/0/boot/initramfs-lts` ≈ 20–30MB (a ~180MB image
means the mkinitfs trim above did not land — see that bullet).

## Troubleshooting

- `ERROR: blkid missing` — you booted something that is not the Alpine
  standard stick (the installer checks util-linux).
- `ERROR: ... is the USB stick this script runs from` — wrong disk name in
  `machine.lua`; `cat /proc/partitions` on the live (no lsblk there), edit, re-run.
- `$DEV is not a block device` — same; the config must name a disk that
  exists on THIS machine.
- No network: `udhcpc -i <iface>` (`ip link` to list interfaces); the
  install downloads from `dl-cdn.alpinelinux.org`.
- `apk add` finds nothing on the live: by design, `/etc/apk/repositories`
  there points only at the media dir (no main/community lines). Nothing to
  fix — `run-install.sh`'s start gate installs its own pinned
  parted/util-linux **offline from the payload repo**, and the installed
  system's repos are written into the TARGET by the installer, not
  inherited from the live. To grab something extra yourself:
  `apk add --repository http://dl-cdn.alpinelinux.org/alpine/v3.24/main <pkg>`.
- The grub menu lists **every surviving generation**: entry 0 is the
  current one (kernel at the ESP root), the rest boot older generations'
  kernel+initramfs from `boot/gen-N/` — a recovery path when a
  kernel/mkinitfs step goes sideways. Note the older entries boot that
  generation's *kernel*, but the mounted root/userspace is still whatever
  `current` points at: generation switching is `kodos rollback`, not a
  grub pick. If entry 0 fails to boot: live USB → `mount /dev/sda2 /mnt/x &&
  mount /dev/sda1 /mnt/x/boot`, `for f in dev proc sys; do mount --rbind
  /$f /mnt/x/$f; done`, then `chroot /mnt/x kodos rollback`.
- `ERROR: lock_drift: X resolves to A but metadata pins B`: the executor
  is fail-closed by design — the ISO froze its index snapshots at build
  time and the mirror has since rolled that package. The media's
  `run-install.sh` refreshes both indexes from the mirror at startup, so
  current media should never hit this; on an older burn, refresh them in
  the live copy and re-run:
  `for r in main community; do wget -qO- https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz | tar -xzO APKINDEX > /root/kodos/indexes/$r-324.apkindex; done`
- A boot dies in grub with `start_image() ... LOAD_ERROR`: the initramfs is
  too big for that firmware (see the mkinitfs bullet in section 4 — this is
  exactly how the NanoVM box failed at 180MB).
- `depmod: ERROR: fstatat(3, vmlinuz): No such file or directory` at the end
  of an older install: benign. `/lib/modules/<abi>/vmlinuz` points at
  `/boot/vmlinuz-lts` on the ESP, which the same op copies only *after*
  depmod; depmod still exits 0 and writes a complete `modules.dep`. Fixed in
  r141 — depmod now runs in the generation chroot, which already has the
  kernel file.
- `supervise-daemon: cosmic-greeter: respawning too fast`: three independent
  causes, all fixed in the image (2026-11-17 field round) — check them first
  on an older install:
  `ls -ld /var/run` must be a symlink whose `readlink -f` is the live `/run`
  tmpfs (a real dir, or `../run` landing in `/kod/generation/<N>/run`,
  breaks zbus's hardcoded `/var/run/dbus/system_bus_socket`, so
  `cosmic-greeter-daemon` exits with `InputOutput(NotFound)` and
  `cosmic-greeter-daemon: respawned too many times` follows);
  `ls /usr/lib/security/pam_gnome_keyring.so` must exist (`cosmic-greeter.pam`
  lists it; when absent PAM marks it faulty and greetd dies with
  `pam_setcred: MODULE_UNKNOWN`). Reinstalling a fresh ISO fixes all three —
  the generation builder now re-anchors links that escape a generation
  top-level dir, and the config carries the PAM module. The module alone is
  enough for login (`gkr-pam: gnome-keyring-daemon didn't start properly` in
  syslog is cosmetic); add `gnome-keyring` only if apps need the Secret
  Service.
- COSMIC greeter shows but login says `failed to start /tmp/dbus-XXXXXX:
  Permission denied`: `start-cosmic` runs `dbus-run-session`, which creates
  its private socket dir in `/tmp`. Package directory modes are content too —
  alpine-baselayout ships `/tmp` and `/var/tmp` as 1777, `/root` as 0700 —
  and the image now preserves them through APK unpack and generation
  assembly. `/tmp` is also no longer a per-generation symlink: it is a shared
  tmpfs mount (1777, cleared each boot), so temp state never lands in a
  generation snapshot. `/var/tmp` stays one shared on-disk directory.
  On an older install: `chmod 1777 /tmp /var/tmp` (follows the generation
  symlink) and `chmod 0700 /root` unblocks login until you reinstall from a
  fresh ISO.
- greetd says `unable to set working directory: No such file or directory`
  on the VT right after `kodos apply` (and even a grub pick of the old
  generation still fails while `current` points at the new one): the new
  generation is missing `/var/lib/cosmic-greeter`. Alpine's
  `cosmic-greeter-openrc` pre-install creates that home via `adduser -h`,
  which silently no-ops when the user already exists in the generation's
  seeded `/etc/passwd`, and `/var` is generation-scoped. Fixed in r139+:
  the generation builder recreates every `/var/lib/<svc>` home named in
  passwd after the package hooks. On an older install:
  `mkdir -p /var/lib/cosmic-greeter && chown cosmic-greeter:cosmic-greeter /var/lib/cosmic-greeter`,
  then `rc-service cosmic-greeter restart`.
