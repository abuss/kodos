# Arch install media — design

Date: 2026-11-18
Status: approved in brainstorming; ready for implementation planning

## Goal

Provide a USB stick and a single-file ISO that install a **bootable Arch Linux** system via the Arch backend, using the same proven `generate.lua` → `lockfile.lua` → `kodos exec` flow as `vm/run-arch.sh`. The existing Alpine install media must remain byte-identical in behavior and output.

Success criteria:

1. `sh build/usb/mkinstall-arch-usb.sh` stages an Arch payload and, with a device argument, writes a bootable isohybrid USB stick.
2. `sh build/usb/mkinstall-arch-iso.sh` produces a single ISO with the same payload grafted at `/kodos`.
3. Booting that media, bringing up Ethernet, and running the shipped installer installs Arch with GRUB EFI, systemd, NetworkManager, ssh, users from a secrets sidecar, and generation 0.
4. `go test ./...`, built-binary `suitetest`, and `selftest` stay green.

## Decisions and constraints

- **Live base:** keep the existing Alpine 3.24 standard ISO as the booted live host. The static `kodos` binary already supports Arch; pacman is not needed at runtime.
- **Target scope:** milestone-1 parity — bootable systemd + GRUB EFI + NetworkManager + ssh + users. No desktop/COSMIC stack.
- **kodos delivery:** ship the static binary as a file (`/usr/bin/kodos`) copied by the bootstrap installer. No local Arch package repo; `self-update` is deferred.
- **Trust oracle:** host `pacman -Sy` with isolated `DBPath`/`CacheDir` runs only at media build time. Pacman is never invoked on the live host or install path. Arch sync DB `desc` files are extracted into the payload and consumed by `alpm.lua`.
- **Alpine isolation:** do not change the Alpine payload, its configs, or its installer. The only shared-script change is one env override with an unchanged default.

## Non-goals

- An Arch archiso/live environment.
- A `.pkg.tar.zst` local repo or in-guest `kodos self-update`.
- Secure Boot support (UEFI-only, fallback GRUB path; Secure Boot must be off).
- Wi-Fi setup on the live stick (wired Ethernet is the install assumption).
- Runtime refresh of Arch DB snapshots (see Risks).

## Architecture

The Arch media is the existing Alpine ISO re-mastered with xorriso, with a different payload at `/kodos`:

```
build host: pacman -Sy (isolated DBPath) -> core.desc/extra.desc
          + static kodos + Arch config + live-host apks
          -> arch-stage/ -> xorriso remaster -> ISO -> dd -> USB

target: boot Alpine live -> copy payload to /root/kodos
      -> apk add live tools only
      -> generate.lua (Arch IR) -> lockfile.lua (ALPM desc) -> kodos exec
      -> copy binary + secrets -> reboot to Arch generation 0
```

No apk repository is used for the target system. The Alpine apks in the payload exist solely to give the live host `parted`, `lsblk`/`blkid`, and `mkfs.ext4`, which the executor's storage ops need.

## Components

| File | Change | Role |
|---|---|---|
| `build/usb/mkinstall-arch-usb.sh` | new | Stages `build/usb/arch-stage/`: static `kodos`, `indexes/arch/{core,extra}.desc`, `config/arch-machine.lua`, `arch-run-install.sh`, `README-arch.md`, and a tiny local apk repo for live tools. With a device argument, builds the ISO and `dd`s it after typed confirmation. `ARCH_MIRROR` and `PACMAN_BIN` are overridable. |
| `build/usb/mkinstall-arch-iso.sh` | new | Fixes `STAGE`/`STAGE_SCRIPT`, sets output `build/usb/iso/kodos-arch-install-<rel>.iso`, and delegates to the existing ISO builder. |
| `build/usb/mkinstall-iso.sh` | one line | Line 31 becomes `OUT=$STAGE STAGE_ONLY=1 sh "${STAGE_SCRIPT:-$REPO/build/usb/mkinstall-usb.sh}" >/dev/null`. Default is unchanged. |
| `build/usb/arch-run-install.sh` | new | Runs on the live stick: live-tool gate, live-stick guard, device override, secrets prompt, `generate.lua` → `lockfile.lua 0 core.desc extra.desc` → `kodos exec`, then copies `/usr/bin/kodos` and `/etc/kodos/secrets.json`. Supports `--dry-run`, `--yes`, `--device`. |
| `build/usb/arch-config/arch-machine.lua` | new | Standalone config: `repos.base.type="arch"` with mirror + core/extra, GRUB EFI, `/dev/sda` GPT+ESP+root, systemd services, root/`abuss` users, sudo/git/mkinitcpio/glibc-locales, fixed `/etc/machine-id`, mkinitcpio hooks without `autodetect`, and `files.config` embedding itself for day-2 management. |
| `build/usb/README-arch.md` | new | Build/boot/edit/install/first-boot walkthrough plus the snapshot-drift caveat. |

## Error handling

- Missing `pacman`, failed `pacman -Sy`, or missing/empty `core.desc`/`extra.desc` aborts staging.
- Missing static `kodos` build or missing live-tool apks aborts staging.
- The installer refuses to target the stick it booted from, and aborts if `lockfile.lua` or `kodos exec` fails.
- `--dry-run` plans against a scratch target root and writes nothing to the target disk.
- Mirror drift between DB snapshot and tarball fetch is the known ceiling: media is snapshot-bound; rebuild media (or restage desc files on a pacman host) when the mirror moves. This is an intentional simplification, marked with a `ponytail:` comment in the staging script.

## Validation plan

1. Test-first: extend `executor/lua/test-generate.lua` to load `../../build/usb/arch-config/arch-machine.lua` and assert the generated IR has distro `arch`, GRUB EFI, core/extra mirror, no apk/local-repo ops, and writes for `/etc/kodos/config.lua` and `/etc/machine-id`. Red before the config exists.
2. `sh -n` every new script.
3. `STAGE_ONLY=1 OUT=/tmp/opencode/arch-stage sh build/usb/mkinstall-arch-usb.sh` on this host; assert payload layout and that `alpm.parse` reads both desc snapshots.
4. `arch-run-install.sh --dry-run --device /dev/nbd99` against the staged payload.
5. Full gates after each change: `go test ./...`, `go build -o kodos . && ./kodos suitetest`, `./kodos selftest`.
6. Human e2e: build media, boot on target/VM, run installer, report findings; fixes land test-first in this worktree.

## Risks

- Rolling Arch mirror can invalidate a build-time snapshot before the media is used; mitigated by rebuilding media, not by runtime pacman.
- Bare-metal hardware module coverage in initramfs: drop `autodetect` so `block`/`filesystems` include the full driver set; larger initramfs is acceptable.
- Writing `/usr/bin/kodos` after `exec` relies on the generation `/usr` symlink layout; verify on the real boot round.
