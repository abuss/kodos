# Alpine static kodos (no kodos.apk repo) Implementation Plan

**Goal:** Alpine bare-metal install media delivers kodos as a static binary in generation 0, with no `kodos.apk` and no local repo declared for it — matching the Arch media.

**Architecture:** `mkinstall-usb.sh` stops building/staging `kodos.apk` but keeps the local repo for live-host tools (`parted`, `util-linux`). `machine.lua`/`target-config.lua` drop the `kodos` repo and package. `run-install.sh` drops the local repo from the lock call and installs the static binary into the real generation directory after `exec`. The executor's existing `carryUnmanagedKodos` propagates that unmanaged binary into later generations; no Go change needed.

**Tech Stack:** POSIX shell, Lua config layers, Go executor, jq for host-side IR checks.

**Spec:** Spike conclusion from 2026-11-20 (conversation): "Remove the need of a repo for kodos.apk" on a branch, preserving the existing media and day-2 apply.

## Global Constraints

- Bare-metal Alpine media and Alpine `kodos apply` must keep working.
- The local repo may remain only for live-installer tools; it must not carry or declare `kodos.apk`.
- Static binary goes to `$TARGET_ROOT/kod/generation/0/usr/bin/kodos` (`/usr` is a chroot-rooted symlink after activation).
- Gates: `cd executor && go test ./...`, `./kodos suitetest`, `./kodos selftest`, `sh build/usb/test-arch-media.sh`, `sh build/usb/test-alpine-media.sh`.

## Review Focus

- Staged payload still containing `kodos-*.apk` → test: stage payload, assert none.
- Generated IR still emitting `copy_tree` to `/kod/repo` or a `kodos` repo/package → test: generate IR from staged config, assert absent.
- Installer writing through the activated `/usr` symlink → test: assert generation-dir path in `run-install.sh`.
- Day-2 `target-config.lua` still pointing at a repo that no longer ships kodos → test: assert no `file:///kod/repo` in both configs.
- `test-arch-media.sh` guard that pins Alpine media unchanged → test: remove the stale guard so the Arch gate still passes.

## Tasks

### Task 1: Host-side media test (RED)

Files: create `build/usb/test-alpine-media.sh`; modify `build/usb/test-arch-media.sh`.
Write the checks in Review Focus, stage the payload with `STAGE_ONLY=1`, generate IR with the staged binary, run the script and confirm it fails because `kodos.apk` is still staged. Commit.

### Task 2: Implement static-binary Alpine media (GREEN)

Files: `build/usb/mkinstall-usb.sh`, `build/usb/run-install.sh`, `build/usb/config/machine.lua`, `build/usb/config/target-config.lua`, `build/usb/README.md`, `build/usb/test-arch-media.sh`.
Drop kodos.apk staging and the local repo from lock/IR; install the static binary into generation 0 after `exec`; update docs. Run all gates, commit.
