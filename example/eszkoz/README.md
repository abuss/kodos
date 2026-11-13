# eszkoz — declarative system config (example), distro-neutral

Proposed declarative config for the machine *eszkoz*, kodos-alpine dialect:
Lua describes the system and generates commands; execution is the compiled
executor's job. **The config is distro-neutral** — the distro is declared
once (`repos.base.type = "apk"`) and everything distro-specific lives in
the backend (`backends/alpine.lua`), which is the spec for the generator's
backend registry. Portability verdict + next steps: [PORTABILITY.md](PORTABILITY.md).

## Two equivalent forms (both must stay supported)

| Form | Files |
|---|---|
| Monolithic | `configuration.lua` (single file, self-contained helpers) |
| Modular | `system.lua` + `home.lua` + `modules/` + `packages/` + `kodos.lua` |
| Backend | `backends/alpine.lua` (picked by `repos.base.type`) |

    lua test-equivalence.lua                              # PASS: forms identical
    lua test-merge.lua                                    # PASS: layer semantics
    lua test-validate.lua                                 # PASS: schema catches typos
    lua test-generate.lua                                 # PASS: IR contract
    lua test-lock.lua                                     # PASS: version lock (needs apkindex, see file header)
    lua check-packages.lua main.pkgs community.pkgs       # PASS: all names resolve

`check-packages.lua` walks the config, resolves each canonical package name
through the backend map (string=rewrite, false=drop-with-warning, absent=
identity), and verifies every resulting name — plus all backend-supplied
packages — against real Alpine v3.24 APKINDEX lists. One or more config
files after the indexes are merged as layers first, so layered configs are
validated too:

    lua check-packages.lua main.pkgs community.pkgs \
        system.lua layers/machine.lua layers/user.lua

## Trust model (config is code, and that is deliberate)

Config Lua is **trusted code** (Guix/Nix precedent): `dofile`/`require` run
with full privileges; whoever can write the config tree is the admin. The
boundaries that make this safe to build on:
- **Code stops at the processor**: the executor only consumes
  `actions.json` + `generation-N.json` — pure data, no Lua, ever.
- **Schema**: computed results must fit the dialect before generation.
- **Determinism tests**: config that uses `os.date`/`math.random`/network/
  subprocess output fails byte-diff CI immediately — output is policed,
  not source.
- A Lua `_ENV` sandbox was rejected as fake security (`require("os")`
  escapes it); revisit only if configs ever come from untrusted sources.
  The dotfiles `repo_url` clone is the one untrusted input and lives
  outside the system-generation path (user space).

## Layering + generation (implemented)

`resolve.lua` is the shared config processor (merge + backend resolution;
`is_array`/`deep_merge` are a local copy of proc-lua-conf `demo.lua`,
semantics kept: arrays concatenate, dicts overlay recursively, later layer
wins). Identity-carrying sections (`services`, `users`) are dicts keyed by
name, so layers merge them natively. `layers/machine.lua` and
`layers/user.lua` demo the semantics; `generate.lua` turns merged config +
backend into the IR action stream (JSON, contract in [IR.md](IR.md)) that
the compiled executor will consume:

    lua generate.lua system.lua layers/machine.lua layers/user.lua > actions.json

`lockfile.lua` pins the same install set to exact versions + checksums
from real index snapshots and emits `generation-N.json` (contract +
recipe in [IR.md](IR.md)); the executor must install exactly those.

## Dialect notes

- **Packages**: tagged lists `{ repo="base", name=... }` with canonical
  names (proc-lua-conf `src/extract.lua` shape). Backend maps e.g.
  `clang→clang20`, `ninja→ninja-build`, `node→nodejs`, `man→mandoc`,
  `lua→lua5.4`; unavailable names (`false`) are dropped with a warning.
- **Services**: plain data keyed by neutral name, `enable = true` required
  (NixOS-style; `enable = false` disables from a layer) — e.g.
  `ssh = { enable = true, permit_root_login = false }`. No helpers, no
  service-type strings. Backend-supplied packages are the baseline; a
  service's own `packages = base(...)` is an additive overlay (canonical
  names, mapped/dropped by the backend). The backend registry is keyed by the
  same name and supplies packages, unit names, runlevels (incl. the Alpine
  `-openrc` subpackage quirk) and settings file paths. **Escape hatch:** a
  name with no backend entry is valid only if the config provides
  `packages`; the unit to enable is the service key (`service_name`
  overrides, `false` = packages only) — portability is then the user's
  problem, by design.
- **Boot**: `bootloader.type` + kernel modules/arguments are neutral;
  backend supplies kernel + bootloader packages (`linux-lts`, `grub`).
- **Extra repos / Flatpak**: `repos.flatpak = { type = "flatpak", name =
  "flathub", url = ... }` plus the `flatpak("org.gimp.GIMP")` helper (same
  tagged shape as `base(...)`, repo `flatpak`). App ids are Flathub-native
  (no backend map); the backend supplies the `flatpak` tool package. Tag
  inside a user's `environment` for per-user install scope. ids without
  `repos.flatpak` = hard error; checker validates reverse-DNS shape
  (Flathub existence = future online CI).
- **Home services**: neutral record; backend decides mechanism
  (`syncthing@abuss` is a systemd-ism; OpenRC user-daemon mapping TODO).
- **Networking**: connection profiles live on the service entry —
  `services.networkmanager.connections = { home = { type = "wifi",
  ssid = "...", psk = { secret = "wifi.home.psk" } } }`. Profiles are a
  dict (layers merge by name), wifi REQUIRES a psk secret reference, and
  a literal psk string is a schema error — raw secrets are inexpressible
  in config and never reach the IR. Executor renders NetworkManager
  keyfiles (identical format on all 3 families). Static IPs TODO.
- **Boot/storage**: grub EFI; ext4 only — generations use the
  generation-apks store model (`/kod/store`, symlinked generations,
  etc/etc_default/etc_store), so no filesystem feature is required.
- **Locale**: musl limits locale support (backend concern), kept minimal.
- **Secrets**: raw values are inexpressible in config. Only references:
  connection `psk = { secret = "dotted.key" }`, user passwords by field
  presence. The executor resolves refs from a mode-0600 store
  (`--secrets FILE`, dotted-key JSON map) — never committed, never in IR.
- **Not listed** (unavailable on Alpine v3.24): AUR sets, vivaldi, brave,
  megasync, rslsync, ghostty, glow, tldr, duf, ipp-usb.
