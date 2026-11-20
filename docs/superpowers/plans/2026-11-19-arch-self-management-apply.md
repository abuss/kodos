# Arch self-management apply Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `kodos apply` work on Arch targets by fetching live Arch sync DBs and locking through the existing Arch lock path, while keeping the Alpine apply path unchanged.

**Architecture:** Capture `configure_repos.type` in `apply`, branch Alpine and Arch index fetching, and reuse `lockfile.lua`’s ALPM mode. Add `ca-certificates` to the Arch target package closure so the target can validate HTTPS mirrors.

**Tech Stack:** Go 1.x executor, embedded Lua toolchain, ALPM `desc` metadata, `net/http`, `archive/tar`, `compress/gzip`, BusyBox shell host tests.

**Spec:** `docs/superpowers/specs/2026-11-19-arch-self-management-apply-design.md`

## Global Constraints

- Keep Alpine `apply`, install media, and package behavior behavior-compatible.
- Arch apply uses live HTTPS DB fetch plus SHA256 package verification; no GPG or pacman on the target.
- The milestone is x86_64-only.
- Every task uses TDD: RED, GREEN, then commit.
- After every task, run the smallest relevant focused test; before final task, run the full gate suite.
- Existing archive fallback in `fetchUnpackArch` must continue to work.
- Errors must name the exact URL, branch, repo type, or config path involved.

## Review Focus

- Missing CA bundle on an Arch target should produce a TLS error that mentions `ca-certificates`; Task 2 tests this.
- Missing Arch DB URL should fail naming the exact attempted URL and branch, leaving no temp output; Task 2 tests this.
- Malformed Arch DB tarball should fail naming the URL and `desc` member pattern; Task 2 tests this.
- Arch config with local repositories should fail before any fetch with an explicit unsupported-milestone message; Task 3 tests this.
- Missing or unsupported `configure_repos.type` should fail before any fetch with a repo-type error; Task 3 tests this.

---

### Task 1: Arch CA Trust Store

**Files:**
- Modify: `executor/lua/backends/arch.lua`
- Modify: `executor/lua/test-generate.lua`
- Modify: `build/usb/test-arch-media.sh`

**Interfaces:**
- Consumes: existing `generate.lua` Arch media config generation.
- Produces: every Arch generation requests the `ca-certificates` package.

- [ ] **Step 1: Add failing Lua IR assertion**

In `executor/lua/test-generate.lua`, inside the standalone bare-metal Arch media assertions near `local am = run("../../build/usb/arch-config/arch-machine.lua")`, add:

```lua
assert(pos(am, '"ca-certificates"'), "Arch target must ship a CA trust store")
```

- [ ] **Step 2: Run the focused Lua test to verify it fails**

Run:

```sh
cd executor/lua
lua test-generate.lua
```

Expected: failure naming `Arch target must ship a CA trust store`.

- [ ] **Step 3: Add failing host media assertion**

In `build/usb/test-arch-media.sh`, after real staging succeeds and after the staged-file existence loop, add:

```sh
"$STAGE/bin/kodos" lua generate.lua "$STAGE/config/arch-machine.lua" \
    | grep -q '"ca-certificates"' || {
    echo "FAIL: staged Arch media IR does not request ca-certificates"; exit 1;
}
```

- [ ] **Step 4: Add `ca-certificates` to the Arch backend**

Modify `executor/lua/backends/arch.lua` so Arch base packages include the CA meta package:

```lua
M.base_packages = { "base", "tzdata", "ca-certificates" }
```

- [ ] **Step 5: Run focused Lua test**

Run:

```sh
cd executor/lua
lua test-generate.lua
```

Expected: `PASS: IR stream (layered config, ordered, deterministic, strict)`.

- [ ] **Step 6: Run host media test**

Run from the repository root:

```sh
sh build/usb/test-arch-media.sh
```

Expected: `test-arch-media installer: ok`.

- [ ] **Step 7: Commit**

```sh
git add executor/lua/backends/arch.lua executor/lua/test-generate.lua build/usb/test-arch-media.sh
git commit -m "feat: add CA trust store to Arch targets"
```

---

### Task 2: Arch Live DB Fetch And desc Extraction

**Files:**
- Create: `executor/apply_arch.go`
- Create: `executor/apply_arch_test.go`

**Interfaces:**
- Consumes: configured Arch mirror and branches; ALPM DB tarballs over HTTPS.
- Produces:
  - `func fetchArchIndexes(mirror string, branches []string, dirArch string, tmp string) ([]string, error)`
  - `func archDBURLs(mirror string, branch string, dirArch string) []string`
  - `func extractArchDesc(blob []byte) ([]byte, error)`

- [ ] **Step 1: Write the Arch DB tar fixture helper**

Create `executor/apply_arch_test.go` with package `main` and a helper modeled after `apkIndexTarGz` in `executor/apply_test.go`:

```go
func archDBTarGz(t *testing.T, members [][2]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{
			Name: m[0], Mode: 0o644, Size: int64(len(m[1])), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return gz.Bytes()
}
```

- [ ] **Step 2: Write failing success and fallback tests**

Add tests asserting:

- `fetchArchIndexes` returns `<branch>.desc` paths in branch order.
- It concatenates all `*/desc` members from `<mirror>/<branch>/os/x86_64/<branch>.db.tar.gz`.
- It falls back to `<mirror>/<branch>/os/x86_64/<branch>.db` when the primary URL is 404.

Use fixture desc values like:

```go
const coreDesc = "%FILENAME%\nlinux-1.0-1-x86_64.pkg.tar.zst\n%NAME%\nlinux\n%VERSION%\n1.0-1\n%SHA256SUM%\nabc\n\n"
const extraDesc = "%FILENAME%\nsudo-1.0-1-x86_64.pkg.tar.zst\n%NAME%\nsudo\n%VERSION%\n1.0-1\n%SHA256SUM%\ndef\n\n"
```

Expected before implementation: compile failure because `fetchArchIndexes` is undefined.

- [ ] **Step 3: Write failing error tests**

Add tests for:

- HTTP 404 on both DB URL variants: error contains the primary URL and the branch name, and the temp directory remains empty.
- Malformed DB tarball: error contains `desc` and the attempted URL.
- TLS unknown-authority failure: use `httptest.NewTLSServer` and assert the returned error contains `ca-certificates`.

- [ ] **Step 4: Run focused Go tests to verify they fail**

Run:

```sh
cd executor
go test ./...
```

Expected before implementation: compile failure naming `fetchArchIndexes`, `archDBURLs`, or `extractArchDesc`.

- [ ] **Step 5: Implement `archDBURLs`**

In `executor/apply_arch.go`, implement:

```go
func archDBURLs(mirror, branch, dirArch string) []string {
	return []string{
		fmt.Sprintf("%s/%s/os/%s/%s.db.tar.gz", mirror, branch, dirArch, branch),
		fmt.Sprintf("%s/%s/os/%s/%s.db", mirror, branch, dirArch, branch),
	}
}
```

- [ ] **Step 6: Implement `extractArchDesc`**

Implement `extractArchDesc` to:

- gunzip `blob`;
- read the tar stream;
- append the contents of every regular member whose name ends in `/desc`;
- return an error containing `no desc member in Arch DB tarball` if none are found.

Signature:

```go
func extractArchDesc(blob []byte) ([]byte, error)
```

- [ ] **Step 7: Implement `fetchArchIndexes`**

Implement `fetchArchIndexes` to:

- use an `http.Client{Timeout: 2 * time.Minute}`;
- for each branch in order, try the URLs from `archDBURLs` until one returns HTTP 200;
- on non-200, remember an error naming that exact URL;
- on transport failure, wrap the error with `archFetchError` before remembering it;
- call `extractArchDesc` on successful bodies;
- buffer all desc bytes and write files only after every branch has succeeded, so partial failures leave `tmp` empty.

Signature:

```go
func fetchArchIndexes(mirror string, branches []string, dirArch string, tmp string) ([]string, error)
```

- [ ] **Step 8: Implement CA hint wrapper**

Add:

```go
func archFetchError(branch, url string, err error) error
```

It returns a wrapped error naming the branch and URL. If the error contains `certificate signed by unknown authority`, append a hint that the target is likely missing `ca-certificates`.

- [ ] **Step 9: Run focused Go tests to verify they pass**

Run:

```sh
cd executor
go test ./...
```

Expected: all Go tests pass.

- [ ] **Step 10: Commit**

```sh
git add executor/apply_arch.go executor/apply_arch_test.go
git commit -m "feat: fetch Arch indexes for apply"
```

---

### Task 3: Apply Repo-Type Selection

**Files:**
- Modify: `executor/apply.go`
- Modify: `executor/apply_test.go`

**Interfaces:**
- Consumes: generated `configure_repos.type`, `localRepos`, mirror, and branches.
- Produces:
  - `func chooseIndexFetch(repoType string, localRepos []localRepoRef) (string, error)`
  - `apply` calls `fetchIndexes` only for Alpine/APK and `fetchArchIndexes` for Arch.

- [ ] **Step 1: Write failing selection tests**

In `executor/apply_test.go`, add a test for `chooseIndexFetch`:

```go
func TestChooseIndexFetch(t *testing.T) {
	if got, err := chooseIndexFetch("arch", nil); err != nil || got != "arch" {
		t.Fatalf("arch: got %q, %v", got, err)
	}
	if got, err := chooseIndexFetch("apk", []localRepoRef{{name: "local"}}); err != nil || got != "apk" {
		t.Fatalf("apk with local repo: got %q, %v", got, err)
	}

	_, err := chooseIndexFetch("arch", []localRepoRef{{name: "local"}})
	if err == nil || !strings.Contains(err.Error(), "local repositories are not supported for Arch") {
		t.Fatalf("arch with local repo: error %v", err)
	}

	for _, repoType := range []string{"", "deb"} {
		_, err = chooseIndexFetch(repoType, nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported repo type") {
			t.Fatalf("repo type %q: error %v", repoType, err)
		}
	}
}
```

Run focused Go test and verify it fails because `chooseIndexFetch` is undefined.

- [ ] **Step 2: Implement `chooseIndexFetch`**

Add to `executor/apply.go`:

```go
func chooseIndexFetch(repoType string, localRepos []localRepoRef) (string, error) {
	switch repoType {
	case "apk":
		return "apk", nil
	case "arch":
		if len(localRepos) > 0 {
			return "", fmt.Errorf("apply: local repositories are not supported for Arch in this milestone")
		}
		return "arch", nil
	default:
		return "", fmt.Errorf("apply: unsupported repo type %q", repoType)
	}
}
```

- [ ] **Step 3: Capture repo type in apply**

In `cmdApply`’s `apply` function, while scanning for `configure_repos`, capture:

```go
repoType = field(act, "type")
```

alongside `mirror`, `branches`, and `localRepos`.

- [ ] **Step 4: Branch index fetching by kind**

Replace the single `fetchIndexes` call with:

```go
kind, err := chooseIndexFetch(repoType, localRepos)
if err != nil {
	return err
}

var indexes []string
if kind == "arch" {
	indexes, err = fetchArchIndexes(mirror, branches, "x86_64", tmp)
} else {
	indexes, err = fetchIndexes(mirror, branches, localRepos, tmp)
}
if err != nil {
	return err
}
```

Keep all later lockfile/exec logic unchanged.

- [ ] **Step 5: Run Go tests**

Run:

```sh
cd executor
go test ./...
```

Expected: all Go tests pass.

- [ ] **Step 6: Run full executor suites**

Run:

```sh
cd executor
go build -o kodos .
./kodos suitetest
./kodos selftest
```

Expected: suite and selftest pass.

- [ ] **Step 7: Commit**

```sh
git add executor/apply.go executor/apply_test.go
git commit -m "feat: select Arch apply index path"
```

---

### Task 4: Docs And Final Verification

**Files:**
- Modify: `build/usb/README-arch.md`

**Interfaces:**
- Consumes: completed Arch apply code and CA prerequisite.
- Produces: operator docs for Arch day-2 behavior and existing-install limitations.

- [ ] **Step 1: Update day-2 docs**

Replace the existing Arch “day-2 in-place apply is not wired” paragraph with supported behavior:

- Fresh Arch installs include `ca-certificates` and can run `kodos apply`.
- Apply fetches live `core` and `extra` DBs from the configured mirror.
- Package fetch still verifies SHA256 and uses archive fallback when needed.
- Existing targets without CA certificates need reinstall or manual CA bootstrap.

- [ ] **Step 2: Update troubleshooting**

Add a bullet for:

```text
x509: certificate signed by unknown authority
```

Explain that the Arch target is missing `ca-certificates`, so rebuild/reinstall from fresh media or bootstrap CA certs manually.

- [ ] **Step 3: Run full host gate**

Run from repository root:

```sh
sh build/usb/test-arch-media.sh
```

Expected: `test-arch-media installer: ok`.

- [ ] **Step 4: Run full executor gates**

Run:

```sh
cd executor
go test ./...
go build -o kodos .
./kodos suitetest
./kodos selftest
```

Expected: all pass.

- [ ] **Step 5: Commit docs**

```sh
git add build/usb/README-arch.md
git commit -m "docs: document Arch self-management apply"
```

- [ ] **Step 6: Manual VM round**

Rebuild fresh Arch media and run a VM apply round. A successful round proves:

- the target has `/etc/ssl/certs/ca-certificates.crt`;
- `kodos status` works;
- `kodos apply --dry-run` fetches Arch DBs and prints a new generation plan;
- real `kodos apply` creates and activates a new generation;
- `kodos rollback` restores the previous generation.
