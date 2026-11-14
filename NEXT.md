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
   Lua↔executor contract (22 ops, strict order, idempotent); `generate.lua`
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
   **All 22 IR ops now implemented** incl. partition/format/mount, users,
   services, locale/kbd/tz/modules, bootloader, and the full dependency
   closure via apk's own solver (90 requested → 864 installed).
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
  `configure_network` IR op (#16). Static-IP connections still TODO.
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
- **Runlevel litter**: `enable_service` never removes stale entries from
  earlier rounds (guest boot still had `mdev@boot` after the backend
  switched to udev). Declarative fix: reconcile runlevel dirs against
  the enable-set (or manage runlevels as etc like everything else).
  Until then: fresh disk when service sets flip.
- **check-packages.lua fails pre-existing** (assert at line 19, unrelated
  to config changes; investigate when package-set work resumes).

- **Closure locking**: locked config names are pinned exact, but 774 of 864
  packages arrive as unlocked deps that float (loud WARNING at install).
  Proper fix: persist the full resolved closure in `lockfile.lua`.
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

Suggested entry point: **boot the VM to a login prompt** — the install
flow reaches the last ops; one clean `vm/run.sh` paste round should get
through create_user → services → bootloader. On boot verify: `uname -r`,
`ls /kod/generation`, `rc-status`, NM keyfiles, serial login
`abuss`/`kodos-user-test`. Remaining non-executor work: the gaps above +
step 6/7.
