# Arch Install Media Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build USB/ISO install media that installs a bootable Arch Linux generation through the Arch backend, without changing the Alpine media flow.

**Architecture:** Add sibling Arch staging/ISO/installer scripts beside the existing Alpine ones and one env override in the shared ISO remaster. The payload carries host-pacman-produced ALPM desc snapshots and a standalone Arch config; the live Alpine host runs the static executor, which locks with `alpm.lua` and installs Arch tarballs directly.

**Tech Stack:** POSIX `sh`, Go static binary, Lua toolchain (`generate.lua`, `lockfile.lua`, `alpm.lua`), host `pacman` as build-time trust oracle, `xorriso` for ISO remaster, `apk-tools-static` only for live-host tooling.

**Spec:** `docs/superpowers/specs/2026-11-18-arch-install-media-design.md`

## Global Constraints

- Live base stays Alpine 3.24 standard ISO; no archiso.
- Installed target = milestone-1 parity: GRUB EFI, systemd, NetworkManager, ssh, users; no desktop.
- kodos is delivered as `/usr/bin/kodos` copied by the bootstrap; no local Arch repo, no `self-update`.
- Pacman runs only on the build host with isolated `DBPath`/`CacheDir`; never on the live host or install path.
- Alpine scripts, configs, and installer stay unchanged; the only shared change is `STAGE_SCRIPT` with the existing default.
- Worktree test assets already copied from main checkout: `vm/apk-tools-static` and `vm/repo/x86_64/*.apk`.
- Full gates after each task: `go test ./...`, `go build -o kodos . && ./kodos suitetest`, `./kodos selftest` from `executor/`. Use the built binary, not `go run .`, for suite/selftest.
- Shell scripts must pass `sh -n`.
- Known ceiling to mark with a `ponytail:` comment: build-time DB snapshots can drift against the rolling mirror; rebuild media or restage desc files on a pacman host.

## Review Focus

- **Missing host pacman:** staging must abort with a clear message; Task 2 test runs with `PACMAN_BIN=/nonexistent` and expects failure naming pacman.
- **Empty or truncated desc snapshot:** staging must abort; Task 2 test truncates a staged `core.desc` and expects failure.
- **Target is the live stick:** installer must refuse; Task 4 test stubs `findmnt`/`lsblk` to report the target as the source and expects refusal.
- **Runtime accidentally uses apk/pacman for the target:** Task 4 dry-run test asserts the IR has no `APKINDEX`, no `"type": "apk"`, and no `file://` repo.
- **Alpine media changed by shared-script edit:** Task 3 test asserts `git diff --exit-code` is clean for `build/usb/mkinstall-usb.sh`, `build/usb/run-install.sh`, and `build/usb/config`.

---

### Task 1: Standalone Arch media config + IR test

**Files:**
- Modify: `executor/lua/test-generate.lua` (insert before its final `print("PASS: ...")` line)
- Create: `build/usb/arch-config/arch-machine.lua`

**Interfaces:**
- Consumes: existing `kodos suitetest` harness and `generate.lua` IR contract.
- Produces: `build/usb/arch-config/arch-machine.lua`, consumed by Tasks 2 and 4 as the config passed to `generate.lua` and `lockfile.lua`.

- [ ] **Step 1: Write the failing test**

Append this section immediately before the final `print("PASS: IR stream ...")` line in `executor/lua/test-generate.lua`:

```lua
-- ====== arch install media: standalone bare-metal config ======
local am = run("../../build/usb/arch-config/arch-machine.lua")
assert(pos(am, '"distro": "arch"'), "media config must select arch backend")
assert(pos(am, '"mirror": "https://mirror.rackspace.com/archlinux"'))
assert(pos(am, '"core"') and pos(am, '"extra"'), "core+extra branches")
assert(pos(am, '"device": "/dev/sda"'), "bare-metal default disk")
assert(pos(am, '"type": "grub"') and pos(am, '"efi": true'), "GRUB EFI")
assert(pos(am, '"path": "/etc/kodos/config.lua"'), "self-management config landed")
assert(pos(am, '"path": "/etc/machine-id"'), "fixed machine-id for read-only /etc")
assert(not am:find('"type": "apk"', 1, true), "no apk repo in Arch media")
assert(not am:find("APKINDEX", 1, true), "no apk index ops in Arch media")
assert(not am:find('"url": "file:///', 1, true), "no local apk repo in Arch media")
assert(not am:find("console=ttyS0", 1, true), "bare-metal kernel args have no serial console")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd executor && ./kodos suitetest`
Expected: FAIL on `test-generate.lua` because the config file does not exist.

- [ ] **Step 3: Implement `build/usb/arch-config/arch-machine.lua`**

Write a standalone Lua config (no `require("system")`) with exactly these top-level keys and values:

- `repos.base = { type = "arch", mirror = "https://mirror.rackspace.com/archlinux", branches = { "core", "extra" } }`
- `boot.bootloader = { type = "grub", efi = true, timeout = 5 }`; `boot.kernel.arguments = { "quiet" }`
- `storage.devices.disk0`: `device = "/dev/sda"`, `type = "gpt"`, `efi = true`, partitions `boot` (512MB, esp, vfat, `/boot`, `check=false`) and `root` (100%, linux, ext4, `/`, `noatime`)
- `generations = { storage = "store", root = "/kod", keep = 5 }`
- `system`: `host_name = "kodos-arch"`, `timezone = "America/Edmonton"`, `keyboard_layout.layout = "us"`, `console_keymap = "us"`, `locale.default = "en_US.UTF-8"`; no `serial_console`
- `services`: `ssh = { enable = true, permit_root_login = true }`, `networkmanager = { enable = true }`
- `users`: `root` (home `/root`, shell `/bin/bash`, `create_home = false`) and `abuss` (group `wheel`, home `/home/abuss`, shell `/bin/bash`, `create_home = true`); no password hashes
- `packages`: `sudo`, `git`, `mkinitcpio`, `glibc-locales` from repo `base`
- `files.machine_id`: path `/etc/machine-id`, mode `"0444"`, fixed 32-hex content ending in newline
- `files.mkinitcpio`: path `/etc/mkinitcpio.conf`; `HOOKS=(base systemd modconf block filesystems)` with **no** `autodetect` (document why in a Lua comment: build-host module detection is wrong for bare metal)
- `files.config`: path `/etc/kodos/config.lua`, content = `sibling("arch-machine.lua")` using the same `debug.getinfo` sibling helper as `build/usb/config/machine.lua`

- [ ] **Step 4: Run test to verify it passes**

Run: `cd executor && ./kodos suitetest`
Expected: PASS including the new Arch-media section.

- [ ] **Step 5: Full gates**

Run: `cd executor && go test ./... && go build -o kodos . && ./kodos suitetest && ./kodos selftest`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add executor/lua/test-generate.lua build/usb/arch-config/arch-machine.lua
git commit -m "feat: add bare-metal Arch media config"
```

---

### Task 2: Arch payload staging with pacman oracle

**Files:**
- Create: `build/usb/mkinstall-arch-usb.sh` (stage-only portion; device/`dd` path comes in Task 3)
- Create: `build/usb/test-arch-media.sh` (staging checks; extended by Tasks 3 and 4)

**Interfaces:**
- Consumes: `build/usb/arch-config/arch-machine.lua` from Task 1, `executor/` Go build, host `pacman`, `build/usb/iso/main-324.apkindex` cache, `vm/apk-tools-static`.
- Produces: `OUT` (default `build/usb/arch-stage`) containing `bin/kodos`, `indexes/arch/core.desc`, `indexes/arch/extra.desc`, `config/arch-machine.lua`, `arch-run-install.sh`, `README-arch.md`, `repo/x86_64/APKINDEX.tar.gz` and pinned live-tool apks. Task 4 consumes this layout.

- [ ] **Step 1: Write the failing test**

Create `build/usb/test-arch-media.sh` with `set -eu`, `REPO=...`, `TDIR=${TMPDIR:-/tmp/opencode}`, `STAGE=$TDIR/arch-stage-test`, and these checks in order. Before real staging, create empty placeholders with `: > "$REPO/build/usb/arch-run-install.sh"` and `: > "$REPO/build/usb/README-arch.md"` so the staging copy has sources; Task 4 replaces their contents.

1. `sh -n "$REPO/build/usb/mkinstall-arch-usb.sh"`
2. `PACMAN_BIN=/nonexistent STAGE_ONLY=1 OUT="$STAGE" sh "$REPO/build/usb/mkinstall-arch-usb.sh"` must exit nonzero; output must contain `pacman`.
3. Real staging: `STAGE_ONLY=1 OUT="$STAGE" sh "$REPO/build/usb/mkinstall-arch-usb.sh"`; assert `bin/kodos`, `indexes/arch/core.desc`, `indexes/arch/extra.desc`, `config/arch-machine.lua`, and `repo/x86_64/APKINDEX.tar.gz` exist and are nonempty. (`arch-run-install.sh` and `README-arch.md` are checked in Task 4, after they exist.)
4. Stub `PACMAN_BIN` with a script that writes empty `sync/core.db` and `sync/extra.db` under the staging script's isolated DBPath; staging must exit nonzero with an error naming the desc snapshot. Restore by rerunning real staging.
5. Print `test-arch-media staging: ok`.

- [ ] **Step 2: Run test to verify it fails**

Run: `sh build/usb/test-arch-media.sh`
Expected: FAIL because `mkinstall-arch-usb.sh` does not exist.

- [ ] **Step 3: Implement staging**

Create `build/usb/mkinstall-arch-usb.sh` with functions `fetch_arch_dbs`, `stage_live_apks`, and `stage_payload`, plus `ARCH_MIRROR=${ARCH_MIRROR:-https://mirror.rackspace.com/archlinux}` and `PACMAN_BIN=${PACMAN_BIN:-pacman}`.

- `fetch_arch_dbs`: require `$PACMAN_BIN`; remove the isolated `DBPath`/`CacheDir` under `/tmp/opencode/arch-media` first (so a stubbed oracle cannot inherit stale DBs); write a `pacman.conf` with those paths, `GPGDir=/etc/pacman.d/gnupg`, `SigLevel = Required DatabaseOptional`, `[core]`/`[extra]` `Server = $ARCH_MIRROR/<repo>/os/x86_64`; run `$PACMAN_BIN -Sy --config ...`; extract `*/desc` from `$DBPath/sync/<repo>.db` to `OUT/indexes/arch/<repo>.desc`; abort if either desc is missing or empty. Markup with `# ponytail: build-time snapshot only; rebuild media or restage desc files when the rolling mirror moves`.
- `stage_live_apks`: reuse the Alpine 3.24 cache/index lookup from `mkinstall-usb.sh` to stage `parted`, `util-linux`, and `e2fsprogs` apks into `OUT/repo/x86_64/`, then build `APKINDEX.tar.gz` with `vm/apk-tools-static`. These are live-host tools only; add a comment saying so.
- `stage_payload`: `go build -C executor -o OUT/bin/kodos .` (or equivalent subshell build), copy config/installer/README, then call the two helpers.
- End with `stage_payload`; if `STAGE_ONLY` is set, print the tree and exit 0. Do not add device/`dd` code yet.

- [ ] **Step 4: Run test to verify it passes**

Run: `sh build/usb/test-arch-media.sh`
Expected: PASS with `test-arch-media staging: ok`.

- [ ] **Step 5: Full gates**

Run the three executor gates from Global Constraints. Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add build/usb/mkinstall-arch-usb.sh build/usb/test-arch-media.sh
git commit -m "feat: stage Arch install media payload"
```

---

### Task 3: ISO builder override and complete USB path

**Files:**
- Modify: `build/usb/mkinstall-iso.sh:31`
- Modify: `build/usb/mkinstall-arch-usb.sh` (device/`dd` path after `stage_payload`)
- Create: `build/usb/mkinstall-arch-iso.sh`
- Modify: `build/usb/test-arch-media.sh` (ISO + Alpine-isolation checks)

**Interfaces:**
- Consumes: staged payload from Task 2; existing `xorriso` remaster in `mkinstall-iso.sh`.
- Produces: `build/usb/iso/kodos-arch-install-<rel>.iso` and a `dd`-able USB path; `mkinstall-arch-iso.sh` sets `STAGE` and `STAGE_SCRIPT` for the shared builder.

- [ ] **Step 1: Write the failing test**

Append to `build/usb/test-arch-media.sh`, before its final ok line:

```sh
sh -n "$REPO/build/usb/mkinstall-arch-iso.sh"
ISO=${ARCH_TEST_ISO:-$TMPDIR/kodos-arch-install-test.iso}
STAGE=$STAGE STAGE_SCRIPT="$REPO/build/usb/mkinstall-arch-usb.sh" \
  OUT="$ISO" sh "$REPO/build/usb/mkinstall-iso.sh" >/dev/null
[ -s "$ISO" ] || { echo "no Arch ISO"; exit 1; }
xorriso -indev "$ISO" -find /kodos -maxdepth 1 2>/dev/null | grep -q run-install.sh \
  || { echo "ISO missing /kodos payload"; exit 1; }
git -C "$REPO" diff --exit-code -- build/usb/mkinstall-usb.sh build/usb/run-install.sh build/usb/config \
  || { echo "Alpine media changed"; exit 1; }
grep -Fq 'STAGE_SCRIPT:-$REPO/build/usb/mkinstall-usb.sh' "$REPO/build/usb/mkinstall-iso.sh" \
  || { echo "ISO builder default override missing"; exit 1; }
```

Replace the final ok line with `echo "test-arch-media iso: ok"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `sh build/usb/test-arch-media.sh`
Expected: FAIL because `mkinstall-arch-iso.sh` does not exist. If the cached Alpine base ISO is absent, copy it from the main checkout or let the builder download it once.

- [ ] **Step 3: Implement the override and ISO/USB paths**

- In `build/usb/mkinstall-iso.sh:31`, replace the staging invocation with exactly:
  `OUT=$STAGE STAGE_ONLY=1 sh "${STAGE_SCRIPT:-$REPO/build/usb/mkinstall-usb.sh}" >/dev/null`
- Create `build/usb/mkinstall-arch-iso.sh`: set `REPO`, `REL=${ALPINE_REL:-3.24.4}`, `STAGE=${STAGE:-$REPO/build/usb/arch-stage}`, `OUT=${1:-$REPO/build/usb/iso/kodos-arch-install-$REL.iso}`, then `exec sh "$REPO/build/usb/mkinstall-iso.sh"` with those values exported.
- Extend `build/usb/mkinstall-arch-usb.sh`: after `stage_payload`, if `STAGE_ONLY` is set exit 0; otherwise require a block device argument, check it is unmounted, print the erase warning naming Arch, require typed device-name confirmation, call `mkinstall-arch-iso.sh`, and `dd` the resulting ISO to the device with `bs=4M status=progress oflag=sync`. Mirror the Alpine script's wording and safety checks.

- [ ] **Step 4: Run test to verify it passes**

Run: `sh build/usb/test-arch-media.sh`
Expected: PASS with `test-arch-media iso: ok`.

- [ ] **Step 5: Full gates**

Run the three executor gates. Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add build/usb/mkinstall-iso.sh build/usb/mkinstall-arch-usb.sh build/usb/mkinstall-arch-iso.sh build/usb/test-arch-media.sh
git commit -m "feat: build Arch install ISO and USB"
```

---

### Task 4: Live installer and usage docs

**Files:**
- Replace placeholder from Task 2: `build/usb/arch-run-install.sh`
- Replace placeholder from Task 2: `build/usb/README-arch.md`
- Modify: `build/usb/test-arch-media.sh` (dry-run + live-stick guard checks)

**Interfaces:**
- Consumes: Task 2 payload at `$PAYLOAD` (`bin/kodos`, `indexes/arch/*.desc`, `config/arch-machine.lua`).
- Produces: installed target root `/mnt/kodos-target` with Arch generation 0, `/etc/kodos/config.lua`, `/etc/kodos/secrets.json`, and `/usr/bin/kodos`.

- [ ] **Step 1: Write the failing tests**

Append to `build/usb/test-arch-media.sh`, before its final ok line:

```sh
DRY=$("$PAYLOAD/run-install.sh" --dry-run --device /dev/nbd99 2>&1)
printf '%s\n' "$DRY" | grep -q 'DRY-RUN configure_repos' \
  || { echo "dry-run has no repo op"; exit 1; }
printf '%s\n' "$DRY" | grep -q 'https://mirror.rackspace.com/archlinux' \
  || { echo "dry-run lost Arch mirror"; exit 1; }
printf '%s\n' "$DRY" | grep -q 'DRY-RUN install_packages' \
  || { echo "dry-run has no package op"; exit 1; }
if printf '%s\n' "$DRY" | grep -qE 'APKINDEX|"type": "apk"|file:///'; then
  echo "Arch dry-run leaked apk/local-repo ops"; exit 1
fi
mkdir -p "$TDIR/fakebin"
printf '#!/bin/sh\n[ "$1" = -n -o "$1" = --mountpoint ] && echo /dev/nbd99\nexit 0\n' > "$TDIR/fakebin/findmnt"
printf '#!/bin/sh\necho nbd99\nexit 0\n' > "$TDIR/fakebin/lsblk"
chmod +x "$TDIR/fakebin/findmnt" "$TDIR/fakebin/lsblk"
if PATH="$TDIR/fakebin:$PATH" "$PAYLOAD/run-install.sh" --dry-run --device /dev/nbd99 2>&1 | grep -q 'USB stick'; then
  : # expected refusal
else
  echo "installer did not refuse the live stick"; exit 1
fi
```

Set `PAYLOAD=$STAGE` near the top of the test script. Replace the final ok line with `echo "test-arch-media installer: ok"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `sh build/usb/test-arch-media.sh`
Expected: FAIL because `arch-run-install.sh` is not staged yet.

- [ ] **Step 3: Implement the installer and README**

Create `build/usb/arch-run-install.sh` (`set -eu`, POSIX sh) with:

- `PAYLOAD=$(cd "$(dirname "$0")" && pwd)`, `KODOS=$PAYLOAD/bin/kodos`, `CFG=$PAYLOAD/config/arch-machine.lua`, `WORK=/tmp/kodos-arch-install`, `TARGET_ROOT=${TARGET_ROOT:-/mnt/kodos-target}`.
- Flags `--dry-run`, `--yes`, `--device DEV`.
- Live-tool gate: require `blkid`; if `parted`, `mkfs.ext4`, or `lsblk` is missing, use `apk add --allow-untrusted --repository "$PAYLOAD/repo"` plus the Alpine 3.24 mirror fallback. Comment that apk here equips the live host only.
- Device resolution from `$CFG` via the same `/dev/...` grep as `build/usb/run-install.sh`, `--device` override, and the same `findmnt`/`lsblk` live-stick refusal.
- Erase confirmation unless `--dry-run`/`--yes`.
- `generate.lua` on the absolute config path; apply the same `sed` device rewrite when `DEV != CFGDEV`.
- Lock with `"$KODOS" lua lockfile.lua 0 "$PAYLOAD/indexes/arch/core.desc" "$PAYLOAD/indexes/arch/extra.desc" -- "$CFG"`. Do not set `KODOS_APK`; do not refresh indexes.
- Secrets: write `$WORK/secrets.json` with `users.root.hashed_password` and `users.abuss.hashed_password` from prompted passwords (or use `$PAYLOAD/config/secrets.json` if present), mode 0600.
- `exec -actions ... -metadata ... -root "$TARGET_ROOT" [-secrets] [-dry-run]`.
- On non-dry success, `install -m 0755 "$PAYLOAD/bin/kodos" "$TARGET_ROOT/usr/bin/kodos"` and `install -m 0600 "$SECRETS" "$TARGET_ROOT/etc/kodos/secrets.json"`, then `sync` and unmount the target.

Create `build/usb/README-arch.md` mirroring `build/usb/README.md` sections but for Arch: build stick/ISO, boot Alpine live, `udhcpc`, copy payload, edit disk/hostname, run `--dry-run` then real install, reboot and log in; document Secure Boot off, wired network, and the snapshot-drift caveat.

- [ ] **Step 4: Run tests to verify they pass**

Run: `sh build/usb/test-arch-media.sh`
Expected: PASS with `test-arch-media installer: ok`.

- [ ] **Step 5: Full gates**

Run the three executor gates. Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add build/usb/arch-run-install.sh build/usb/README-arch.md build/usb/test-arch-media.sh
git commit -m "feat: add Arch media live installer"
```
