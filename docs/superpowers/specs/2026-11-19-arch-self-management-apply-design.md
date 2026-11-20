# Arch self-management apply design

## Status

Approved in chat on 2026-11-19 for written specification. This document is the
spec review artifact before implementation planning.

## Problem

`kodos apply` on an Arch target fails because the apply path is Alpine-specific:

```text
ERROR: fetch index https://mirror.rackspace.com/archlinux/core/x86_64/APKINDEX.tar.gz
```

Arch publishes ALPM sync databases such as `core.db` and `extra.db`, not Alpine
`APKINDEX.tar.gz`. The installed target also lacks a usable CA bundle because the
Arch `base` group does not include `ca-certificates`, which makes the TLS fetch
fail before the wrong URL can be reported clearly.

The current Arch media deliberately supports install and rollback only. This
spec adds the missing day-2 `apply` path while keeping the existing Alpine
self-management path unchanged.

## Goals

- Make `kodos apply` work on Arch targets configured with `repos.base.type = "arch"`.
- Keep the existing Alpine `apply` code path behavior-compatible.
- Reuse the existing Arch `lockfile.lua` ALPM lock mode and Arch package fetcher.
- Fetch current package metadata from the configured Arch mirror.
- Fail with clear errors that name the exact URL, repo, and configuration field.
- Add the target-side TLS prerequisite (`ca-certificates`) to Arch generations.

## Non-goals

- No GPG verification of Arch sync databases on the target in this step.
- No pacman or `archlinux-keyring` on the Arch target apply path.
- No change to Alpine install media, Alpine apply, or Alpine package behavior.
- No new multi-architecture support; this milestone remains x86_64.
- No day-2 upgrade path for an already-installed Arch target that lacks CA
  certificates; those targets need reinstall or manual CA bootstrap.

## Current Alpine Apply Shape

`kodos apply` already does this:

1. Resolve `/etc/kodos/config.lua` or an explicit config path.
2. Run `generate.lua --inplace` to build the in-place action stream.
3. Read `mirror`, `branches`, and local repo declarations from the generated
   `configure_repos` action.
4. Fetch all live indexes before any target mutation.
5. Lock the package closure with `lockfile.lua`.
6. Run `kodos exec` against the live root.

Alpine currently uses live HTTPS index fetching with no apk/GPG verification.
The Arch design should be structurally the same, with ALPM DB fetching replacing
APKINDEX fetching.

## Recommended Architecture

Add a repository-type branch to the index-fetch phase of `apply`.

```text
configure_repos.type
  apk  -> existing fetchIndexes()
  arch -> new fetchArchIndexes()
```

`generate.lua` already emits `configure_repos.type`. `apply.go` must capture that
field and choose the fetcher instead of assuming Alpine.

## Arch Index Fetch

For each configured Arch branch (`core`, `extra` by default), fetch the DB tar:

```text
<mirror>/<branch>/os/x86_64/<branch>.db.tar.gz
```

If that URL returns 404, fall back to:

```text
<mirror>/<branch>/os/x86_64/<branch>.db
```

Extract every regular member matching:

```text
*/desc
```

and write their concatenated contents, in tar order, to:

```text
<kodos-apply-temp>/<branch>.desc
```

The temp desc paths are then passed to the existing `lockfile.lua` ALPM mode:

```sh
kodos lua lockfile.lua <generation> core.desc extra.desc -- <config.lua>
```

No `apk`, `apk-tools`, or pacman is invoked. `lockfile.lua` already understands
ALPM `desc` text and resolves the closure internally.

## Trust Model

The trust model is intentionally analogous to current Alpine `apply`:

- The live index is fetched over HTTPS from the configured mirror.
- The index is parsed into exact version plus SHA256 pins.
- Every package tarball downloaded by the Arch fetcher is verified against its
  pinned SHA256 before it enters the store.
- A pinned file missing from the rolling mirror already falls back to
  `archive.archlinux.org`, with the same SHA256 pin.

The fetched DB itself is not GPG-verified in this milestone. That is the same
trust posture as Alpine `apply`, which fetches live APKINDEX files over HTTPS
without verifying apk signatures on the target.

## CA Certificate Prerequisite

Arch `base` does not include a CA bundle. Arch targets need:

- `ca-certificates`
- its dependency `ca-certificates-mozilla`

The cleanest place to add this is the Arch backend base package set:

```lua
M.base_packages = { "base", "tzdata", "ca-certificates" }
```

This makes every Arch generation carry the system trust store. Existing Arch
targets without CA certificates cannot self-update into that package over TLS;
they require reinstall from fresh Arch media or manual CA bootstrap.

## Apply Flow Changes

`executor/apply.go` changes only around the index-fetch boundary:

1. Capture `repoType` from the generated `configure_repos` action.
2. Keep the current Alpine fetch path unchanged when `repoType == "apk"`.
3. When `repoType == "arch"`, call `fetchArchIndexes(...)`.
4. Pass the resulting desc paths to the existing `lockfile.lua` invocation.
5. Continue to write actions/metadata and execute only after all fetching and
   locking have succeeded.

`fetchArchIndexes` should use the existing HTTP timeout discipline and return
errors that include:

- the repository branch,
- the exact attempted URL,
- the HTTP status or transport error,
- the path used for the extracted desc when extraction fails.

A helper should translate Go x509 "unknown authority" failures into a message
that names the likely missing `ca-certificates` package.

## Package Fetch Behavior

The Arch package fetcher already pins version and checksum from the lockfile.
For apply, those pins now come from the freshly fetched live DB. The existing
fetch behavior remains:

- Try the configured mirror first.
- Fall back to `archive.archlinux.org` for pinned tarballs the mirror has
  already dropped.
- Verify SHA256 before storing any package.
- Fail closed on checksum mismatch.

## Error Handling

- If `configure_repos.type` is missing or unsupported, fail before any fetch:
  `apply: unsupported repo type <type>`.
- If an Arch config declares local repositories, fail before any fetch:
  `apply: local repositories are not supported for Arch in this milestone`.
- If an Arch DB URL is missing, fail with the URL and branch name.
- If a DB tarball does not contain a `desc` member, fail with the URL and member
  pattern.
- If TLS certificate verification fails, include a CA-bundle hint naming
  `ca-certificates`.
- If no generation can be created because lockfile.lua fails, do not mutate the
  target.

## Testing Plan

### Unit Tests

Add `apply_arch_test.go`:

- Capture the generated `configure_repos.type` path through `apply` or expose a
  small helper for the repo-type decision.
- Start an httptest server that serves valid `core.db.tar.gz` and
  `extra.db.tar.gz` files containing `*/desc` members.
- Call the Arch index fetcher.
- Assert it returns two desc paths and that their contents contain the expected
  package stanzas.
- Assert failures name exact URLs for HTTP 404 and malformed tarballs.

### Lua Tests

Update `executor/lua/test-generate.lua`:

- Assert the Arch media IR requests `ca-certificates`.
- Keep the existing assertion that Arch media does not use Alpine APKINDEX.

### Host Media Tests

Update `build/usb/test-arch-media.sh`:

- Assert the staged Arch config or generated Arch IR includes `ca-certificates`.
- Continue asserting Arch media has no APKINDEX path.

### VM Integration Round

After implementation, use an Arch VM round to verify:

1. Boot the installed Arch generation.
2. Confirm `ls -l /etc/ssl/certs/ca-certificates.crt` exists.
3. Edit the installed config in a way that changes only system configuration or
   adds one small Arch package.
4. Run `kodos apply --dry-run` and verify it fetches Arch DBs, locks the
   closure, and prints a new generation plan without mutating the target.
5. Run real `kodos apply`.
6. Verify the new generation exists and becomes current.
7. Verify `kodos rollback` restores the prior generation.

## Compatibility

Alpine compatibility:

- `fetchIndexes()` remains the path for `repos.base.type = "apk"`.
- The Alpine URL shape and extracted `APKINDEX` filename remain unchanged.
- Existing Alpine apply, rollback, and self-update rounds are unaffected.

Arch compatibility:

- Existing rollback works without apply.
- Fresh installs from the updated media get CA certificates and can apply.
- Existing installs without CA certificates require reinstall or manual CA
  bootstrap before apply can use HTTPS.

## Acceptance Criteria

- `kodos apply` on a fresh Arch target creates a new generation instead of
  fetching Alpine APKINDEX URLs.
- The Arch apply path fetches live `core` and `extra` DBs and locks against their
  `desc` members.
- Package tarballs are verified by SHA256 and mirror/archive fallback remains
  available.
- Alpine apply behavior remains unchanged.
- Fresh Arch media includes `ca-certificates` in the target package closure.
- `go test ./...`, `./kodos suitetest`, `./kodos selftest`, and
  `build/usb/test-arch-media.sh` pass.
