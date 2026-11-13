# Config schema portability — assessment

**Verdict: the declarative config schema in this example is generic enough
to drive Arch and Debian unchanged.** Verified with real package indexes
(Alpine v3.24 dump, Arch current core+extra sync DB, Debian trixie
Packages index), not guesses.

## Evidence

Of the 66 canonical package names used by `eszkoz` (indexes re-fetched
2026-11):

| Target | Direct hits | Map entries | Dropped |
|---|---|---|---|
| Alpine v3.24 | 59/66 | 7 | 0 |
| Arch (rolling) | 54/66 | 11 | 0 |
| Debian trixie (main+contrib+n-f) | 39/66 | 17 | 10 (helix, zig, nim, zellij, yazi, wezterm, tailscale, …) |

Debian renames the most (`network-manager`, `golang`, `fd-find`,
`openssh-server`, `linux-image-amd64`) — none of that quirkiness ever
reaches the config; it is backend data.

## Per-distro cost (measured)

| Layer | Change |
|---|---|
| Config | **one line**: `repos.base = { type = "arch" \| "deb" + mirror/suite }` |
| Backend | one new file, ~40–60 lines (see contract below) |
| Checker | one entry in the backend registry map |

## What the config may NOT contain (invariants)

Service names/packages/runlevels, unit names, kernel/bootloader package
names, distro package spellings, locale availability lists. All of it is
backend-owned. The distro appears exactly once: `repos.base.type`.

## Backend contract (what `backends/<distro>.lua` must supply)

Proven by alpine.lua; arch/deb must cover the same surface:

1. `repo_types` — which `repos.base.type` values it answers
2. repo setup — how to write the distro's repository config
3. `base_packages`, `kernel_packages[variant]` (keyed by neutral variant
   names — `default`, `lts`, `hardened`, `zen`, `rt`, `cloud`; the config's
   `boot.kernel.variant` selects one, unknown variant = hard error),
   `bootloader_packages[type]`
4. `map` — canonical→distro name (`false` = unavailable, drop with warning)
5. `services[name]` — baseline packages incl. init-script quirks (Alpine
   `-openrc`), unit name, enable mechanism (`rc-update` vs `systemctl`),
   settings files. A service's own `packages` in the config is an additive
   overlay on this baseline (canonical names run through `map`/drop).
   **Escape hatch:** names absent from the registry are valid if the config
   supplies `packages`; enable name = service key (`service_name` override,
   `false` = packages-only).
6. `home_services[name]` — mechanism (systemd user units vs OpenRC gap)
7. locale surface — `locales_supported`, mechanism (musl-locales vs
   `locale-gen`), extra knobs (Alpine: `rc.conf unicode="YES"`)

## Known gaps found along the way

- Canonical font names are `font-*` (Alpine-flavored); should become
  prefix-free (`fira-code`) so Arch/Debian map to `ttf-*`/`fonts-*`.
- Users section is still an array; services are now a keyed dict — merge
  semantics differ between sections; pick one story per section type.
- systemd-style ordering (`after`/`wanted_by`) is not modeled; needed only
  when a service grows dependencies (OpenRC: init-script headers).
- `schema.lua`/`validator.lua` (nkodos) still describe the old dialect —
  they need a local copy updated for: keyed services + `enable`,
  `generations` section, repos variants, locale/service validation.
- Secrets (password hashes) have no injection mechanism yet.

## What could be next

1. Rename canonical fonts; add a font map to all backends (small, today).
2. **Done: all three backends** (alpine/arch/deb). Portability CI runs today:
   same config + one `repos.base.type` line per distro, checked against the
   real indexes — alpine 89 names/0 missing, arch 76/0 (1 drop),
   deb 70/0 (10 drops). Note: Debian trixie drops `helix`, which eszkoz's
   home layer actually uses — a real portability cost, surfaced honestly.
3. Define the **command/action IR contract** (the Lua→executor interface):
   the generator emits store/generation/package/service/file actions as
   serialized records (the old `{action, action_type}` idea, extended with
   `phase`, `depends_on`, `stdin`); the compiled executor (Go/Nim/Rust)
   consumes them. This is the next design artifact worth effort.
4. Port schema/validator (local copy) to validate this dialect — the
   checker's rules become the schema.
5. Design the executor's generation operations from `install-apks.py`
   (`create_generation`, `seed_etc`, `activate_generation`, `chroot_resolve`)
   plus bootloader entry writing — the only genuinely new code.
6. Secrets injection (e.g. `secrets = {...}` referencing external files).
