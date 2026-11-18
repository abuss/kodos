# Arch Backend Milestone 1: Bootable Arch VM End-to-End — Design

**Goal:** `kodos exec` installs a distro-neutral eszkoz config into a qcow2 disk
as an Arch Linux system that boots with systemd, proving the Arch backend
end-to-end with the same flow that proves Alpine today.

**Decisions already taken (context):**
- Config/generator side already supports Arch (`backends/arch.lua`,
  `resolve.lua` backend registry, `generate.lua` emits `distro = "arch"`).
  Verified against real Arch indexes (PORTABILITY.md).
- Pacman is the trust oracle only (its host keyring); kodos keeps its own
  resolution (Lua lockfile) and its own extraction/store. No pacman on the
  exec path. File placement never delegates to the distro tool.

## Success criteria

`vm/run-arch.sh` (new sibling of `vm/run.sh`; the Alpine flow stays untouched)
runs the same phases as the Alpine rounds:

1. `generate.lua` (system.lua + arch vm layer + eszkoz layers) emits
   `actions.json` with `distro: "arch"`.
2. DB snapshots are fetched/verified, `lockfile.lua` emits
   `generation-0.json` pinning every package to exact version + sha256.
3. `kodos exec -actions ... -metadata ... -root <mnt> -secrets ...` installs
   into `vm-disk.qcow2` via nbd, same op stream, no distro-specific ops.
4. `run-arch.sh boot` boots the guest under OVMF: systemd reaches
   multi-user.target, serial getty answers, ssh (hostfwd 2222) works with the
   test users.
5. `run-arch.sh guest` checks: `systemctl is-enabled` reports the configured
   services, ` pacman -Qs`-free (kodos-managed) store content matches the
   pins, hostname/locale/timezone files are the rendered ones.
6. Re-running `install` is idempotent (new generation, same bootability),
   mirroring the Alpine rounds.

## Explicit non-goals (milestone 2+)

- In-guest self-management: `kodos apply`, `kodos rollback`, self-update
  rounds on Arch (the `manage.go` distro gate stays Alpine-only for now, but
  the manifest `distro` field must record `"arch"` honestly).
- Local `repositories` section (`copy_tree` + local repo) on Arch.
- Flatpak, home services (`systemd-user` mechanism remains a stub note).
- `.INSTALL` scriptlet execution — bypassed explicitly, exactly as apk
  TRIGGERS are bypassed today. Kernel bootability is executor-owned.
- pacman-key bootstrapping inside the guest (base ships the keyring;
  `pacman-key --init` on first boot is an admin step, out of scope).

## Trust and resolution model

Arch is rolling: the host pacman DB cannot pin versions a snapshot lockfile
references. The split therefore differs from the apk flow:

| Piece | Owner | Mechanism |
|---|---|---|
| Sync DB trust | host pacman keyring | `run-arch.sh` fetches `core`/`extra` `.db.tar.gz` + `.db.sig`, verifies `gpg --homedir /etc/pacman.d/gnupg --verify`, then `bsdtar -xO` extracts the flat-text DB into `/tmp/opencode/` snapshots (same pattern as the Alpine APKINDEX snapshots) |
| Dependency closure + pins | `lockfile.lua` | new ALPM DB parser + resolver. DB record format is flat text stanzas: `%NAME% %VERSION% %DESC% %CSIZE% %MD5SUM% %SHA256SUM% %BUILDDATE% %PACKAGER% %URL% %LICENSE% %PROVIDES% %DEPENDS% %OPTDEPENDS% %CHECKSUM% %BUILDDIR% %FILES%` (check `%CHKSUM%` for the file hash). Resolution: iterative closure over `%DEPENDS%` + `%PROVIDES%` (versioned provides respected), optdeps excluded. Output pins: `{name, version, checksum(sha256), repo}` in `generation-N.json` |
| Package fetch | executor | download `$mirror/$repo/os/$arch/${name}-${version}.pkg.tar.zst`, verify sha256 against the pin, then extract. Chain of trust: signed DB → lockfile pins → checksum-verified tarballs |
| File placement | executor | own `.pkg.tar.zst` extraction into the generation store (unchanged architecture: read-only store + hardlinked generation) |

**Known ceiling (ponytail comment):** snapshot→exec race on a rolling mirror
(DB moves between lockfile run and download). Mitigation: run-arch.sh refetches
the DB immediately before exec. Upgrade path when this bites: pin a TimeMachine
mirror in `repos.base.mirror`.

## Executor changes (branch on `meta.Distro`, keep one op stream)

The IR op set and ordering (`ir.go`) do **not** change. Distro behavior branches
inside the ops:

- **Gate:** `main.go` accepts `distro: "arch"` alongside `"alpine"`;
  unknown distros still fail closed.
- **`configure_repos`:** for arch render `/etc/pacman.conf` (+
  `/etc/pacman.d/mirrorlist`) from the same action fields
  (`type/mirror/branches`); alpine keeps `/etc/apk/repositories`.
- **Store/install:** `.pkg.tar.zst` extraction (zstd via
  `github.com/klauspost/compress/zstd`, archive/tar); per-package metadata from
  the `.PKGINFO` inside the archive (`pkgname/pkgver`, `%BACKUP%`-style
  `backup =` entries for config files). Config-conflict path reuses the
  existing `.apknew` hardlink machinery with `.pacnew` naming for arch.
- **Users/groups:** `create_user` uses `useradd`/`groupadd` (shadow, part of
  Arch `base`) instead of busybox `adduser`; home-dir facts per the existing
  `homedirs.go` notes.
- **Kernel/initramfs (`install_bootloader`):** modules dir at
  `usr/lib/modules/<ver>` in the gen tree (Arch is usrmerged); run `depmod
  <ver>` then `mkinitcpio -o /boot/initramfs-<flavor>.img <ver>` in the gen
  chroot, where `<flavor>` maps from the shipped `/boot/vmlinuz-<flavor>`
  (linux, linux-lts, ...). Hollow-image guard: the cpio content check looks
  for Arch initramfs content (systemd-cpio/init) instead of busybox.
- **`enable_service`:** systemd enabling is just wants-symlinks under
  `/etc/systemd/system/{multi-user.target,getty.target}.wants/` written as
  generation content — no `systemctl` needed in the chroot (offline enable).
  The `runlevel` IR field maps to the target name for arch
  (`boot`/`default` → `multi-user.target`, `sysinit` stays reserved-unused).
  `reconcileRunlevels` gains the wants-symlink equivalent reconciliation.
- **System files (`configure_locale/keyboard/timezone`):** arch writes
  `/etc/locale.conf` + `/etc/vconsole.conf` and runs `locale-gen` in the
  chroot (glibc ships it in the package); timezone = `/etc/localtime` symlink
  into tzdata only (modern Arch dropped `/etc/timezone`; UTC-vs-local is
  `/etc/adjtime`/RTC convention, not touched in this milestone).
- **Serial console:** `backends/arch.lua` declares the unit
  (`serial-getty@ttyS0.service` wants-symlink); `generate.lua`'s
  serial-console branch accepts either `backend.inittab` (alpine) or
  `backend.serial_console_unit` (arch) and fails closed when neither exists.

**Filesystem-layout audit:** every chroot path assumption gets re-checked for
usrmerge (`/lib` → `/usr/lib` symlinks ship inside Arch's `filesystem`
package; the generation tree reproduces them, so chrooted tools are fine, but
executor Go code that scans `gen/lib/modules` must scan `gen/usr/lib/modules`).

## Generator-side additions (small)

- `backends/arch.lua`: `serial_console_unit`, plus whatever the lockfile
  plumbing needs to know repo names (`core`/`extra`) — branches already carry
  them.
- `lockfile.lua`: ALPM DB mode alongside APKINDEX mode (same pins output
  schema, plus `repo` + `checksum` fields; existing consumers unchanged).

## VM harness

`vm/run-arch.sh`: same cleanup/nbd/idempotence skeleton as `run.sh`
(structure copied, Alpine specifics swapped): Arch DB snapshot fetch + sig
verify, `generate.lua` with an `arch-layer.lua` vm layer (new: sets
`repos.base = { type = "arch", mirror, branches = {core, extra} }`, kernel
variant default, keeps users/services/ssh test facts), `lockfile.lua` in ALPM
mode, no `mkrepo/mkapk`, `kodos exec` without `-apk`.

## Testing

- **Lua suites** (`test-lock.lua` style): ALPM DB parser on a fixture DB
  (hand-written text record), closure over depends + versioned provides,
  optdeps excluded, pin schema carries sha256 + repo.
- **Go unit tests:** pacman.conf/mirrorlist rendering; `.PKGINFO` parsing;
  `.pkg.tar.zst` extraction into the store from a fixture archive built in
  the test (tar.gz→zstd via the vendored lib); `.pacnew` conflict path;
  wants-symlink enable + reconciliation; initramfs naming
  (`initramfs-<flavor>.img`) and the arch hollow-image guard; distro gate
  (arch accepted, `deb` still rejected).
- **E2E:** `vm/run-arch.sh install && boot`, then `guest` assertions per the
  success criteria; a second `install` run proving idempotence.
- **Equivalence:** the existing `test-equivalence.lua` contract (Lua
  generator ↔ Go executor) must keep passing for alpine; new arch generate
  fixtures where the contract branches.

## Error handling

Same posture as the Alpine spike: fail closed before mutating — unknown
distro, missing pin checksum, sha256 mismatch, missing `%CHKSUM%`, no
`vmlinuz-<flavor>` for a modules dir, hollow initramfs, unverified DB
signature (script aborts before exec). No silent fallbacks from arch to alpine
paths: every branch is explicit.
