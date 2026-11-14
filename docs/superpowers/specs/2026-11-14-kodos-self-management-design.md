# Kodos self-management — design

Date: 2026-11-14
Status: APPROVED — sections 1-3 in chat; gopher-lua kept as the 5.1-dialect
decision after the version-support research (2026-11-14)

## Problem

Today kodos is an out-of-band *installer*: the declarative Lua config and the
whole toolchain (generator, lockfile, executor) live in the host repo, and
change happens by running `vm/run.sh install` (or a future live-USB flow)
against a mounted target root. Once the system is installed — VM or bare
metal — **there is no host**. The machine must manage itself: add/remove
packages, reconfigure, roll back, with no external machine involved.

This is the primary way users interact with kodos, not an extra.

## Brief (agreed)

On the booted system, the user edits the declarative Lua config on disk and
runs one command (`kodos apply`) to converge the system; `kodos rollback`
reverts to a previous generation; reboot only when the kernel/bootloader leg
changed. The old host-driven flow survives only as bootstrap/rescue.

Success (definition of done, VM proof): install round ships `kodos`
in-package-closure; then entirely in-guest — edit config → `kodos apply` →
new package live in generation N+1 → `kodos rollback` → reboot lands on the
previous generation → `kodos status` shows the whole story.

## Decisions already banked

- Toolchain distribution: kodos **eats its own dogfood** — a real `kodos`
  apk in a local repo, declared like any package. No installer injection.
- The Go binary embeds the Lua toolchain (`go:embed`) and runs it with
  **gopher-lua**; distribution is ONE static binary. Lua dialect policy:
  config + toolchain Lua is written to the **5.1 dialect (+ `goto`, which
  gopher-lua supports)** — our sources are already 5.1-clean by scan.
  Escape hatches for a future modern-Lua requirement (5.3 go-lua forks,
  or cgo + Lua 5.4 C at the cost of pure-Go cross-builds) are explicit
  trade-offs to take *then*, not now. Shopify/go-lua was considered and
  rejected: its own README lists missing core stdlib (coroutines, string
  patterns).
- Config discovery: explicit DIR (contains `config.lua`) or explicit FILE.lua;
  default `/etc/kodos/config.lua`.
- Live apply never formats (omitted from stream + executor hard guard).
- Generation retention: declarative cap (default 5).
- `kodos rollback [N]`; no argument = previous generation.

## §1 — One binary: shape, verbs, config resolution

**Binary.** Single static Go binary `kodos`. The current executor becomes the
`exec` subcommand (identical flags/behavior); `vm/run.sh` switches to
`kodos exec`. The vendored `kodos-exec` main becomes a subcommand file.

**Embedded Lua.** All toolchain modules — `resolve.lua`, `validate.lua`,
`generate.lua`, `lockfile.lua`, `json.lua`, `kodos.lua`, `system.lua`
module-parts, `backends/`, `modules/` — are embedded with `go:embed` and run
on gopher-lua (yuin/gopher-lua, pure Go, keeps the binary static).
Compatibility facts established by scan: our Lua is 5.1-syntax-clean (host
lua is actually 5.5.1; code never used 5.2+ features); stdlib surface is
`io.open/popen/stderr`, `os.execute/exit/getenv/remove/time`. `io.popen` is
the only uncertain gopher-lua surface — if absent, a small Go-registered shim
(a few lines around `os/exec`) closes the gap.
**Compatibility gate:** the five existing Lua suites (`test-generate`,
`test-validate`, `test-lock`, `test-merge`, `test-equivalence`) run inside
`kodos selftest` through the embedded VM — the compat surface is enforced in
CI forever.

**Verbs (public surface — deliberately minimal).**

| Verb | Effect |
|---|---|
| `kodos apply [DIR\|FILE.lua] [--dry-run]` | Full on-machine converge: load config → live lock → generate (in-place mode) → `exec -root /` → activate N+1. `--dry-run` prints the plan, changes nothing. |
| `kodos status` | Current generation (`current` alias), boot default entry, generation list with package counts. |
| `kodos rollback [N]` | Re-activate generation N (default: previous). Flips `current`, moves the grub default. Does NOT re-run post-install hooks (they ran when N was first created; Alpine hooks are not idempotent — amended 2026-11-14 after Task 12). |
| `kodos exec ...` | Today's executor, unchanged; dev/harness + internal plumbing. |

No `edit`, no `diff`, no confirmation prompt: you edited the config, you ran
apply — the config IS the confirmation.

**Config resolution.** `apply` argument: directory → `<dir>/config.lua`;
plain `.lua` file → that single file (e.g. `configuration.lua`); absent →
`/etc/kodos/config.lua`. The config file's own directory is added to the
embedded Lua module search path, so config.lua may `require("layers.machine")`
-style relative pieces exactly like the repo's `system.lua + layers/` pattern.
The kodos modules themselves always come from the embed.

**Secrets.** `secrets.json` beside the config (default
`/etc/kodos/secrets.json`, mode 0600, required if referenced), same
dotted-key contract as the host flow. Raw secrets stay inexpressible in the
config itself (existing requirement).

## §2 — The apply pipeline (live machine)

1. **Load & validate** the config through embedded Lua (validator = today's
   `validate.lua`).
2. **Lock, live.** For each configured repo: `https://…` → fetch
   `<repo>/<branch>/x86_64/APKINDEX.tar.gz` with embedded `net/http` (no curl
   dependency on the machine); `file://…` → used as-is (lockfile.lua already
   consumes `.tar.gz` indexes and serves them to the solver this session
   proved). The closure lock runs the SAME solver path; on the machine the
   apk binary is the system `apk` (every Alpine system has it — no
   host-distro assumption, and no vendored `apk-tools-static` needed).
   Index snapshots land in a temp dir; lock stays deterministic per snapshot.
3. **Generate (in-place mode).** `generate.lua` gains an `inplace` flag:
   omits `partition_disk`, `format`, and swap-create; emits everything else —
   repos, `copy_tree` targets (§3), packages, files, users,
   services, fstab, `create_generation`, `activate_generation`, bootloader,
   users/services reconciliation. The host bootstrap stream keeps the full
   storage leg.
4. **Guards regardless of stream.** Executor hard-refuses `format_disk` and
   `format` when the target filesystem/device is already mounted. `mount` ops
   become skip-if-already-mounted (general hardening; host mode benefits).
5. **Exec `-root /`.** The executor is chroot-rooted by construction;
   chrooting into `/` is a no-op. Post-install hooks re-run for the new
   generation (existing behavior), initramfs clean/deref runs, bootloader leg
   re-runs (grub-install + ESP copy proven idempotent while live), runlevel
   reconciliation sweeps stale enables.
6. **Generations.** Apply = next number = max(existing)+1; metadata
   generation-N.json stored with the generation (timestamp/config_hash
   stamped, as today). **Cap:** `boot.generations_keep = 5` default in the
   schema; executor prune becomes config-driven; the generation `current`
   points at is never pruned regardless of cap.
7. **Rollback.** `kodos rollback [N]`: "previous" = highest generation
   strictly below the current one. Load that generation's stored metadata → `exec` a minimal stream (mounts from live fstab + activate + bootloader
   default) → `current` flips → reboot lands there. Post-install hooks are
   NOT re-run (they already ran for that generation; re-running them after
   activation would abort mid-boot-config with `current` already flipped).
   Live /etc flips because
   activation re-hardlinks (the fs-agnostic etc model already does this at
   activation).
8. **Service lifecycle stays honest.** apply changes *state*; running
   services do not auto-restart. Reboot when the kernel/bootloader leg
   changed (apply prints that hint when it touched it); otherwise
   `rc-service restart` as needed. Auto-restart orchestration is explicitly
   out of scope.

## §3 — Distribution: how kodos.apk reaches the machine

**Chicken-and-egg answer:** the FIRST (host-run bootstrap) install round
ships the local repo onto the disk; after that the machine is self-sufficient.

- **Schema:** the config `repositories` section accepts local entries, e.g.
  `repositories = { kodos = { url = "file:///var/kodos/repo" } }`. The
  backend writes the matching `/etc/apk/repositories.d/` line, and
  lock/simulate pass `-X file://…` (proven this session).
- **New IR op `copy_tree`:** copies a directory tree (repo-local source →
  absolute target) as part of an action stream; used by bootstrap to place
  `/var/kodos/repo/{kodos.apk,APKINDEX.tar.gz}`. (General-purpose: also fine
  for future drop-in trees.)
- **Declaring kodos on itself:** `packages = { { repo = "kodos", name =
  "kodos" } }` — an ordinary locked package: versioned, closure-resolved,
  prunable, rollback-able. Self-upgrade = place new apk in the repo dir +
  `kodos apply`. `/usr` switchover means the running (old) binary keeps its
  inode; no self-overwrite hazard.
- **Building kodos.apk (dev side, this repo):** a small build script —
  `CGO_ENABLED=0 go build` → tar.gz tree with `.PKGINFO` → `apk index` for
  APKINDEX.tar.gz → output into a repo dir the install config points at.
  Unsigned: the trust boundary is the root-owned repo path; detached signing
  is future work.

## Schema / IR deltas (summary)

- `repositories.<name> = { url = "file://…" }` — local repo entries.
- `boot.generations_keep = N` (default 5) — prune cap.
- IR op `copy_tree { src, dst }` (rank with the file/config ops).
- Executor guards: no format on mounted targets; mount skip-if-mounted.
- `generate.lua inplace` mode (flag, not a schema concept — `apply` sets it).

## Error handling

- Config/lock errors (unknown package, schema violation, index unfetchable)
  abort BEFORE any mutation — same as today's lock-first discipline.
- A failed apply leaves the previous generation booted and live (activation
  is the last step; partial streams never sweep runlevels — existing rule).
- Rollback to a generation whose metadata is missing/corrupt = hard error
  with the generation list (`status` output).
- Prune never removes the generation `current` points at, even with cap 1.

## Testing

- `kodos selftest` keeps all current checks, PLUS: the five Lua suites run
  through the embedded gopher-lua VM (compat gate).
- New executor selftests: format-refuses-when-mounted, mount-skip-if-mounted,
  prune-respects-generations_keep-and-current.
- `test-lock`/`test-generate` extended for local `file://` repo entries and
  inplace-mode action streams (no format/partition ops emitted).
- VM E2E (the definition of done): bootstrap round installs kodos from
  `vm/repo/` (local repo in-repo fixture) → ssh into the guest → edit a
  config layer + `kodos apply` → package present, generation incremented →
  `kodos rollback` → reboot → `status` shows the older current →
  self-upgrade path exercised by bumping the fixture apk version once.

## Out of scope / future work

- Service auto-restart orchestration after apply.
- Apk signing/keys for local repos.
- `kodos edit`/`diff` conveniences.
- Arch/deb backends (lockfile repos are apk-only for now).
- Network/VPN bring-up edge handling during apply (assumes working repos
  reach; offline apply from cached index snapshots is a later feature).
