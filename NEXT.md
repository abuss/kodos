# Alpine Kodos — where we are, what is next

Snapshot 2026-11-13. Review of the config schema work (`example/eszkoz/`,
~1000 lines of Lua + docs) and the ranked path from here.

## Where the schema landed (settled decisions)

| Decision | Status |
|---|---|
| Lua generates commands; compiled executor executes | agreed, untouched since evaluation |
| Storage = generation-apks model from day one (store + symlinks + etc/etc_default/etc_store) | in schema (`generations` section); no btrfs anywhere |
| Config is distro-neutral; distro declared once (`repos.base.type`) | **proven**: same config drives Alpine v3.24 / Arch / trixie |
| Backends carry distro truth (map/drop, units, kernel variants, locales) | 3 backends, all names index-verified |
| Two config forms (monolithic + modular) | equivalence-tested (`test-equivalence.lua`) |
| Services/users: plain dicts keyed by neutral name/username | implemented + checked |
| Kernel: neutral `boot.kernel.variant` | implemented + checked, hard error on unofferable variant |
| Validation against real distro indexes | `check-packages.lua`, all three green |
| Layering: base + machine + user, `enable = false`, dict-keyed users | **implemented + tested** (`resolve.lua`, `test-merge.lua`) |
| Flatpak: `repos.flatpak` + `{repo="flatpak"}` tags (system + per-user scope), backend tool package, strict remote rule | **implemented + tested** |
| Version lock: `generation-N.json` pins every name to version+checksum | **implemented + tested** (`lockfile.lua`, `test-lock.lua`) |
| IR contract + reference Lua generator | **implemented + tested** (`IR.md`, `generate.lua`, `test-generate.lua`) |
| Executor language: **Go** (static binary, stdlib JSON/tar/gzip, cross-compile) — spike settled it | **decided 2026-11-12** (`executor/`) |
| Networking: service-keyed `connections{}` dict (wifi/ethernet), `configure_network` IR op, NM keyfile rendering | **implemented + tested** (machine layer carries site networks) |
| Secrets: raw values inexpressible; only `{secret="dotted.key"}` refs + field presence; 0600 `--secrets FILE` = executor's job | **config/IR side done**; resolver consumption lands in executor |

## What is still paper, not code

1. ~~**Layering/merge is claimed but not implemented.**~~ **DONE
   2026-11-11**: `resolve.lua` (deep merge copied from proc-lua-conf
   `demo.lua`; `services` and `users` are dicts keyed by name so layers
   merge them natively), `layers/machine.lua` +
   `layers/user.lua`, `test-merge.lua` proves disable/override/append
   semantics; layered configs pass `check-packages.lua` on all three
   distros.
2. ~~**No generator.**~~ **DONE 2026-11-11 (reference)**: `IR.md` is the
   Lua↔executor contract (23 ops, strict order, idempotent); `generate.lua`
   emits the JSON action stream for any backend (alpine: 30 actions from
   the layered eszkoz config); `test-generate.lua` pins shape, order,
   layering effects, determinism, hard errors. The **compiled executor**
   that consumes this stream still does not exist.
3. ~~**No validator/schema for this dialect.**~~ **DONE 2026-11-12**:
   `validate.lua` — data-driven schema of the whole dialect (sections,
   records, dicts, lists, tagged packages, enums, required/optional,
   scalar service settings); runs on the MERGED config inside
   `generate.lua` and `check-packages.lua`; `test-validate.lua` proves
   clean configs pass and 14 seeded errors are caught. Honest limit:
   typo'd *service-settings* keys are indistinguishable from real ones
   without backend settings knowledge (documented in validate.lua).
4. ~~**No version pinning.**~~ **DONE 2026-11-12**: `lockfile.lua` —
   parses real apk index text/`.tar.gz`, locks every `install_set` name to
   `{version, checksum}` (first index wins = repo priority), emits
   `generation-N.json` (deterministic; timestamp/config_hash stamped by
   executor); flatpak ids stay floating (Flathub = future online lock).
   `test-lock.lua`: 88 pkgs pinned for layered eszkoz on v3.24,
   byte-deterministic, missing package = hard error. IR.md "Version lock"
   section defines the executor contract.
5. ~~**No executor**, and the language choice (Go/Nim/Rust) is still open.~~
   **SPIKE DONE 2026-11-12** (`executor/`, Go 1.27): consumes
   `actions.json` + `generation-N.json` with contract checks (unknown/op-out-
   of-order/wrong-schema_version = hard error before any syscall).
   Implemented: `configure_repos`, `write_file`, `install_packages`
   (download + unpack pinned .apks into the store, idempotent),
   `configure_network` (NM keyfile 0600, secrets resolved from `--secrets`
   JSON), `create_generation`/`activate_generation` (faithful port of
   install-apks.py: chroot-rooted links, seed_etc with edit preservation +
   `.apknew` conflicts, current-alias activation, prune, rollback).
   Verified: `kodos-exec selftest` (create/switch/prune/rollback/edit-keep)
   + live scratch-root E2E with the REAL pinned `broot-1.54.0-r0` from
   v3.24 (download, unpack, generation, activation, idempotent re-run).
   **All 23 IR ops now implemented** incl. partition/format/mount, users,
   services, locale/kbd/tz/modules, bootloader, and the full dependency
   closure via apk's own solver (90 requested → 864 installed).

   **Canonical scratch E2E recipe** (Task 9+): pre-mount stubs so the 3
   `mount` actions in the `--inplace` stream all hit the skip-if-mounted
   guard (the `/` skip prints `config names … but live device is …` and
   takes the live device, here `none` — the tmpfs source field); run ends
   at the expected `bootloader: UUID of none: …` ERROR — the root-UUID lookup now runs
   BEFORE the ESP grub trees are wiped (fail-closed, no half-boot):
   ```sh
   rm -rf scratch/root && mkdir -p scratch/root
   unshare -Urm sh -c "
     mount -t tmpfs none scratch/root
     mkdir -p scratch/root/boot scratch/root/home
     mount -t tmpfs none scratch/root/boot
     mount --bind scratch/root/home scratch/root/home
     PATH=\$PWD/scratch/fakabin3:/usr/bin:/bin ./executor/kodos exec \
       -actions scratch/actions.json -metadata vm/generation-0.json \
       -secrets vm/secrets.json -root \$PWD/scratch/root -apk vm/apk-tools-static
   "
   ```
   qcow2 VM E2E (`vm/run.sh`, paste-run): the flow runs cleanly through
   repos → partition → formats → mounts → 864-pkg store → config rendering
   → create/activate generation → post-installs; the create_user/services/
   bootloader leg was last blocked by chroot-rooted etc symlinks dangling
   against the host (fixed by chrootResolve, `9f9bb96`); boot-to-login not
   yet demonstrated.
   Live /etc entries are **hardlinks into `gen-N/etc_store`** (shadow 4.18
   refuses symlinked /etc/passwd|group|shadow); package etc *symlinks* are
   recreated verbatim; every host-side etc writer resolves the chroot-rooted
   chain first (`chrootResolve`) — the host is treated as an **unknown
   distro** everywhere (rule; `vm/run.sh` ab8561a: free-nbd-index scan means
   a wedged /dev/nbdX no longer needs a reboot).
6. ~~Workspace is **still not a git repo**~~ **DONE 2026-11-12**: baseline
   commit `fcbfe3a` (43 files; reference projects stay symlinks, secrets
   gitignored).

## Schema gaps found during the work (backlog, not blockers)

- ~~**Networking**~~ **DONE 2026-11-12**: `services.<nm>.connections` dict
  (wifi/ethernet, wifi requires psk secret ref, literal psk = schema error),
  `configure_network` IR op (#17). Static-IP connections still TODO.
- **Mounts** (`configs.mount` pattern from kodos testvm) — fstab today is
  implied by `storage.devices` only.
- **Desktop/display-manager**: dropped from the neutral dialect (only
  package lists); if eszkoz boots to GNOME this must come back.
- **Kernel modules/initrd**: `boot.kernel.modules` exists but nothing
  says which mechanism loads them (/etc/modules + mkinitfs on Alpine).
- Secrets: refs + `configure_network` plumbing **done**; the resolver side
  (secrets file format for the executor, `hashed_password` consumers)
  lands with the executor.
- Service ordering (`after`/`wanted_by`) unmodeled — first service that
  needs a dependency will force the OpenRC-header mapping.
- OpenRC user services (syncthing) — backend marks mechanism TODO.
- Config sandboxing/lint — deliberately NOT built (trust model documented in
  example/eszkoz/README.md); revisit only if configs ever come from
  untrusted sources. If ever: a cheap `os.`/`io.` identifier lint beats a
  leaky `_ENV` sandbox.

## Known gaps / TODOs (found during VM E2E, 2026-11-13)

- **Silent boot / no login prompt** — FIXED (2026-11-14): Alpine's
  baselayout inittab ships the `ttyS0` getty line COMMENTED OUT, so with
  `console=ttyS0` the box booted fully (all services `[ ok ]`) but nothing
  read the serial line. New config knob `system.serial_console = true`
  (vm-layer sets it); backend-owned full managed `/etc/inittab` emitted
  as a `write_file` action; backends without `M.inittab` hard-error.
- **setuid bits lost in unpack** — FIXED (2026-11-14): guest `sudo`/`su`
  died ("must be suid"). Two compounding bugs: the tar loop wrote files
  with `Mode().Perm()` (truncates setuid) AND the kernel strips
  setuid/setgid on *writes* (file_remove_privs) — creation mode alone is
  not enough. Fix: full mode at create + `chmod` after the content copy.
  Selftest covers it (in-memory tar.gz with 04755 file + hardlink).
  WARNING: stores unpacked BEFORE this fix keep scarrred modes (fetch
  skips existing dirs) — use `vm/run.sh fresh`, do not heal by hand.
- **No network in guest (ssh dead)** — FIXED (2026-11-14, four layers;
  verified: eth0 connects via our `wired` profile and host
  `ssh -p 2222 abuss@127.0.0.1` works):
  (1) linux-virt builds virtio_net as `=m` and nothing coldplugged it
  (Alpine enables its device manager via the *installer*, not packages).
  (2) coldplug alone was NOT enough: nmcli showed EVERY device
  `unmanaged`, reason 71 = NM_DEVICE_STATE_REASON_UNMANAGED_LINK_NOT_INIT
  — NM >=1.48 refuses to manage links udev never tagged, and busybox
  mdev never tags. Neutral service renamed `mdev` -> `udev`; alpine maps
  it to eudev (+libs/hwids/openrc + udev-init-scripts-openrc, which
  ships /etc/init.d/udev) at runlevel **sysinit** (must precede NM at
  boot; "udev" sorts after "networkmanager"), plus `udev-trigger`
  (coldplug is its own service: boot uevents are long gone). arch/deb:
  no-op. NOT the ifupdown plugin (checked its strings; red herring).
  (3) our keyfile profiles silently rejected: hand-built connection UUID
  had a 2-hex-char 4th segment (%x of one byte) -> keyfile plugin
  refuses the whole file -> NM fell back to its transient profile.
  connUUID() + regex selftest (4676aef).
  (4) THE BIG ONE, host-side: run.sh booted
  `-netdev user,id=n0 -device virtio-net-pci` — a NIC without netdev=
  lands on qemu's default hub literally named **"net0"**, our id was
  "n0": BOTH sides "have no peer" and the guest NIC talked to NOTHING
  (zero DHCPOFFERs from any client, no ICMP to 10.0.2.2; busybox udhcpc
  ruled out NM). Proven with a disposable `-kernel vmlinuz-virt
  -initrd initramfs-virt ... break` boot (no disk, no lock, no sudo):
  without netdev=n0 silent, with it 10.0.2.2 replies to ping instantly.
  qemu warns about this on stderr — the warnings were buried. Lesson:
  read qemu launch warnings!
- **bluetooth `[crashed]` in VM** — vanilla behavior, not our bug:
  linux-virt ships `# CONFIG_BT is not set`, so bluetoothd can never open
  the management socket. vm-layer now sets `services.bluetooth.enable =
  false` (desktop config keeps it for real hardware; bluez drops from
  the VM closure). Existing guest disks keep the stale
  `runlevels/default/bluetooth` symlink until reconciled: one-time
  `rc-update del bluetooth default` in-guest, or let it crash harmlessly
  until the next fresh. (3rd bite of the runlevel-litter gap -> do the
  declarative-runlevel reconciliation next.)
- **Runlevel litter** — FIXED (2026-11-14): the executor now makes
  OpenRC runlevels declarative. It records every (runlevel,unit) its
  enable_service actions declare, snapshots live runlevels around the
  package-hook sweep to also protect hook-enabled units (dbus etc. —
  hooks re-run at every create_generation (every apply), so protection re-arms every round),
  and after a fully successful stream removes any runlevel symlink in
  neither set ("runlevel reconcile: removed stale <rl>/<unit>").
  Partial (failed) streams never sweep. Boundary: runlevels are
  kodos-owned declarative state — an admin's manual rc-update add is
  removed by the next apply; declare it in config instead. Proven in
  scratch: flipping bluetooth off removes only default/bluetooth;
  rerun is a no-op. Selftest covers keep-declared/keep-hooked/
  sweep-stale/no-service-stream-hands-off.
- **check-packages.lua fails pre-existing** (assert at line 19, unrelated
  to config changes; investigate when package-set work resumes).

- **Self-management (`kodos apply` on the machine itself)** — the primary
  interaction model once installed (there is no host on bare metal). Design
  approved + spec written: `docs/superpowers/specs/2026-11-14-kodos-self-
  management-design.md` (single Go binary embedding the Lua toolchain via
  gopher-lua + go:embed; apply/status/rollback verbs; live lock via
  net/http + system apk; in-place generate mode; `copy_tree` IR op ships a
  file:// local repo carrying kodos.apk; generations_keep cap). Implemented
  via `docs/superpowers/plans/2026-11-14-kodos-self-management.md` (13 tasks;
  SDD ledger `.superpowers/sdd/2026-11-14-kodos-self-management/`).
  **DONE — VM-PROVEN 2026-11-16 at commit `ee192d0` (paste rounds 1-13):** the
  single binary embeds the Lua toolchain and runs `apply`/`status`/`rollback`;
  `kodos.apk` ships in the local file:// repo (`build/mkapk.sh`); the machine's
  own `/etc/kodos/config.lua` + 0600 secrets are written by the bootstrap
  stream. On the booted guest: `kodos apply` live-added `dummy` (new gen
  current, no storage ops), `kodos rollback` moved boot default + current back
  and the reboot LANDED on it (`Booting 'Kodos generation 2'` + full OpenRC
  boot), and the self-update round upgraded the machine's own manager
  (`kodos 0.1.0-r119 → 0.2.0-r121`, old binary intact, new one self-tests
  green in-guest). The paste runbook in "Self-management on the VM (Task 13)"
  below is the reproduction. Notable field facts it proved are ledged in
  `.superpowers/sdd/2026-11-14-kodos-self-management/progress.md`.
  ON-MACHINE FACT (paste round 1, 2026-11-14): Alpine's base `blkid` is a
  busybox applet that silently IGNORES `-o value -s UUID` (exit 0: empty on
  a bogus device, full `/dev/x: UUID=... TYPE=...` line on a real one) —
  on-machine kodos needs util-linux's real blkid: the kodos apk declares
  `depend = util-linux`, and install_bootloader fails closed on an empty
  UUID before any ESP wipe.
  GUIDANCE (Task-13 review): the config `files` section must NOT carry real
  secrets — write_file content rides the (host-side 0644) generated actions
  stream; secrets belong in the out-of-band 0600 secrets.json only.

- **Initramfs store debris** — FIXED (2026-11-14, 2nd attempt): mkinitfs
  chrooted into the gen tree copies our etc symlinks (etc/modprobe.d/
  *.conf -> /kod/generation/N/etc_store/modprobe.d/...) AND their store
  targets (8 records). The 1st fix stripped the "duplicate" store
  records only -> links dangled in the guest initramfs (libkmod
  "Cannot stat directory entry" x3 per modprobe run). Correct fix:
  cleanInitramfs/stripCpio (Go stdlib) now DEREFS such symlinks — the
  rebuilt record is a regular file carrying the store record's content
  (only mode+filesize header fields rewritten) — then drops the store
  records; a symlink to a missing store file fails the build loudly.
  The parse also requires a top-level init record (hollow-image guard
  from the NEXT.md incident) BEFORE the ESP copy. Parser quirks: names
  "./"-prefixed (cpio -t hides it); busybox (mkinitfs) cpio aligns
  fields to 4 bytes, GNU newc to 2 — the walker tries both candidate
  record ends and trusts the next magic. Kept records copied
  byte-for-byte. Scratch proof: 330 -> 322 entries, etc/modprobe.d
  REG with real content, 0 symlink entries (cpio -tv), no kod,
  usr/sbin/modprobe -> ../bin/kmod untouched. Selftest: deref to REG,
  dangling-target abort, idempotent re-parse, hollow-image, truncated
  input. NOTE: a guest that booted the 1st-attempt image carries
  those dangling links in RAM only; next install round rewrites the
  ESP image clean.

- **Closure locking** — FIXED (2026-11-14): lockfile.lua now locks the
  FULL apk closure (VM profile: 97 requests -> 867 pinned). It serves
  the given index snapshots as `file://` repos (layout apk really
  fetches: `<repo>/<arch>/APKINDEX.tar.gz`, member named APKINDEX) to
  the repo's static apk-tools (`KODOS_APK=/path`, default
  script-relative `../../vm/apk-tools-static`), runs `add --simulate`
  on the pinned requests, and pins every resolved dep by index
  checksum (name|version missing from the snapshots = lock error).
  Executor side: the "N deps unpinned" float is gone — the live resolve
  now ENFORCES the lock: a pinned dep resolving to another version is
  a hard "lock drift" abort (proven with doctored metadata), and a dep
  the live repos gained post-lock warns loudly (repo moved past the
  snapshot: re-snapshot via the curl recipe and relock). test-lock
  asserts closure >> requests, transitive pins (libcrypto3, dbus-libs),
  determinism, and strictness.
- **Rollback vs live /etc**: booting an older generation does not flip live
  /etc until the next activation re-hardlinks it. Real kodos gets this free
  via whole-root btrfs subvol flips; our fs-agnostic etc model needs an
  explicit store→live re-link at boot (or generation-pinned fstab).
- **Guest fstab** — FIXED (2026-11-14): generate.lua emits `{UUID:<device>}`
  placeholders in fstab; the executor's `write_file` expands them via blkid
  (format/mount ops ran first, devices attached). Guests mount by stable
  UUID; re-runs are byte-stable (format never re-mkfs'd), which also kills
  the repeated fstab etc-conflict litter. Also fixed same round: package
  `.pre-install` hooks now run at activation (system users like dbus's
  `messagebus` are created ONLY there — nothing declares them in
  metadata), and `e2fsprogs` joined Alpine base_packages so OpenRC's fstab
  fsck pass finds `fsck.ext4`.
- **Package build wiring** (real kodos `build.rs` → generation-apks
  equivalent): eszkoz binaries ship as prebuilt apks from a local repo dir;
  not yet wired into repos/install.
- Static-IP connections, display-manager section, mounts config, arch/deb
  lock parsers: unchanged backlog below.

## Self-management on the VM (Task 13) — DONE, VM-PROVEN 2026-11-16

The round that proves an installed machine manages itself. Every piece of it is
in place and proven host-side (scratch root); the install/boot/ssh lines below
are the paste round, because they need nbd + qemu + a booted guest.

What the bootstrap now ships into the image (host proof: regenerating
`vm/actions.json` with `lua generate.lua system.lua "$PWD/../../vm/vm-layer.lua"
../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua`):

- `/etc/kodos/config.lua` — the machine's OWN config. `kodos apply` reads its
  config from DISK and the kodos binary embeds only the toolchain, so this file
  must be on the guest. Content is read at generate time from
  `vm/guest-config/config.lua` via a new config `files` section
  (`name -> {path, content, mode?}` → two `write_file` ops, no new IR op). It
  mirrors the whole layer stack (system + vm + machine + user) because those
  layer FILES exist only on the build host, and it is the one file the guest is
  meant to edit: it declares `dummy`, which bootstrap does NOT install, so the
  first in-guest apply is a live package ADD.
- `/etc/kodos/secrets.json`, mode 0600 — apply's secrets sidecar, read from the
  config's own directory (the default `/etc/kodos/secrets.json`). Test creds,
  already public in this repo.
- `repositories.kodos.src = "/kod/repo"` (absolute, i.e. the machine's own
  copy of the repo): `copy_tree` self-skips (src == the chroot-mapped dst) and
  apk/fetchUnpack read the repo off the local disk. Bootstrap keeps
  `src = "repo"` — host-path provenance — and its lock needs no config drift
  (893 pins incl. the util-linux closure, `kodos` 0.1.0-r<commit-count>, no `dummy`).
  PASTE ROUND 2 RULING (2026-11-14): the repo lives at **`/kod/repo`**, root
  level next to `/kod/store` — `/var` is per-generation (flips at activation,
  pruning-unsafe), so a repo under `/var/kodos` would vanish at the next
  activation and the self-copy guard would never re-ship it.
- Guest disk identity: `qemu -drive ...,if=virtio` ⇒ the guest's disk is
  **/dev/vda** (bootstrap's /dev/nbdX is the host's view; fstab mounts by UUID).
- `services.ssh.permit_root_login = true` in vm-layer AND the guest config: the
  paste rounds drive this throwaway box as root over ssh. A real machine keeps
  the base profile's `false`.

Live-add proof on the scratch root (`scratch/apply-config-guest.lua` = the
guest's own config with only the repo `src` and the disk name host-adapted,
since the host has no /dev/vda): bootstrap gen0 = 868 packages, no `dummy` and
no `store/dummy`; `kodos apply` → gen1 = 869 packages, `dummy` 1.0-r0 pinned,
`store/dummy/1.0-r0` fetched from the local repo, `current` → gen1.

### The paste round — one paste line per step

```sh
# 0. terminal A: build the repo artifacts and install into the qcow2
sudo vm/run.sh install                    # runs build/mkrepo.sh + build/mkapk.sh itself
# 1. terminal B: boot (quit with Ctrl-a x; the next lines run from terminal A)
vm/run.sh boot
# 2. kodos on the machine it runs on: selftest (suitetest SKIPs by design, see the file)
vm/run.sh guest "$(cat vm/guest-test/00-kodos.sh)"
# 3. the machine reconfigures itself: dry-run, then real (minutes), then status
#    expect: a new generation is current, dummy pinned inside it, no storage ops
vm/run.sh guest "$(cat vm/guest-test/10-apply.sh)"
# 4. land on the new generation
vm/run.sh guest 'reboot'                  # qemu exits with the guest
vm/run.sh boot
# 5. after reboot: boot default names the NEW generation, and no libkmod noise
vm/run.sh guest 'kodos status; dmesg | grep -i libkmod || echo "no libkmod errors"'
# 6. roll back, reboot, confirm it stuck (grub title + status name the OLD generation)
vm/run.sh guest "$(cat vm/guest-test/20-rollback.sh)"
vm/run.sh guest 'reboot'
vm/run.sh boot
vm/run.sh guest 'kodos status'
# 7. self-upgrade: newer kodos.apk into the guest's repo, then apply on the guest
bash vm/guest-test/selfup-build.sh         # PKGVER=0.2.0 build + copy into the guest
vm/run.sh guest "$(cat vm/guest-test/30-selfup.sh)"
```

**Rebuilds and the stale-store trap**: `build/mkapk.sh` versions the kodos apk
as `0.1.0-r<git-commit-count>` (monotonically increasing per commit). After any
executor change, commit it, then re-run `vm/run.sh install` — the new rN pkgrel
produces a new store key and the guest's `install_packages` picks up fresh
bytes. The symptom of a stale store is a guest apply log whose shape contradicts
HEAD behaviour (e.g. hook lines appearing after `activate_generation`, or
`checkGenRunnable` absent from the binary). Grep `usr/bin/kodos` from the guest
repo apk for a known error string to confirm staleness (`strings <apk-binary> |
grep "generation.*incomplete"`; 0 matches = pre-bef50c0 binary).

**Rollback runnable gate**: `kodos rollback -to N` refuses (errors, current
alias untouched) if generation N is missing `sbin/init` or `bin/sh` — stranded
debris generations from aborted bootstraps can no longer strand the machine.
Plain `kodos rollback` skips non-runnable generations silently and picks the
highest runnable one below current.

**Guest apk-tools and the index-writer mismatch**: Alpine 3.24 ships apk-tools
3.0.9. It reads v2-format repos (resolution and fetch always worked) but its
`apk index` writer refuses v2-no-datahash packages ("file format is invalid or
inconsistent"). The selfup round therefore ships a host-built APKINDEX.tar.gz
(from vm/apk-tools-static 2.14) alongside the new apk via `selfup-build.sh`;
the guest 30-selfup.sh asserts the shipped index names the new version instead
of rebuilding it. v3-native package format (.DATAHASH, abuild) is a deferred
concern. ponytail: global host-built index; per-package datahash when v3
packaging is adopted.

`vm/run.sh guest '<cmd>'` / `vm/run.sh guest-file F [DIR]` are the ssh runners
(host 2222 → guest 22, root + the test password). This host HAS `sshpass`, so
the rounds run non-interactively; on a host without it the same lines work but
ssh prompts for `kodos-root-test` (the runner says so). Findings from this round
become fix-forward commits; the round is the definition of done for
self-management.

## Bare-metal install USB (build/usb/)

Full create/boot/install walkthrough: **`build/usb/README.md`** (it also
ships on the payload partition). The gist:

The stick = Alpine standard ISO (3.24.4, isohybrid: the stick itself boots
BIOS *and* UEFI via El Torito) + one ext4 partition labelled KODOS holding the
payload: static `kodos`, local repo (`kodos.apk` + APKINDEX via
`build/mkapk.sh`), 3.24 main+community index snapshots, the bare-metal config
layer, and `run-install.sh`. Packages resolve from the Alpine mirror on the
target (network available — the design decision); offline mirror deferred.

FIRST STICK ROUND (2026-11-16, user's host) caught a partitioner trap: the
Alpine 3.24.4 ISO's table is xorriso hybrid-DOS (entry id 0 spans the whole
ISO + a tiny ESP entry), and `sgdisk` refuses to touch it
(`Invalid partition data!` rc=2). The script now uses `parted`, replacing the
hybrid ISO entry with the payload partition — both stick boot paths survive
the surgery (qemu-proven: UEFI GRUB *and* SeaBIOS isolinux reach live login
after it; the xorriso MBR bootstrap reads the El Torito marker at LBA0, and
UEFI only needs the ESP entry, which parted leaves intact).

Host side (needs go, curl, parted, mkfs.ext4; the ISO + index snapshots cache
under `build/usb/iso/`):

```
sh build/usb/mkinstall-usb.sh /dev/sdX      # typed device-name confirm, stick ERASED
STAGE_ONLY=1 sh build/usb/mkinstall-usb.sh  # payload staging only (layout proof)
```

USB side (Alpine live: root login, Ethernet up):

```
udhcpc -i <if>            # or setup-alpine; network IS required
mount -L KODOS /usb
vi /usb/config/machine.lua    # host_name + the /dev/... disk line (lsblk)
sh /usb/run-install.sh --dry-run
sh /usb/run-install.sh
```

`run-install.sh` is the VM-proven install sequence minus the nbd
indirection: `kodos lua generate.lua` -> `kodos lua lockfile.lua`
(`KODOS_APK` = the live stick's own apk, absolute, like apply resolves it) ->
`kodos exec -root /mnt/kodos-target`. It refuses a target disk that *is* the
payload stick (findmnt + lsblk PKNAME), requires typed `yes` before touching
any disk, seds the `copy_tree` src to wherever the stick IS mounted (the
proven nbd-sed idea) and `--device` seds the whole stream when it differs
from the config's device (vm/run.sh's nbd-sed, generalized), prompts a root
password into a 0600 secrets file and lands it at the machine's
`/etc/kodos/secrets.json` next to the shipped config.lua. Afterwards
self-management applies exactly as above: `/kod/repo` is on the machine's
own disk.

**ISO variant (approved 2026-11-16):** `build/usb/mkinstall-iso.sh`
xorriso-re-masters the Alpine standard ISO (boot info verbatim from
`-report_el_torito as_mkisofs`) with the payload grafted read-only at
`/kodos` — one file for VMs/optical/ISO-only writers; live auto-mounts its
ISO at `/media/cdrom`, so day-of use is `cp -a /media/cdrom/kodos /root/
kodos`, edit, `run-install.sh --device <disk>`.

**LIVE-ENV FACTS this proved (qemu, no sudo):** the Alpine standard live ISO
is BUSYBOX-flavored — no parted, no mkfs.ext4, and busybox blkid/mount lack
the util-linux interfaces the executor uses. Fixes landed: run-install.sh
auto-installs parted + util-linux + e2fsprogs at startup (parted/util-linux
ride in the payload repo pinned to the snapshot index = offline; e2fsprogs
from the mirror), and `executor/ops.go` mount now best-effort `modprobe`s
the fs TYPE first (live kernels do not autoload ext4 at mount time —
busybox mount then fails EINVAL). Payload repo also now stages EXACTLY one
kodos apk (the HEAD-built `r<commit-count>`; vm/repo accumulates old
versions). Full qemu E2E chain at this commit: ISO live -> 37-op install on
virtio disk (parted/formats/swap/mounts/store/grub all green) -> UEFI
reboot -> `Booting 'Kodos generation 0'` -> OpenRC on linux-lts -> login ->
`kodos status` current->0. Headless VM test note: add `serial_console =
true` + `"console=ttyS0"` to the config for serial-visible boots (real
boxes with a monitor never need it).

**Proven:** `STAGE_ONLY` layout + full host dry-run through the embedded
toolchain (rc 0): 98-pkg closure incl. `linux-lts` + `linux-firmware` (the
backend DEFAULT variant is the right one for real hardware), canonical IR
order ending `install_bootloader`, copy_tree src rewrite visible, no `dummy`,
kodos apk pinned — plus the qemu E2E chain above for the ISO path. **REAL
BARE-METAL INSTALL DONE 2026-11-16** (user's box, install-ISO burned to
USB): the field hiccup was a stale SWAP signature on the root-designated
partition surfacing as a mount failure — root-caused + fixed at `ad2a9ac`
(format self-heals swap signatures, protects real filesystems; create_swap
refuses to erase data), cleared live with one `wipefs -a`, install then
completed. Known
limits (as designed): the INSTALLED system's
bootloader is grub EFI only (the stick itself also boots legacy BIOS, but an
install on a legacy-BIOS-only box would not boot); partition sizes are the
base profile's placeholders (ponytail: confirm with lsblk on the stick; the
fixed parts total ~99GB so the disk must be >= ~100GB);
the machine config inherits the eszkoz base desktop profile (tailscale etc —
edit the layer to trim).

**NanoVM gen-2 boot failure -> seedEtc root cause (2026-11-16, second field
round):** the box installed and gen0/gen1 booted, but a config tweak's gen2
died at grub `start_image() 0x8000000000000001` (LOAD_ERROR): NanoVM's
minimal UEFI loads a 108MB initrd but refuses 180MB. `cpio -tv` of the image:
`/lib/firmware` fat — Alpine's default mkinitfs features include `kms`
(features.d/kms.modules = the whole `kernel/drivers/gpu` tree) and
mkinitfs's initfs_firmware attaches every collected .ko's `firmware:` blobs
(amdgpu/i915 ≈ 10MB each; the gen2 package set pushed it over). Kodos needs
only disk/net drivers before the root mount — the GPU comes up under openrc.
Config fix: the machine layer now ships a trimmed
`/etc/mkinitfs/mkinitfs.conf` (stock features minus `kms`) in the `files`
section -> 22MB initramfs (measured in-VM). The config fix then EXPOSED the
real executor bug: fresh-ISO E2E still built 180MB despite the write_file
op landing in live /etc — `seedEtc` recognized "local edits" ONLY from a
previous generation's etc_store, so at bootstrap (no prev) every
package-shipped conffile was seeded verbatim, silently dropping the stream's
write_file content from the gen tree that install_bootloader's chrooted
mkinitfs reads; a plain `kodos apply` on the installed box lost the edit
the same way (gen1 measured 180MB). Root fix in `seedEtc`: a shipped
conffile whose LIVE /etc content differs from its pristine default IS the
local edit (live wins) — covers bootstrap AND apply; unit test
`TestSeedEtcBootstrapKeepsStreamEdits` (red first). Field remediation needs
the FIXED executor (the box's installed kodos predates it): boot media with
the rebuilt payload, copy it over, refresh `/etc/kodos/config.lua` from the
payload's target-config.lua, run the payload's `bin/kodos apply`, verify the
new gen's initramfs size, reboot (`kodos rollback` stays the escape hatch).
**Still open on the bare box:** the day-2 checks
(apply/rollback/self-update) have only ever been driven on the VM; the
stick variant (writable KODOS ext4 partition) has not had its own physical
round.

## Offline VM forensics (no sudo; host needs qemu-img, mtools, e2fsprogs)

```
qemu-img convert -U -O raw vm/vm-disk.qcow2 /tmp/opencode/vm-raw.img
dd if=vm-raw.img of=esp.img bs=512 skip=2048 count=1046528   # p1 ESP
mdir -i esp.img ::/ ; mcopy -i esp.img ::/initramfs-virt out  # ESP via mtools
dd if=vm-raw.img of=root.img bs=4M iflag=skip_bytes,count_bytes \
   skip=$((1050624*512)) count=$((134215680*512)) conv=sparse  # p2 root
debugfs -R "ls -l <path>" root.img                            # ext4 read-only
```

Incident note (2026-11-13): a wedged-nbd crashed install truncated live
/etc files (0 bytes); the etc conflict-preservation logic then fossilized
them into every later etc_store, so mkinitfs silently built hollow 79-entry
initramfs images forever. Fix: clean disk (`vm/run.sh fresh`) — do not heal
scarred disks by hand. Hardening idea: guard install_bootloader on
initramfs *contents* (must contain /init provider), not just size.
(DONE: busybox-content grep + the cpio walk in cleanInitramfs requires a
top-level init entry before the ESP copy.)

## Ranked next steps

| # | Step | Why this order |
|---|---|---|
| 0 | ~~`git init` + commit example, docs, backends~~ | **DONE 2026-11-12** (baseline `fcbfe3a`) |
| 1 | ~~Config layering + merge~~ | **DONE 2026-11-11** |
| 2 | ~~Action/IR contract + reference Lua generator~~ | **DONE 2026-11-11** |
| 3 | ~~Port schema/validator~~ | **DONE 2026-11-12** (`validate.lua`, `test-validate.lua`; wired into generator + checker) |
| 4 | ~~Version locking + generation metadata~~ | **DONE 2026-11-12** (`lockfile.lua`, `test-lock.lua`, IR.md contract) |
| 5 | ~~Executor design + spike~~ | **SPIKE DONE 2026-11-12** (Go; store/generation core + install + network live-tested on a scratch root; remaining ops listed in item 5 above) |
| 6 | Schema-gap backlog above (networking **done** 2026-11-12; display manager next) | fill gaps as real needs, not upfront |
| 7 | Drift CI: re-fetch the 3 indexes + re-run all backends per Alpine release / weekly | `clang19→clang20` proved the failure mode is real |
| 8 | Bare-metal install media (`build/usb/`) | **install DONE on real hardware 2026-11-16** (ISO path; field trap fixed at `ad2a9ac`); qemu E2E green; day-2 apply/rollback/self-update on the bare box not yet driven |

Suggested entry point: the **bare-metal paste round** (build/usb/ section
above) — VM install-to-login and self-management are DONE (2026-11-16), so
the unproven surface is now physical hardware: mkinstall-usb.sh on a real
stick (parted partitioning after the ISO dd — sgdisk refuses the hybrid-DOS
table: `Invalid partition data!`, field trap 2026-11-16), run-install.sh on a real disk,
then apply/rollback/self-update on the bare box. Remaining non-executor
work: the gaps above + step 6/7.
