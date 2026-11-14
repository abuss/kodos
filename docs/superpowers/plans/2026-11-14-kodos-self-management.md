# Kodos Self-Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the installed machine manage itself: `kodos apply` / `status` / `rollback` run ON the machine, with the Lua toolchain embedded in one static Go binary and kodos itself distributed as a locked package from a local `file://` repo.

**Architecture:** The executor becomes the `exec` subcommand of a single `kodos` binary that embeds the whole Lua toolchain (`go:embed` + gopher-lua, Lua 5.1+`goto` dialect). `apply` = config load → in-place generate (no storage ops) → live closure lock (fetch indexes with `net/http` / `file://`) → `exec -root /`. kodos reaches the machine via a new `copy_tree` IR op that ships `/var/kodos/repo` during the bootstrap round; after that kodos is an ordinary locked, prunable, rollback-able package.

**Tech Stack:** Go 1.27 (CGO_ENABLED=0), gopher-lua, existing Lua 5.1-clean toolchain in this repo, apk-tools (`apk index`/`-X file://`), Alpine VM harness `vm/run.sh`.

**Spec:** `docs/superpowers/specs/2026-11-14-kodos-self-management-design.md`

## Global Constraints

- Pure Go only: one new dependency, `github.com/yuin/gopher-lua`; `CGO_ENABLED=0` everywhere; binary stays statically buildable.
- Lua dialect: 5.1 (+`goto`). Host `lua` (5.5) may run the suites in dev, but `kodos suitetest` (gopher-lua) is the binding gate.
- No host-distro assumptions in shipped code paths: repo-local tools + the target's own `apk` only. Never modify the symlinked external projects (kodos, install-alpine, generation-apks, nkodos).
- Destructive guard is defense-in-depth: in-place stream omits storage ops AND the executor refuses format of a mounted target regardless of stream.
- TDD order per task (failing check → implement → pass); commit at every task end; all five existing Lua suites + `kodos selftest` stay green at every commit.

## File Structure

- `executor/` Go module renamed to module `kodos`:
  - `main.go` — subcommand dispatch (`exec`, `selftest`, `suitetest`, `lua`, `apply`, `status`, `rollback`)
  - `luaembed.go` — `go:embed` of `executor/lua/**`, preload registry, `runLuaScript`, gopher-lua openlibs + `io.popen`/env shims
  - `apply.go` — config resolution, index fetch, apply pipeline
  - `manage.go` — `status` + `rollback` verbs
  - `ops.go`/`main.go` — `copy_tree` op, format/mount guards, `generations_keep` prune, per-gen metadata persistence
- `executor/lua/` — the toolchain Lua MOVED from `example/eszkoz/` via `git mv` (`json resolve validate generate lockfile kodos system modules/ backends/ packages/ test-*.lua`); `example/eszkoz/` keeps the user-facing example configs (`configuration.lua`, `layers/`, README) pointing at the toolchain dir.
- `build/mkapk.sh` — builds `kodos.apk` + index into `vm/repo/`.
- `vm/repo/` — local repo dir (gitignored binary contents; script rebuilds).
- `vm/vm-layer.lua` — declares the local repo + `kodos` package; `vm/run.sh` — `kodos exec` + `apply/status/rollback` ssh phases.
- `example/eszkoz/IR.md`, `NEXT.md` — contract updates.

## Review Focus

1. **Index unreachable during apply** (typo'd repo URL, offline) → apply aborts with the failing URL named, BEFORE any mutation; guest state untouched. → Task 11 test.
2. **Config path mistakes** (nonexistent path, dir without `config.lua`, non-`.lua` file) → one clear error naming what was looked for. → Task 11 test.
3. **Rollback to a missing/corrupt generation** → hard error listing available generations, nothing activated. → Task 12 test.
4. **A stream (hand-written or future bug) formats a live/mounted target** → executor refuses hard, non-zero exit, device untouched. → Task 6 test.
5. **Prune cap smaller than generations on disk** → never prunes the generation `current` points at; newest others survive to the cap. → Task 10 test.

---

### Task 1: `kodos` binary with subcommand dispatch

**Files:**
- Modify: `executor/main.go` (flag parse + `selftest` arg → dispatch), `executor/go.mod` (module `kodos`)
- Modify: `vm/run.sh` (call `"$REPO/executor/kodos" exec …`), scratch E2E invocations in docs
- Test: existing `selftest` + scratch E2E must pass unchanged

**Interfaces:**
- Produces: binary `executor/kodos`; `cmdExec(args []string) error` (today's main body), `cmdSelftest()` (today's `selftest()` entry). Unknown/missing subcommand → usage to stderr, exit 2.

- [x] **Step 1:** Reorganize `main.go`: `main()` switches on `os.Args[1]` → `exec` → `cmdExec(os.Args[2:])` (existing flag parsing moves verbatim), `selftest` → existing path. Build, run `./kodos selftest` — expect the two PASS lines exactly as today.
- [x] **Step 2:** Update `vm/run.sh` executor invocation to `kodos exec`; re-run the scratch E2E command (README/NEXT.md recipe with `./executor/kodos exec`): same behavior incl. the expected fake grub failure.
- [x] **Step 3:** Commit `refactor(executor): kodos binary with exec subcommand`.

### Task 2: Embed the Lua toolchain; `kodos lua` runner

**Files:**
- Move: `git mv example/eszkoz/{json,resolve,validate,generate,lockfile,kodos,system}.lua example/eszkoz/modules example/eszkoz/backends example/eszkoz/packages example/eszkoz/test-*.lua executor/lua/`
- Create: `executor/luaembed.go`
- Modify: `executor/main.go` (subcommand `lua`), host lua tests' working dir (`cd executor/lua` when running suites with host lua), `vm/run.sh` lua paths
- Test: `kodos lua test-merge.lua` (pure suite, no popen-of-lua) must PASS under gopher-lua

**Interfaces:**
- Produces: `runLuaScript(name string, args []string, searchDir string) (string, error)` in `luaembed.go` — loads embedded `name` (and preloads every embedded `.lua` under its path so `require("resolve")` etc. work); `searchDir` (may be `""`) additionally puts that ON-disk directory on the module path (Task 11 passes the config dir; Task 2 passes `""`), exposes `arg` (1-based after script name, Lua-5.1 style), returns captured stdout. `add gopher-lua` to go.mod. Open gopher-lua base/os/io libraries; **if** gopher-lua lacks `io.popen` or `os.execute`, register Go shims (`io.popen` → `os/exec` pipe to a temp file Lua can `io.open`; that's the whole shim).
- Produces: `cmdLua(args)` = `kodos lua <script.lua> [args…]` → streams the script's stdout to stdout, exit = script status.

- [x] **Step 1:** Probe gopher-lua `io.popen`/`os.execute` availability (30-line Go test `luaembed_probe_test.go`, throwaway): decide shims now.
- [x] **Step 2:** Do the `git mv` (toolchain into `executor/lua/`, example configs stay in `example/eszkoz/`); fix host-suite run dirs + `vm/run.sh` paths. Verify: host-`lua` suites green from their new home.
- [x] **Step 3:** Implement `luaembed.go` (embed.FS `lua/*`, preload map, `arg`, shims per probe).
- [x] **Step 4:** Failing check → implement → pass: `./kodos lua test-merge.lua` exits 0 and prints its PASS line (run via gopher-lua, not host lua).
- [x] **Step 5:** Commit `feat(executor): embed Lua toolchain + kodos lua runner (gopher-lua)`.

### Task 3: Full Lua suite compatibility gate — `kodos suitetest`

**Files:**
- Modify: suites that spawn host lua: `executor/lua/test-lock.lua`, `test-generate.lua` (replace `"lua lockfile.lua …"` popen strings with `(os.getenv("KODOS_LUA_CMD") or "lua") .. " …"`; when unset, default to invoking the embedded path is NOT required — dev keeps host lua)
- Modify: any Lua file where gopher-lua stumbles (5.1-cleanliness fixes only, per scan none expected beyond maybe `os.date`/`os.tmpname` use)
- Modify: `executor/main.go` — `cmdSuitetest`: runs (embedded-VM) `test-merge`, `test-validate`, `test-equivalence` (pure), then re-runs `test-generate`/`test-lock` with `KODOS_LUA_CMD="kodos lua"` semantics by calling lockfile/generate through `runLuaScript` directly from Go instead of popen (simplest: suites take env `KODOS_INPROCESS=1` → require("…") + call functions instead of popen — add that branch to the two suites).
- Test: `./kodos suitetest` → five PASS lines

**Interfaces:**
- Consumes: `runLuaScript` from Task 2.
- Produces: `kodos suitetest` (used by CI, selfupgrades' smoke test, and this plan's later tasks).

- [x] **Step 1:** `cmdSuitetest` runs the pure suites first (failing: they don't exist as subtests yet) — implement the harness.
- [x] **Step 2:** Add `KODOS_INPROCESS=1` branches to test-lock/test-generate (popen helper indirection); run under `kodos suitetest`; fix every gopher-lua incompatibility encountered (one dialect-clean fix per stumble, no feature work).
- [x] **Step 3:** Verify `./kodos suitetest` prints 5 PASS lines; also host-`lua` suites still pass (dev path).
- [x] **Step 4:** Commit `test(executor): run Lua suites under gopher-lua (kodos suitetest)`.

### Task 4: Persist per-generation metadata (status/rollback substrate)

**Files:**
- Modify: `executor/main.go` (`create_generation` case) — after successful `createGeneration`, write `kod/generation/<N>/generation.json`: the executor's metadata (packages map) PLUS executor-stamped fields `timestamp`, `config_hash` (from `-metadata` file hash if provided else hash of actions stream), and `kernel_args` (from the last `set_kernel_arguments` op, `e.kernelArgs`).
- Test: `executor/selftest.go` — after create, assert `generation.json` exists in gen dir with the package map and `kernel_args`.

**Interfaces:**
- Produces: `kod/generation/<N>/generation.json` on disk, loadable as `metadata` struct + `{timestamp, config_hash, kernel_args}` extras; Tasks 10/12 read it.

- [x] **Step 1:** Extend selftest with the failing assertion (file absent).
- [x] **Step 2:** Implement the stamp write (marshal from the same maps the executor already holds); selftest PASS.
- [x] **Step 3:** Commit `feat(executor): persist per-generation metadata (generation.json)`.

### Task 5: `generate.lua` in-place mode

**Files:**
- Modify: `executor/lua/generate.lua` — scan args for a literal `--inplace` (stripped before the usual config-file parsing); when set, omit ops with op ∈ `{"partition_disk","format","create_swap"}` from the emitted stream; everything else identical.
- Modify: `executor/lua/test-generate.lua` — new failing assertions first.
- Modify: `example/eszkoz/IR.md` — document the mode (flag, not schema).

**Interfaces:**
- Produces: `kodos lua generate.lua --inplace <configfiles…> → actions.json` (Task 11 consumes).

- [x] **Step 1:** Add assertions: with `--inplace`, `actions.json` contains NO `partition_disk`/`format`/`create_swap` ops, and contains all of `configure_repos`/`install_packages`/`create_generation`/`activate_generation`/`install_bootloader`; without the flag, `format` still present. Run → FAIL.
- [x] **Step 2:** Implement the filter in generate.lua; suites green (host lua AND suitetest).
- [x] **Step 3:** Commit `feat(lua): generate --inplace omits storage ops`.

### Task 6: Executor guards — format refuses mounted, mount skip-if-mounted

**Files:**
- Modify: `executor/ops.go` `formatPartition` + `partitionDisk` (refuse when the target device or any partition of the disk appears in `/proc/mounts`), and `mountOp` (if `dst` is already a mountpoint per `/proc/mounts`, print `mount /x: already mounted, skipping` and return nil).
- Test: `executor/selftest.go` table check `mountedDevices(parseMounts(sample))` pure fn + one fs-backed check that a second `mount` op on the same scratch target no-ops (scratch E2E already re-mounts `/{,boot}`; observable: new skip line, run still green).

**Interfaces:**
- Produces: pure helpers `parseMounts(text string) map[string]string` (device/mountpoint → line) used by the guards and by Task 12's rollback fstab handling; guard error text starts `refusing to format mounted target`.

- [x] **Step 1:** Failing selftest: `parseMounts` + `isMounted(target, mounts, liveRoot)` (live-root exception: `-root` itself isn't a live mountpoint in dev flows — the guard checks `/proc/mounts` truth, not intent).
- [x] **Step 2:** Implement guards in the three ops; existing scratch E2E shows skip-lines on re-run and still completes.
- [x] **Step 3:** Commit `feat(executor): hard guards — no format of mounted targets, mount skip-if-mounted`.

### Task 7: Local `file://` repos end-to-end (schema + configure_repos + lock)

**Files:**
- Modify: `executor/lua/validate.lua` — repositories entries accept `{url = "file://…"}` (string or `{name,url}` shape — pick `{name = {..., url = ...}}` matching existing repos table style and follow it);
- Modify: `executor/lua/generate.lua` — pass local repo entries into the `configure_repos` action as `"repos": [{name,url}]`;
- Modify: `executor/ops.go` `configureRepos` — write `/etc/apk/repositories.d/<name>.list` lines; `executor/main.go` `apkSimulate` — append `-X <url>` for each;
- Test: `test-validate.lua` accepts/rejects; scratch E2E with a fixture repo.
- Create: `vm/repo/` fixture + `build/mkrepo.sh` (uses `vm/apk-tools-static apk index` on a dummy unsigned apk built with `tar czf fixture.apk` + `.PKGINFO`).

**Interfaces:**
- Produces: `configure_repos` action gains optional `repos` list; `e.branches` pattern extended by `e.localRepos []string` for `-X`.

- [ ] **Step 1:** Failing validate + generate assertions for a local repo entry; implement; suites green.
- [ ] **Step 2:** `build/mkrepo.sh`: stage dummy pkg tree (`.PKGINFO` minimal valid), `tar czf`, `apk index` → `vm/repo/{dummy-1.0-r0.apk,APKINDEX.tar.gz}`. Commit the SCRIPT; gitignore the artifacts.
- [ ] **Step 3:** Scratch E2E: actions with the local repos entry + installing `dummy` succeeds via `-X file://$PWD/vm/repo` (store dir gets `dummy/1.0-r0`). Lockfile path: `lua lockfile.lua … vm/repo/APKINDEX.tar.gz` as index arg already works — assert `dummy` locks.
- [ ] **Step 4:** Commit `feat: local file:// apk repos in config schema, repos.d, solver, lock`.

### Task 8: `copy_tree` IR op

**Files:**
- Modify: `executor/ops.go` (new `copyTree(act)`: `{op:"copy_tree","name":…,"src":host-abs-dir,"dst":chroot-rooted-abs}`; walks src, dirs 0755, regular files copied 0644, symlinks recreated verbatim), `executor/main.go` (dispatch + summarize), `executor/lua/generate.lua` (emit one `copy_tree` per local repo entry whose table carries `src = "<path relative to config dir>"`; rank emitted next to `configure_repos` — BEFORE it in stream order), `example/eszkoz/IR.md` (op #23 contract + ordering note).
- Test: scratch E2E: local repo entry with `src = "repo"` → `scratch/root/var/kodos/repo/APKINDEX.tar.gz` exists before `configure_repos` writes its line; `kodos exec` rerun idempotent (tree re-copied, no error).

**Interfaces:**
- Produces: IR op `copy_tree`; generate maps config `repositories.<n>.src` → action (Task 9/11 consume).

- [ ] **Step 1:** Failing scratch check (file absent); implement; rerun idempotent.
- [ ] **Step 2:** IR.md documents src/dst semantics + rank; commit `feat(executor): copy_tree IR op + generate emission for local repo src`.

### Task 9: kodos.apk build + self-package install (bootstrap proof)

**Files:**
- Create: `build/mkapk.sh` — `CGO_ENABLED=0 go build -o stage/usr/bin/kodos ./executor` + `.PKGINFO` (depends: none; provider name `kodos`) → `tar czf vm/repo/kodos-<ver>-r0.apk` → rebuild `vm/repo/APKINDEX.tar.gz` via `apk index` on BOTH apks.
- Modify: `vm/vm-layer.lua` — `repositories.kodos = {url="file:///var/kodos/repo", src="repo"}` + `packages` entry `{repo="kodos",name="kodos"}`.
- Test (paste-run, user): `sudo vm/run.sh install` → guest tree has `/var/kodos/repo`, `usr/bin/kodos` hardlinked from store, gen contains kodos+deps only via the local repo; `vm/run.sh boot` + ssh → `kodos selftest` + `kodos suitetest` PASS **inside the guest**.

**Interfaces:**
- Consumes: Tasks 7+8 machinery. Produces: `vm/repo/kodos-<ver>-r0.apk`; version string from `git describe --always`.

- [ ] **Step 1:** Write `build/mkapk.sh`; run; `vm/apk-tools-static`-index lists `kodos`.
- [ ] **Step 2:** vm-layer edits; regenerate lock (`lockfile.lua` now pins `kodos` from the local index — assert its version appears in `vm/generation-0.json`).
- [ ] **Step 3:** PASTE ROUND: install+boot; verify in-guest `kodos selftest`/`suitetest`. Fix fallout in follow-up commits.
- [ ] **Step 4:** Commit `build: kodos.apk in local repo; vm config installs kodos itself`.

### Task 10: `boot.generations_keep` — declared prune cap

**Files:**
- Modify: `executor/lua/validate.lua` (`boot.generations_keep` integer ≥1), `generate.lua` (emit as field on `create_generation` action, default 5), `executor/ops.go`/`main.go` create_generation case — prune keeps newest `keep` generations, and NEVER prunes the one `kod/generation/current` points at even if outside the cap; selftest covering: cap 2 with gens 0..4 + current=0 → gens {0,4,3} survive (store dirs pruned too).
- Test: `executor/selftest.go` — `prunePlan(gens []int, keep int, current int) []int` pure fn: sorted newest-first minus current, drop beyond cap; then fs-backed prune uses it.

- [ ] **Step 1:** Failing selftest table incl. the current-is-untouchable case; implement pure fn + wire existing prune through it.
- [ ] **Step 2:** Lua schema + generate default 5; suites green; commit `feat: boot.generations_keep prune cap (current never pruned)`.

### Task 11: `kodos apply` — config → live lock → in-place exec against `/`

**Files:**
- Create: `executor/apply.go`; `executor/main.go` (subcommand `apply [DIR|FILE.lua] [--dry-run]`).
- Modify: `executor/lua/generate.lua`/`lockfile.lua` unchanged (consume via `runLuaScript`).
- Test: Go unit tests in `apply_test.go` + scratch-rooted apply E2E + **Review Focus rows 1&2** tests.

**Interfaces:**
- Produces:
  - `resolveConfig(arg string) (entry string, dir string, err error)`: DIR → `DIR/config.lua` (missing → error naming the tried path); `.lua` file → itself; `""` → `/etc/kodos/config.lua`; error messages list the exact attempted path (Review Focus 2).
  - `fetchIndexes(repos []branchRef, localRepos []repoRef, tmp string) ([]string, error)`: https mirror/branches → `GET <mirror>/<branch>/x86_64/APKINDEX.tar.gz` via `net/http` (fail → error naming the URL; ALL fetching completes before any other side effect — Review Focus 1); `file://` → local index path; returns index paths for lockfile.lua.
  - `apply(arg string, dryRun bool) error`: resolve → `runLuaScript("generate.lua", {"--inplace", configfiles…}, <config dir>)` (config dir on the module path via Task 2's `searchDir`, spec §1) → actions JSON → parse the `configure_repos` op (mirror/branches/repos) → `fetchIndexes` → `runLuaScript("lockfile.lua", {nextGenIdx, indexes…, "--", configfiles…}, <config dir>)` → generation-N.json → copy both into `<chroot>/kod/actions/` (0600 dir) → `cmdExec(-actions … -metadata … -root / -secrets <configdir>/secrets.json if it exists)`. `--dry-run` stops after printing generate+lock outputs + plan.
- Produces: next generation number = `maxExistingGenDir(chroot)+1` (never reuses numbers; rollback reactivation of old gens does NOT bump).

- [ ] **Step 1:** Failing `apply_test.go`: resolveConfig 4 cases (dir-with-config.lua / single .lua / default path absent error / nonexistent path error text) — Review Focus 2.
- [ ] **Step 2:** Implement resolveConfig; failing `fetchIndexes` test against an `httptest` server (200 + 404: 404 error names the URL; no partial files left behind); implement.
- [ ] **Step 3:** Failing scratch-root apply E2E (chroot = scratch root, config = vm-layer variant): apply creates gen N+1 with the fixture `dummy` package added via local repo, prints plan; a SECOND apply bumps again; `--dry-run` mutates nothing (compare `ls kod/generation`).
- [ ] **Step 4:** Commit `feat(executor): kodos apply — config → live lock → in-place exec`.

### Task 12: `kodos status` and `kodos rollback [N]`

**Files:**
- Create: `executor/manage.go`; dispatch in `main.go`; Test: `manage_test.go` + scratch/VM checks — **Review Focus rows 3&4-adjacent** (4 owned by Task 6; 3 owned here).

**Interfaces:**
- Produces:
  - `status(chroot string) error`: prints `current → kod/generation/<N>` (broken link handled: says so), generation table (N, timestamp, config_hash, package count from `generation.json`), boot-default line from `boot/grub/grub.cfg` entry title.
  - `rollback(chroot string, N *int) error`: "previous" = highest gen < current (spec-pinned); target gen dir or its `generation.json` missing/corrupt → error listing readable gen list (Review Focus 3); builds synthesized action stream `[mount entries from live /etc/fstab (Task 6 skip-if-mounted makes these no-ops), set_kernel_arguments (kernel_args from generation.json), create_generation? NO — generation already exists: stream is {mount…, activate_generation(N), install_bootloader}]` and executes via the same executor path with `-metadata kod/generation/<N>/generation.json`; flips `current` via existing activate machinery; prints "reboot to boot generation N" when bootloader ran.

- [ ] **Step 1:** Failing `manage_test.go`: `rollbackTarget(current int, N *int, gens []int)` pure fn (previous-selection + missing-target error), `status` rendering on a temp kod tree with a corrupt gen dir → lists usable ones, names the corrupt one.
- [ ] **Step 2:** Implement both verbs through the executor path; scratch E2E: apply (Task 11) → rollback → `status` shows old gen current → rollback to explicit N works.
- [ ] **Step 3:** Commit `feat(executor): kodos status and rollback (with corrupt-generation diagnostics)`.

### Task 13: VM end-to-end — the definition of done (paste-run)

**Files:**
- Modify: `vm/run.sh` — helper phases: `vm/run.sh guest '<cmd>'` → ssh `-p 2222 root/abuss` runner for paste rounds; `build/mkapk.sh` rebuild step inside install phase.
- No Go/Lua code changes; findings become fix-forward commits.

- [ ] **Step 1:** Round A: `build/mkapk.sh && sudo vm/run.sh install && vm/run.sh boot` → guest has `/var/kodos/repo` + `kodos` binary (Task 9 already proved; re-verify after 10-12).
- [ ] **Step 2:** In-guest (paste): edit `/etc/kodos/config` layer to ADD `dummy` (ship repo src via copy_tree from the guest's own `/var/kodos/repo` — src path points there); `kodos apply --dry-run` then `kodos apply`; expect: new generation N+1, plan printed, no format/partition ops, no `libkmod` errors after reboot.
- [ ] **Step 3:** In-guest: `kodos status` → N+1 current; `kodos rollback` → N; reboot lands on generation N (grub title + `kodos status` after boot).
- [ ] **Step 4:** Self-upgrade once: bump kodos pkgver, rebuild apk into the guest repo dir, `kodos apply`, verify store + running binary untouched.
- [ ] **Step 5:** Update NEXT.md (self-management: DONE at commit X) + IR.md flag note; commit `docs: self-management proven on VM`.

---

## Self-review notes (author, post-write)

- Spec coverage: §1 → Tasks 1-3; §2 → 4-6, 10-12 (guards in 6, cap in 10, rollback/params in 12); §3 → 7-9; done-when → 13. Secrets default (`/etc/kodos/secrets.json`) in Task 11 apply wiring. Out-of-scope items untouched. ✓
- Type consistency: `runLuaScript`, `resolveConfig`, `fetchIndexes`, `parseMounts`, `prunePlan`, `rollbackTarget` — names fixed once each and consumed as named. ✓
- One risk called out for implementers: gopher-lua stdlib gaps get shims in Task 2 (probe first), NOT dialect changes.
