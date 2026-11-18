# Arch Backend Milestone 1 (Bootable Arch VM) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kodos exec` installs a distro-neutral config into a qcow2 disk as an Arch system that boots with systemd — the Arch mirror of the proven Alpine round.

**Architecture:** One IR op stream stays distro-neutral; distro behavior branches inside the op implementations (branch key: `meta.Distro`). Resolution moves fully into `lockfile.lua` for arch (ALPM DB text snapshots + a Lua resolver); the executor only downloads pinned `.pkg.tar.zst`, sha256-checks, and unpacks into the existing store/generation machinery. pacman never runs on the exec path; the host pacman GPG keyring is the DB trust oracle in `vm/run-arch.sh`.

**Tech stack:** Go (executor, single module `kodos`), Lua 5.4 + gopher-lua (generator/lockfile/suites), new Go dep `github.com/klauspost/compress/zstd`, bash/qemu/nbd (vm harness).

**Spec:** `docs/superpowers/specs/2026-11-18-arch-backend-milestone1-design.md`

## Global Constraints

- No new IR ops; op names/ranks (`executor/ir.go` `opRank`) unchanged. Distro branches live inside op implementations.
- Every task ends with Alpine untouched: `cd executor && go test ./... && go run . suitetest` passes.
- Arch pins: `{"name": {"version": ..., "checksum": ..., "repo": ...}}`; `checksum` is the ALPM `%CHKSUM%` (sha256 of the package file); `repo` is the DB section name (`core`/`extra`). Alpine pins keep omitting `repo`.
- `pacman` is never invoked by executor or `lockfile.lua`. GPG verification of sync DBs happens only in `vm/run-arch.sh` using `gpg --homedir /etc/pacman.d/gnupg`.
- Lua suite files must run under gopher-lua (`go run . suitetest`): pure data transformation, no `io.popen`/`os.execute` in the new suite module.
- All commands run from `executor/` unless stated. Test fixture package file shape: `<name>-<version>.pkg.tar.zst` (e.g. `hello-1.0-1.pkg.tar.zst`).
- Commit after every task (one commit per task minimum).

## Review Focus

Failure modes the spec implies that no single happy-path test exercises; each gets a pinning test in the named task:

1. **Dangling wants-symlink** — enabling a unit no pinned package ships: systemd silently skips broken wants links (service just never starts). Expected: hard error at `enable_service`. Pinned in Task 6.
2. **locale-gen ordering** — `/etc/locale.gen` is written before activation (op rank 11) but `locale-gen` can only run against the live activated root; if it never runs, the locale silently doesn't exist at boot. Expected: `locale-gen` runs right after `activate_generation` when the stream configured a locale. Pinned by the guest check in Task 11 (unit part pinned in Task 7).
3. **usrmerge layout** — the arch generation tree has `/lib` as a package-shipped symlink to `usr/lib`; scans that assume a real `gen/lib/modules` directory or hardcoded `gen/usr/...` diverge. Expected: kernel pairing works from a tree whose `/lib` is a symlink. Pinned in Task 8.
4. **Rolling mirror drift** — pinned version 404s at fetch time (repo moved after locking). Expected: hard error naming package, version and URL — never another version. Pinned in Task 4.
5. **Hollow initramfs** — mkinitcpio succeeds but the image lacks systemd/init (broken HOOKS), booting into a panic. Expected: arch `initramfsSane` rejects a cpio stream without `usr/lib/systemd/systemd`. Pinned in Task 8.

---

### Task 1: Distro gate + pin schema

**Files:**
- Modify: `executor/main.go:149-154` (distro gate), `executor/ir.go` (`pkgPin`)
- Test: `executor/ir_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `func checkDistro(af *actionsFile, meta *metadata) error` in `ir.go` (the gate extracted from `cmdExec`); `pkgPin.Repo string` with `json:"repo,omitempty"`; accepted distros `"alpine"` and `"arch"`.

- [ ] **Step 1: Write the failing test** `ir_test.go`: `TestCheckDistro` — three cases: actions+metadata `distro:"arch"` → nil; `distro:"deb"` → error containing `deb`; actions `arch` vs metadata `alpine` → error containing `disagree`. Also assert `json.Unmarshal` of `{"version":"6.1-1","checksum":"ab","repo":"core"}` into `pkgPin` yields `Repo=="core"`.
- [ ] **Step 2: Run** `go test ./... -run TestCheckDistro` — expect FAIL (undefined).
- [ ] **Step 3: Implement** `checkDistro` in `ir.go` (allow `"alpine"`, `"arch"`; else error naming the got distro and the supported set; then the existing actions/meta agreement rule). Replace the inline gate in `cmdExec` with `checkDistro(af, meta)`. Add the `Repo` field to `pkgPin`.
- [ ] **Step 4: Run** `go test ./...` — expect PASS.
- [ ] **Step 5: Commit** `git add executor && git commit -m "executor: accept arch distro in IR gate; pins carry repo"`

Note: `manage.go` rollback gate stays Alpine-only by design (milestone 2).

### Task 2: Lua ALPM resolver (`alpm.lua`) + lockfile integration

**Files:**
- Create: `executor/lua/alpm.lua`, `executor/lua/test-arch-lock.lua`, fixture `executor/lua/test-fixture/core.desc` (tiny hand-written ALPM desc text, see Step 1)
- Modify: `executor/lua/lockfile.lua` (ALPM mode: when `alpm.is_alpm` on every index arg, parse+resolve via `alpm.lua` and skip the entire apk-tools closure block), `executor/main.go` `cmdSuitetest` suites list (append `"test-arch-lock.lua"`)

**Interfaces:**
- Consumes: nothing.
- Produces (all in `alpm.lua`, pure Lua, no popen):
  - `alpm.is_alpm(text) boolean` — true iff text contains `"\n%CHKSUM%\n"` or `"\n%DEPENDS%\n"`
  - `alpm.parse(text, repo) -> { name = { version=, checksum=, repo=, depends={raw strings}, provides={raw strings} } }` — ALPM desc format: `%FIELD%` header line followed by value lines until blank line; fields needed: `%NAME% %VERSION% %PROVIDES% %DEPENDS% %CHKSUM%`
  - `alpm.vercmp(a, b) -> -1|0|1` — alpm semantics: optional `epoch:` prefix on the whole version; compare pkgver then pkgrel (segment-wise: digit runs numerically, other chars lexicographically, digits sort after letters)
  - `alpm.closure(requests {names}, index) -> locked { name = {version, checksum, repo} }` — iterative closure: each request's and each added pkg's `depends` must be satisfied by the exact-name package or a provider (`provides` entries parse as `name[=ver]`); constraints `name(=|>=|<=|>|<)ver` checked with `vercmp`; deps starting `so:` match providers only; candidate preference: exact name first, else providers sorted by name then version, first satisfying wins; unsatisfied dep → `error("unsatisfied dependency <dep> of <pkg>", 0)`. Cycles are fine (visited set).

- [ ] **Step 1: Write the failing test** `test-arch-lock.lua` (plain asserts, `print("test-arch-lock: ok")` at the end, mirroring `test-lock.lua` style). Fixture `core.desc` (checked into `executor/lua/test-fixture/`):
  ```
  %FILENAME%
  core/os/x86_64/glibc-2.40-1.pkg.tar.zst
  %NAME%
  glibc
  %VERSION%
  2.40-1
  %PROVIDES%

  %DEPENDS%
  linux>=6.6
  filesystem
  %CHKSUM%
  0000000000000000000000000000000000000000000000000000000000000001

  %FILENAME%
  core/os/x86_64/filesystem-2025.10-1.pkg.tar.zst
  %NAME%
  filesystem
  %VERSION%
  2025.10-1
  %PROVIDES%

  %DEPENDS%

  %CHKSUM%
  0000000000000000000000000000000000000000000000000000000000000002

  %FILENAME%
  core/os/x86_64/linux-6.12-1.pkg.tar.zst
  %NAME%
  linux
  %VERSION%
  6.12-1
  %PROVIDES%
  kernel=6.12
  so:libcrypto.so.3=3.4
  %DEPENDS%
  core-utils
  %CHKSUM%
  0000000000000000000000000000000000000000000000000000000000000003

  %FILENAME%
  core/os/x86_64/core-utils-1-1.pkg.tar.zst
  %NAME%
  core-utils
  %VERSION%
  1-1
  %PROVIDES%

  %DEPENDS%

  %CHKSUM%
  0000000000000000000000000000000000000000000000000000000000000004
  ```
  Assertions: `is_alpm` true on fixture / false on apk text (`"P:glibc\nV:2.40-1\nC:abc\n"`); parse gives glibc version/checksum/repo (pass repo arg `"core"`); `vercmp("1.2-3","1.10-1") == -1`, `vercmp("2:1.0","1:9.9") == 1`, `vercmp("1.0-1","1.0-1") == 0`; `closure({"filesystem"}, idx)` pins exactly those 2 with repos/checksums; `closure` with a dep satisfiable only by a provides name resolves to the provider; `closure` with dep `"linux>=7"` against the fixture fails via `pcall`.
- [ ] **Step 2: Run** `lua test-arch-lock.lua` (from `executor/lua`) — expect FAIL (module not found).
- [ ] **Step 3: Implement** `alpm.lua` per the interface block.
- [ ] **Step 4: Run** `lua test-arch-lock.lua` — expect PASS; then `go run . suitetest` after appending the suite — expect it to include the new suite and pass.
- [ ] **Step 5: lockfile integration.** In `lockfile.lua`: after loading index args, if `alpm.is_alpm` matches, build the map via `alpm.parse(text, repoOf(arg))` where `repoOf` derives the repo name from the file name (`core.desc` → `core`, `extra.desc` → `extra`; document this in the usage header) and resolve via `alpm.closure`; skip the `KODOS_APK` simulate block entirely on that path. Keep the JSON output identical in shape; arch pins additionally carry `repo`.
- [ ] **Step 6: Integration check (not a suite test):** create a minimal throwaway config `/tmp/arch-min.lua` returning `{ repos = { base = { type = "arch", mirror = "https://geo.mirror.pkgbuild.com", branches = {"core","extra"} } }, boot = { bootloader = { type = "grub", efi = true } }, system = { host_name = "t", timezone = "UTC" }, generations = { root = "/kod" }, storage = {}, packages = { { name = "filesystem" } } }`; run `lua lockfile.lua 0 test-fixture/core.desc -- /tmp/arch-min.lua`; expect JSON with `distro: "arch"` and every package carrying `repo`. Fixture names must be extended to cover whatever `base_packages`+`kernel_packages` the arch backend demands — add the missing tiny records to the fixture (that is legitimate fixture growth; resolver semantics assertions stay as above).
- [ ] **Step 7: Commit** `git add executor && git commit -m "lockfile: ALPM index mode + Lua closure resolver"`

### Task 3: Arch repo rendering (`configure_repos`)

**Files:**
- Modify: `executor/main.go` `configureRepos` (~290-335)
- Test: `executor/repos_test.go` (create)

**Interfaces:**
- Consumes: `e.mirror`, `e.branches` already captured by `configureRepos`.
- Produces: `func renderPacmanConf(branches []string) string` and `func renderMirrorList(mirror string) string` in `main.go`; arch branch writes exactly these to `/etc/pacman.conf` and `/etc/pacman.d/mirrorlist` via `e.writeFileTo`, skipping the `/etc/apk/*` writes.

- [ ] **Step 1: Write the failing test** `TestRenderArchRepos`: assert `renderMirrorList("https://geo.mirror.pkgbuild.com")` == `"## kodos-managed: repos.base.mirror\nServer = https://geo.mirror.pkgbuild.com/$repo/os/$arch\n"`; assert `renderPacmanConf({"core","extra"})` contains `SigLevel = Required DatabaseOptional`, `ParallelDownloads = 5`, and the two repo sections each followed by `Include = /etc/pacman.d/mirrorlist`, in order core then extra.
- [ ] **Step 2: Run** `go test ./... -run TestRenderArchRepos` — expect FAIL (undefined).
- [ ] **Step 3: Implement** the two render funcs; body of `configureRepos` branches on `e.meta.Distro == "arch"` to write them (mirrorlist mode 0644, pacman.conf 0644). `e.repoDistro`-style extra state: not needed.
- [ ] **Step 4: Run** `go test ./...` — PASS.
- [ ] **Step 5: Commit** `git add executor && git commit -m "configure_repos: arch pacman.conf/mirrorlist rendering"`

### Task 4: `.pkg.tar.zst` fetch, checksum verify, zstd unpack

**Files:**
- Modify: `executor/main.go` (`fetchUnpack`, `unpackApkStream`), new `executor/archpkg.go`
- Test: `executor/archpkg_test.go` (create)
- Dep: `go get github.com/klauspost/compress@latest` (use ONLY its `zstd` package)

**Interfaces:**
- Consumes: `pkgPin.Repo`, `pkgPin.Checksum` (Task 1).
- Produces:
  - `func unpackTarStream(r io.Reader, dst, name string) error` in `main.go` — the tar-walking body of `unpackApkStream` moved verbatim (full `hdr.FileInfo().Mode()` semantics, unsafe-path guard, chmod-after-write).
  - `func unpackArchStream(body io.Reader, dst, name string) error` in `archpkg.go` — zstd then `unpackTarStream`.
  - `func archPkgURL(mirror, repo, arch, name, version string) string` in `archpkg.go` = `mirror + "/" + repo + "/os/" + arch + "/" + name + "-" + version + ".pkg.tar.zst"`.
  - `func verifySha256(path, want string) error` in `archpkg.go`.
  - `fetchUnpack` arch branch: download to a temp file (http only, no localRepos — no local repos this milestone), `verifySha256` against `e.meta.Packages[name].Checksum` (missing pin or missing repo → error `arch fetch %s-%s: no repo/checksum pin`), then `unpackArchStream` into the store dir. Non-200 on every branch → error listing attempted URLs.

- [ ] **Step 1: Write the failing test** `archpkg_test.go` with `TestUnpackArchStream`: in-test build `hello-1.0-1.pkg.tar.zst` bytes via `zstd.NewWriter` + `archive/tar` with members `.PKGINFO` (0644), `usr/bin/hello` (0755, content `"x"`), `etc/hello.conf` (0644); call `unpackArchStream` into tmpdir; assert the three files exist with those modes. `TestArchPkgURL` exact string check. `TestVerifySha256`: file with known content passes with its own hash, fails with `"0"*64` (error mentions the package path).
- [ ] **Step 2: Run** `go test ./... -run 'Arch|Sha256'` — expect FAIL (undefined).
- [ ] **Step 3: Implement** the refactor (extract `unpackTarStream` from `unpackApkStream` with zero behavior change; Alpine selftest must still pass) + `archpkg.go` + the `fetchUnpack` arch branch.
- [ ] **Step 4: Run** `go test ./... && go run . selftest` — PASS (Alpine unpack unchanged).
- [ ] **Step 5: Commit** `git add executor go.mod go.sum && git commit -m "executor: arch pkg tarball fetch with sha256 pin enforcement + zstd unpack"`

### Task 5: `install_packages` arch branch (pins ARE the closure)

**Files:**
- Modify: `executor/main.go` `installPackages` (~380-421)
- Test: extend `executor/archpkg_test.go` with `TestInstallPackagesArch`

**Interfaces:**
- Consumes: `e.meta.Packages` (pins incl. `Repo`), Task 4 fetch path.
- Produces: `installPackages` arch branch — for every requested name: pin must exist in `e.meta.Packages` else error `install_packages %s: not pinned in generation metadata`; `resolved = map of ALL pinned name->version` (lockfile already emitted the full closure — no live resolve, no apk); fetch/unpack loop and `e.resolved = resolved` unchanged downstream (createGeneration consumes the same `map[string]string`).

- [ ] **Step 1: Write the failing test:** executor with `meta{Distro:"arch", Packages:{...pins...}}`, mirror set, names action `{"filesystem"}`; run against a `httptest.Server` serving `/core/os/x86_64/filesystem-2025.10-1.pkg.tar.zst` (fixture bytes from Task 4) — assert store dir populated and `e.resolved` equals the full pins map; second case: request `"ghost"` absent from pins → error contains `not pinned`.
- [ ] **Step 2: Run** `go test ./... -run TestInstallPackagesArch` — expect FAIL.
- [ ] **Step 3: Implement** the branch.
- [ ] **Step 4: Run** `go test ./...` — PASS.
- [ ] **Step 5: Commit** `git add executor && git commit -m "install_packages: arch resolves from locked pins, no live solver"`

### Task 6: systemd service enabling (Go) + serial console (Lua)

**Files:**
- Modify: `executor/ops.go` (`enableService`, `snapshotRunlevels`, `reconcileRunlevels`), `executor/lua/backends/arch.lua` (add `M.serial_console_unit = "serial-getty@ttyS0"`), `executor/lua/generate.lua` (serial-console branch + service plan emit), `executor/lua/test-generate.lua`
- Test: `executor/services_test.go` (create)

**Interfaces:**
- Consumes: `e.meta.Distro`, gen tree (`e.s.genDir(e.meta.Generation)`), `e.s.chrootResolve`.
- Produces:
  - arch `enableService`: target dir from runlevel — `boot`/`default` → `multi-user.target`, `getty` → `getty.target` (other runlevels → error `arch enable_service: runlevel %q unsupported`); unit gains `.service` suffix if it contains no `.`; BEFORE symlinking, the unit file must exist in the gen tree: real path via `e.s.chrootResolve(filepath.Join(genDir, "usr/lib/systemd/system/"+unitFile))` — missing → error `enable_service %s: unit %s not shipped by any package` (Review Focus 1). Symlink: `<chroot>/etc/systemd/system/<target>.wants/<unitFile>` -> `/usr/lib/systemd/system/<unitFile>` (create dirs, idempotent: existing symlink with same target is fine). Declared key: `target + ".wants/" + unitFile`.
  - `snapshotRunlevels` arch form: walk `<rootDir>/etc/systemd/system`, dirs matching `\.wants$`, keys `dir+"/"+entry` (same key space as above; the map shape `map[string]bool` is unchanged).
  - `reconcileRunlevels` arch form: same sweep over `<chroot>/etc/systemd/system/*.wants`, deleting symlink entries whose key is neither in `e.declaredRunlevels` nor `e.hookRunlevels`.
  - `generate.lua`: the `conf.system.serial_console` branch becomes: `backend.inittab` → write_file (Alpine, unchanged); elseif `backend.serial_console_unit` → remember; the existing enable_service emission loop then also emits `act("enable_service", { name = "serial-console", unit = backend.serial_console_unit, runlevel = "getty" })`; neither key → the existing hard error.

- [ ] **Step 1: Write the failing test** `services_test.go` with `TestEnableServiceArch` and `TestReconcileWants`: scratch root with gen tree containing `usr/lib/systemd/system/sshd.service` (regular file) — `enable_service` action `{name:"ssh", unit:"sshd", runlevel:"default"}` → assert symlink `<chroot>/etc/systemd/system/multi-user.target.wants/sshd.service` exists as a symlink with target `/usr/lib/systemd/system/sshd.service`; unit `sshd` NOT in gen tree → error `not shipped by any package` and no symlink; `reconcileRunlevels` with declared `{"multi-user.target.wants/sshd.service"}` and a pre-existing stray `bluetooth.service` symlink in the wants dir → stray removed, declared kept; `serial-getty@ttyS0` with `runlevel="getty"` lands in `getty.target.wants` (fixture unit file `serial-getty@.service` counts: match templated units — gen tree has `serial-getty@.service`, requested `serial-getty@ttyS0.service`, either file satisfies the existence check).
- [ ] **Step 2: Run** `go test ./... -run TestEnableServiceArch|TestReconcileWants` (name them exactly) — expect FAIL.
- [ ] **Step 3: Implement** the three Go branches (branch on `e.meta.Distro == "arch"`; OpenRC path untouched).
- [ ] **Step 4:** add to `test-generate.lua` a case: arch config (`repos.base.type="arch"`, `system.serial_console=true`) → generated stream contains an `enable_service` action with `unit="serial-getty@ttyS0"` and `runlevel="getty"`, and does NOT contain a write_file of `/etc/inittab`. Run `lua test-generate.lua` — FAIL then implement the generate.lua branch, PASS.
- [ ] **Step 5: Run** `go run . suitetest && go test ./...` — PASS.
- [ ] **Step 6: Commit** `git add executor && git commit -m "services: systemd wants-symlink enable + reconcile; arch serial console unit"`

### Task 7: System file rendering branches + `.pacnew` naming

**Files:**
- Modify: `executor/ops.go` (`configureLocale`, `configureKeyboard`, `configureTimezone`, `loadKernelModules`), `executor/store.go` (`system` struct + `installEtc` conflict name), `executor/main.go` (construct `system{... Distro: af.Distro}`; `activate_generation` case), `executor/selftest.go` (constructors if they build `system`)
- Test: `executor/files_test.go` (create)

**Interfaces:**
- Consumes: `e.meta.Distro` (already on metadata; also mirrored into `e.s.Distro` for store.go which has no executor backref).
- Produces:
  - `system` gains `Distro string`; `installEtc` conflict suffix: `".apknew"` alpine → `".pacnew"` when `s.Distro == "arch"` (same hardlink dance otherwise).
  - arch `configureLocale`: write `/etc/locale.gen` line `en_US.UTF-8 UTF-8` (for `def`, replacing the alpine `/etc/env.d/02locale` write) AND `/etc/locale.conf` line `LANG="en_US.UTF-8"` (quoted, matching systemd convention); set executor field `e.needLocaleGen = true`.
  - `activate_generation` case in `cmdExec`: after successful activation, `if e.meta.Distro == "arch" && e.needLocaleGen { e.chrootSh(e.s.chroot, "locale-gen") }` (idempotent whole-file regen; one `ponytail:` comment: full rebuild each activation, per-locale check if it ever gets slow). This is the Review Focus 2 mechanism; its boot-level pin is Task 11's guest check.
  - arch `configureKeyboard`: write `/etc/vconsole.conf` `KEYMAP=us` (unquoted; only when a console keymap is set).
  - arch `configureTimezone`: symlink only (skip the `/etc/timezone` file write; modern Arch dropped it — matches spec).
  - arch `loadKernelModules`: append to `<chroot>/etc/modules-load.d/kodos.conf` (systemd-modules-load reads it at boot; same idempotent skip-if-listed logic).

- [ ] **Step 1: Write the failing test** `files_test.go` against a scratch root executor: `configure_locale {default:"en_US.UTF-8"}` on distro arch → `/etc/locale.gen` has the UTF-8 line and `/etc/locale.conf` has `LANG="en_US.UTF-8"`, and NO `/etc/env.d/02locale`; `configure_keyboard {layout:"us"}` → `/etc/vconsole.conf` `KEYMAP=us`; `configure_timezone {timezone:"America/Edmonton"}` → `etc/localtime` symlink to `/usr/share/zoneinfo/America/Edmonton` and NO `/etc/timezone`; `installEtc` conflict on an `arch` system produces `.pacnew-0`, on `alpine` still `.apknew-0`.
- [ ] **Step 2: Run** `go test ./... -run 'TestConfigureLocaleArch|TestConfigureKeyboardArch|TestConfigureTimezoneArch|TestEtcConflictNames'` (use exactly these names) — expect FAIL.
- [ ] **Step 3: Implement** the branches + `system.Distro` plumbing + the activate hook.
- [ ] **Step 4: Run** `go test ./... && go run . selftest` — PASS (alpine files/e tc unchanged: existing `store_test.go` must still pass).
- [ ] **Step 5: Commit** `git add executor && git commit -m "system files: arch locale-gen/locale.conf/vconsole/modules-load.d + .pacnew naming"`

### Task 8: Boot branch — arch kernel pairing, mkinitcpio, image guards

**Files:**
- Modify: `executor/ops.go` (`installBootloader` kernel loop + initramfs guards), new pure helper in `executor/archboot.go`
- Test: `executor/archboot_test.go` (create)

**Interfaces:**
- Consumes: `e.meta.Packages` (kernel pins: package names `linux`, `linux-lts`, `linux-hardened`, `linux-zen` — regex `^linux(-[a-z]+)*$`), gen tree dirs.
- Produces: `type kJob struct { Pkg, Flavor, Kver string }` and `func archKernelJobs(pins map[string]pkgPin, modDirs []string) ([]kJob, error)` in `archboot.go`: each pinned `linux*` package whose `Version` matches one of `modDirs` produces `kJob{Pkg, Flavor: Pkg, Kver: version}` (flavor == package name, so `linux` -> `vmlinuz-linux` + `initramfs-linux.img`); every `modDirs` entry must be owned by exactly one pin, else error `modules dir %s has no pinned kernel package` (fails closed before mutations). Review Focus 3: `modDirs` is obtained from `filepath.Glob(filepath.Join(e.s.genDir(...), "lib", "modules", "*"))` on BOTH distros — the symlinked `/lib` resolves through the tree, so no `usr/lib` hardcoding; the test fixture must reproduce that symlink.
- arch installBootloader loop: per `kJob` run `depmod <Kver>` then `mkinitcpio -o /boot/initramfs-<Flavor>.img <Kver>` via `e.genChrootSh` (replacing the Alpine `kernel-suffix` scan only when `e.meta.Distro=="arch"`).
- Guards: initramfs existence check uses `initramfs-<Flavor>.img` for arch; `initramfsSane` gains an arch form: the cpio stream must contain `usr/lib/systemd/systemd` (any of the name encodings the existing scanner tolerates, `./`-prefixed included) — Review Focus 5.

- [ ] **Step 1: Write the failing test** `archboot_test.go`: `TestArchKernelJobs` — pins `{linux:6.12-1, linux-lts:6.6-1}` + modDirs `["6.12-1","6.6-1"]` → 2 jobs sorted by Pkg; modDir `"6.13-1"` with no pin → error names it. `TestInitramfsSaneArch` — cpio bytes (reuse/extend the padded cpio writer pattern from `selftest.go`) containing `usr/lib/systemd/systemd` pass; without it fail with the `skeleton` error message.
- [ ] **Step 2: Run** `go test ./... -run 'TestArchKernelJobs|TestInitramfsSaneArch'` — expect FAIL.
- [ ] **Step 3: Implement** the helper + the distro branches in `installBootloader`/`initramfsSane` (Alpine path byte-identical).
- [ ] **Step 4: Run** `go test ./...` — PASS.
- [ ] **Step 5: Commit** `git add executor && git commit -m "bootloader: arch kernel pairing + mkinitcpio + systemd initramfs guard"`

### Task 9: Arch VM config (standalone layer)

**Files:**
- Create: `vm/arch-config.lua`

**Interfaces:**
- Consumes: backends/arch.lua (incl. Task 6 `serial_console_unit`), validate.lua, generate.lua.
- Produces: the single input for Task 10: `lua generate.lua vm/arch-config.lua` and `lua lockfile.lua 0 core.desc extra.desc -- vm/arch-config.lua`.

- [ ] **Step 1: Write the config** as ONE standalone table (no `modules/` requires — the eszkoz module set is Alpine-flavored, YAGNI for this milestone):
  - `repos.base = { type = "arch", mirror = "https://geo.mirror.pkgbuild.com", branches = { "core", "extra" } }` (current stable repos are exactly core+extra — verified against ArchWiki Nov 2026)
  - `boot = { bootloader = { type = "grub", efi = true, timeout = 5 } }`, kernel variant absent (default `linux` + `linux-firmware`), `arguments = { "console=ttyS0" }`
  - `storage.devices.disk0`: device `/dev/nbd0`, GPT + EFI layout — copy the exact partition shape from `executor/lua/modules/filesystems.lua` (read it; EFI 512M vfat + rest ext4 root)
  - `generations = { storage = "store", root = "/kod", keep = 5 }`
  - `system = { host_name = "kodos-vm", timezone = "America/Edmonton", keyboard_layout = { layout = "us" }, console_keymap = "us", locale = { default = "en_US.UTF-8" }, serial_console = true }`
  - `services = { ssh = { enable = true, permit_root_login = true }, networkmanager = { enable = true } }` (both have arch.lua registry entries; NetworkManager proves a real wants-symlink service, sshd proves the settings file path)
  - `users = { abuss = { name = "Test User", group = "wheel", supplementary_groups = {}, create_home = true, shell = "/bin/bash" } }` (passwords arrive via the secrets file, like the Alpine round)
  - `packages = { { name = "sudo" }, { name = "git" } }` (canonical names; arch map identity)
- [ ] **Step 2: Verify generation:** `cd executor/lua && lua generate.lua ../../vm/arch-config.lua | grep -o '"distro":"arch"'` prints once; stream contains `"op":"configure_repos"` with `"type":"arch"`, an `enable_service` for `NetworkManager`, `serial-getty@ttyS0`, and `install_packages` with `networkmanager`/`openssh`/`base`/`linux`.
- [ ] **Step 3: Verify lockfile mode against fixture?** No — real DBs arrive in Task 10; skip.
- [ ] **Step 4: Commit** `git add vm/arch-config.lua && git commit -m "vm: standalone Arch milestone-1 config"`

### Task 10: `vm/run-arch.sh` harness

**Files:**
- Create: `vm/run-arch.sh` (copy the skeleton of `vm/run.sh`, Alpine specifics replaced; disk `vm/arch-disk.qcow2`, mnt `vm/mnt-arch`)

**Interfaces:**
- Consumes: Tasks 1-9 everything (`kodos exec` without `-apk` needs `-apk` default "apk" but never invoked on the arch path — leave the flag), `vm/arch-config.lua`.
- Produces: the e2e entry point for Task 11.

- [ ] **Step 1: Implement** phases `fresh|install|boot|guest|all` exactly like `run.sh` with these substitutions: DB snapshot step replaces APKINDEX step —
  ```sh
  M=https://geo.mirror.pkgbuild.com
  for r in core extra; do
      curl -sfO $M/$r/os/x86_64/$r.db.tar.gz       # into $D/arch/
      curl -sfO $M/$r/os/x86_64/$r.db.tar.gz.sig
      gpg --homedir /etc/pacman.d/gnupg --verify $r.db.tar.gz.sig $r.db.tar.gz || {
          echo "ERROR: $r.db signature FAILED (host keyring: pacman-key --init && pacman-key --populate archlinux)"; exit 1; }
      tar -xOzf $r.db.tar.gz $r.desc > $D/arch/$r.desc
  done
  ```
  then `lua lockfile.lua 0 $D/arch/core.desc $D/arch/extra.desc -- "$REPO/vm/arch-config.lua" > "$VMDIR/arch-generation-0.json"`, `lua generate.lua vm/arch-config.lua` step (with the nbd-index sed like run.sh), and `kodos exec -actions ... -metadata ... -root $MNT -secrets $VMDIR/secrets.json` (no `-apk`, no mkrepo/mkapk). Same nbd attach/cleanup/idempotence functions, same secrets generation, same OVMF probing boot function, guest ssh stays port 2222 with the same test creds.
- [ ] **Step 2: Smoke (non-root, network):** `sh -n vm/run-arch.sh` passes; run the DB fetch + `lockfile.lua` steps manually on the real snapshots and eyeball `arch-generation-0.json`: base/linux/openssh/networkmanager present, every pin has version+checksum+repo.
- [ ] **Step 3: Commit** `git add vm/run-arch.sh && git commit -m "vm: arch end-to-end harness (DB sig verify, lock, exec, boot)"`

### Task 11: End-to-end round + guest assertions + final gates

**Files:**
- (fixes found by the round, anywhere)

- [ ] **Step 1:** `vm/run-arch.sh fresh` then `install` (as root via sudo; this is the first full arch exec — expect to fix real bugs here; commit each fix).
- [ ] **Step 2:** `vm/run-arch.sh boot` (interactive) or scripted: `timeout 90 vm/run-arch.sh boot 2>&1 | tee /tmp/opencode/arch-boot.log`; boot success marker: login prompt on serial (grep `kodos-vm login:`).
- [ ] **Step 3:** `vm/run-arch.sh guest '<cmd>'` assertions (each must pass):
  - `systemctl is-enabled sshd NetworkManager` → both `enabled` (wants symlinks survived activation)
  - `systemctl is-system-running` → `running` or `degraded`; if degraded, `systemctl --failed` output must be empty of units the config enables
  - `locale -a | grep -i en_us.utf8` → nonempty (Review Focus 2 pin)
  - `cat /etc/vconsole.conf` → `KEYMAP=us`; `cat /etc/locale.conf` → `LANG="en_US.UTF-8"`
  - `date +%Z` matches the configured timezone offset; `hostname` → `kodos-vm`
  - `login abuss` over ssh with the test password, `%wheel` sudo works (`sudo -n true` after password, or `id -nG` includes wheel)
- [ ] **Step 4:** idempotence: run `install` a second time (no `fresh`), boot, repeat Step 3 quickly.
- [ ] **Step 5:** Alpine regression: `cd executor && go test ./... && go run . suitetest && go run . selftest` all pass; `vm/run.sh install` still completes (boot optional if time-boxed, install must pass).
- [ ] **Step 6:** Commit any outstanding fixes; tag the milestone: `git commit --allow-empty -m "milestone: bootable Arch VM end-to-end (milestone 1)"` only if fixes needed a summary; otherwise skip the empty commit.

---

## Deviations from the spec (deliberate, recorded)

- Spec §Executor mentions `.PKGINFO` parsing for conf-file semantics. Implementation fact: the executor's pin map drives everything and conf-conflict naming is the only behavior branch — no `.PKGINFO` parsing is built this milestone (YAGNI). `.pacnew` naming lands in Task 7 as specified.
- Spec put arch modules at `usr/lib/modules`; the code scans `gen/lib/modules` on BOTH distros and relies on the package-shipped `/lib -> usr/lib` symlink — same result, one code path (Review Focus 3 pins it).
- Spec §Executor lists a `create_user` useradd branch. Codebase fact: `createUser` (ops.go:1152) ALREADY uses `useradd`/`groupadd`/`chpasswd` (shadow), which Arch's `base` ships — zero changes needed; audit-only.
