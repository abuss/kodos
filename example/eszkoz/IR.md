# IR contract — the Lua ↔ executor seam

`generate.lua` consumes the merged config + the backend selected by
`repos.base.type` and emits ONE JSON document: a flat, **strictly ordered**
action list. The compiled executor (Go/Nim/Rust — decision still open)
executes it top-to-bottom, then reports failures. No DAG, no phases field:
the order IS the contract. An executor may only parallelise actions the
ordering provably allows.

```json
{ "schema_version": 1, "distro": "alpine", "actions": [ { "op": "...", ... } ] }
```

Rules:
- Every action is idempotent: re-running the stream on the same target is a
  no-op (repos already configured, package already in store, unit already
  enabled). Repetition is how partial installs resume.
- `write_file.path` is absolute *inside the target root* the executor is
  building; the executor owns the mount point.
- Packages are bare distro names in the stream; exact versions live in
  `generation-N.json` (see "Version lock" below) — the executor must only
  install versions the metadata pins.
- Secrets never appear here: `create_user` references them by presence of
  the field (executor injects hashes out of band), and a connection `psk`
  travels only as `{secret = "dotted.key"}` — the schema rejects literal
  values, so raw secrets are inexpressible in config AND in this stream.
  Executor resolves refs from a secrets store (`--secrets FILE`: JSON map
  of dotted key → string, mode 0600), never logs values.
- Distro mechanisms are the executor's per-family mapping (the backend
  registry in `backends/*.lua` is the data model it follows): e.g.
  `enable_service` on Alpine = `rc-update add <unit> <runlevel>`;
  `configure_repos` on apk = write `/etc/apk/repositories`.

## Operations (in canonical emission order)

| # | op | fields | fed by | notes |
|---|----|--------|--------|-------|
| 1 | `configure_repos` | `type`, `mirror`, `branches[]` | `repos.base` | executor renders per-family file format |
| 2 | `partition_disk` | `device`, `table`, `efi`, `partitions[]`: `{index, name, size, type}` | `storage.devices` | skip if table already present; executor parses sizes ("512MB"/"64GB"/"100%"; "100%" = remainder minus later explicit sizes when not last) |
| 3 | `format` | `partition`, `filesystem` | partitions | skip if a filesystem exists |
| 4 | `create_swap` | `partition` | swap partitions | `mkswap` |
| 5 | `mount` | `partition`, `point`, `options` | partitions with `mountpoint` | emitted deepest-path-last so `/` mounts first |
| 6 | `write_file` | `path`, `content` | derived: `/etc/fstab`, `/etc/hostname` | generic single-file write |
| 7 | `install_packages` | `names[]` | all package sources: config (mapped), backend base, kernel variant, service plans (baseline+additive), bootloader, locale, flatpak tool | fetch into the generation store + link; deduped, sorted |
| 8 | `configure_flatpak_remote` | `name`, `url` | `repos.flatpak` | `flatpak remote-add --if-not-exists` |
| 9 | `install_flatpak` | `ids[]`, `user?` | `{repo="flatpak"}` tags; system scope, or one action per user (`user` = login) when tagged inside that user's `environment` | flatpak tool comes from backend `flatpak_packages` via #7; per-user installs the executor defers to first login (needs the user session) |
| 10 | `configure_locale` | `default` | `system.locale` | Alpine: env.d + rc.conf unicode (backend facts) |
| 11 | `configure_keyboard` | `layout`, `console_keymap` | `system` | setup-keymap equivalent |
| 12 | `configure_timezone` | `timezone` | `system.timezone` | |
| 13 | `load_kernel_modules` | `modules[]` | `boot.kernel.modules` | Alpine: `/etc/modules` + mkinitfs |
| 14 | `set_kernel_arguments` | `arguments[]` | `boot.kernel.arguments` | bootloader cmdline |
| 15 | `configure_service` | `name`, `settings{}`, `settings_file` | service entries with settings (e.g. ssh) | settings = the config keys on the service entry |
| 16 | `configure_network` | `connections[]`: `{service, name, type, ssid?, psk?={secret}}` | `services.*.connections` (enabled services only) | executor renders NetworkManager keyfiles (same format on all 3 families); `psk` = secret REFERENCE, resolved from the secrets store, written only into the 0600 keyfile |
| 17 | `create_generation` | `packages[]`, `root`, `keep` | `generations` + resolved set + `generation-N.json` metadata | cf. `create_generation`/`seed_etc` in install-apks.py; exact versions per metadata |
| 18 | `activate_generation` | `root` | `generations.root` | flip symlink + prune to `keep`; rollback = re-run with older metadata. **Everything from #19 on runs inside the activated chroot** — busybox and `/etc/init.d` scripts only exist after activation |
| 19 | `create_user` | `user`, `name`, `group`, `supplementary_groups[]`, `home_directory`, `shell`, `create_home` | `users{}` (key = login; `name` = real-name/gecos) | password: if `users.<login>.hashed_password` is in the secrets store, executor sets it (out-of-band like all secrets) |
| 20 | `enable_service` | `name`, `unit`, `runlevel` | service plan, `unit` ≠ nil | packages-only entries (backend `service_name = false`) produce no action |
| 21 | `enable_home_service` | `user`, `name`, `mechanism`, `packages[]`, `settings{}` | `users{}.environment.services` | OpenRC has no user services; backend picks `mechanism` (TODO: user-daemon runner); executor defers to first login |
| 22 | `install_bootloader` | `type`, `efi`, `timeout` | `boot.bootloader` | packages already installed at #7 |
| 23 | `copy_tree` | `name`, `src` (absolute host dir), `dst` (target-rooted absolute path) | `repositories.<n>.src` (local repo entries) | copies src INTO chroot-mapped dst: dirs 0755, files 0644, symlinks verbatim; idempotent (overwrite); **must precede `configure_repos`** so the repo tree exists before apk resolves it |

A variant the backend cannot offer (kernel variant, bootloader type) makes
the generator **fail, not fall back** — same rule as the checker. Flatpak
app ids without a declared `repos.flatpak` likewise abort generation.

## Version lock & `generation-N.json`

`lockfile.lua` resolves the SAME install set as `install_packages` against
real distro indexes (apk index text or `.tar.gz`) and emits the generation
metadata JSON — EVERY package pinned to exact version + checksum: the
config-declared requests PLUS their full dependency closure, locked by
running the repo's static apk-tools (`KODOS_APK=/path`, default
`../../vm/apk-tools-static`) in `add --simulate` mode against those exact
snapshots served as `file://` repos:

    lua lockfile.lua <generation> main.apkindex community.apkindex \
        -- system.lua layers/machine.lua layers/user.lua > generation-0.json

```json
{
  "schema_version": 1, "kind": "kodos-generation", "generation": 0,
  "distro": "alpine", "kernel_variant": "default",
  "kernel": "linux-lts-6.18.63-r0",
  "packages": { "clang20": { "version": "20.1.8-r3", "checksum": "Q1I//..." }, ... },
  "flatpak": { "system": [], "users": [] },
  "locked_count": 978, "requested_count": 99
}
```

Executor contract:
- the `install_packages` names are the config-declared requests; the
  `generation-N.json` `packages` keys are their full resolved closure
  (a superset; both computed from `resolve.install_set` + apk). The
  executor re-resolves live and ENFORCES the lock: any pinned version
  resolving differently is a hard lock-drift error; a dependency the
  live repos resolve but the lock does not contain (repo changed after
  locking) is reported loudly. A locked install is reproducible or
  fails visibly — nothing floats.
- `timestamp` / `config_hash` are deliberately absent from the Lua output
  so it is byte-deterministic per index snapshot; the executor stamps them
  when it materializes a generation (per the GENERATION_STORAGE_SUMMARY
  sketch).
- rollback = activate an older generation whose metadata already pins the
  old versions; nothing to re-resolve.
- flatpak ids stay floating (Flathub is the authority offline indexes
  cannot pin); online flathub locking is future work.

## `--inplace` flag

Pass `--inplace` to `generate.lua` when re-applying config to an already-installed machine. The flag is stripped before config-file parsing and causes the emitted stream to omit every action whose `op` is `partition_disk`, `format`, or `create_swap`; all other actions are emitted byte-identically to the no-flag output. This is a generator flag, not a config schema field. The executor also guards against storage ops on live mounts as defense-in-depth.

## `build_package` — source-built packages (designed, NOT yet emitted)

Closes the AUR-shaped hole, distro-neutrally (Arch aport / Alpine APKBUILD /
bare git are the same shape). Config: a `builds = { name = { version,
source = {type="git", url, rev}, build_deps = base(...),
steps = {build={...}, install={...}} } }` section; `rev` is REQUIRED
(it is the pin; no rev = hard error).

- IR op (ordered after `install_packages`, before `create_generation`):
  `{ "op": "build_package", "name", "version", "source": {type,url,rev},
     "build_deps": [...locked distro names], "steps": {"build":[...],
     "install":[...]} }` — `{out}` placeholder = the store dir.
- `build_deps` join `install_set`/lock like any package.
- Metadata gains `built: { name: {version, source, artifact_checksum} }`.
- Contract: build once into `store/<name>/<version>` (idempotent: existing
  artifact + matching checksum = no-op); rollback restores the ARTIFACT,
  never rebuilds implicitly; toolchain drift therefore cannot silently
  change a built package. Generation ops treat built packages exactly like
  apk ones — the store layout already fits.
