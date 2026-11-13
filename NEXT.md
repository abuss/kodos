# Alpine Kodos — where we are, what is next

Snapshot 2026-11-11. Review of the config schema work (`example/eszkoz/`,
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
5. **No executor**, and the language choice (Go/Nim/Rust) is still open.
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

## Ranked next steps

| # | Step | Why this order |
|---|---|---|
| 0 | ~~`git init` + commit example, docs, backends~~ | **DONE 2026-11-12** (baseline `fcbfe3a`) |
| 1 | ~~Config layering + merge~~ | **DONE 2026-11-11** |
| 2 | ~~Action/IR contract + reference Lua generator~~ | **DONE 2026-11-11** |
| 3 | ~~Port schema/validator~~ | **DONE 2026-11-12** (`validate.lua`, `test-validate.lua`; wired into generator + checker) |
| 4 | ~~Version locking + generation metadata~~ | **DONE 2026-11-12** (`lockfile.lua`, `test-lock.lua`, IR.md contract) |
| 5 | **Executor design + spike**: consume `actions.json` (IR.md); port `create_generation`/`seed_etc`/`activate_generation`/`chroot_resolve` from `install-apks.py`; decide Go/Nim/Rust then spike chroot+apk-store against a qcow2 VM | the only genuinely new code; the IR gives it a real, tested input contract today |
| 6 | Schema-gap backlog above (networking **done** 2026-11-12; display manager next) | fill gaps as real needs, not upfront |
| 7 | Drift CI: re-fetch the 3 indexes + re-run all backends per Alpine release / weekly | `clang19→clang20` proved the failure mode is real |

Suggested entry point: **5** — the executor spike now has everything it
needs as input: schema-validated config → `actions.json` (IR.md) +
`generation-N.json` (pinned versions). Remaining non-executor work in the
meantime: step 6 gaps (display manager, mounts, secrets resolver lands
inside the executor) and step 7 drift CI.
