package main

// Unit tests for `kodos status` / `kodos rollback`:
//   rollbackTarget — pure target selection (previous = highest gen strictly
//   below current; explicit N must exist, error names the usable list)
//   statusTo       — rendering over a temp kod tree with one good gen, one
//   CORRUPT generation.json and one gen with no manifest at all, plus the
//   broken/absent current alias cases.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRollbackTarget(t *testing.T) {
	gens := []int{0, 1, 2}
	p := func(n int) *int { return &n }

	// explicit N present in gens -> that N
	if got, err := rollbackTarget(2, p(0), gens); err != nil || got != 0 {
		t.Errorf("explicit 0: got %d, %v", got, err)
	}
	// explicit N == current is allowed (re-activate the same generation)
	if got, err := rollbackTarget(2, p(2), gens); err != nil || got != 2 {
		t.Errorf("explicit == current: got %d, %v", got, err)
	}
	// previous = highest generation strictly below current
	if got, err := rollbackTarget(2, nil, gens); err != nil || got != 1 {
		t.Errorf("previous of 2: got %d, %v", got, err)
	}
	if got, err := rollbackTarget(5, nil, []int{0, 1, 3, 4}); err != nil || got != 4 {
		t.Errorf("previous of 5 over {0,1,3,4}: got %d, %v", got, err)
	}
	if got, err := rollbackTarget(1, nil, []int{0, 4, 7}); err != nil || got != 0 {
		t.Errorf("previous of 1 over {0,4,7}: got %d, %v", got, err)
	}

	// explicit N missing -> hard error naming N AND the generation list
	_, err := rollbackTarget(2, p(5), gens)
	if err == nil {
		t.Fatal("explicit 5 must be a hard error")
	}
	if !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "0 1 2") {
		t.Errorf("explicit-missing error must name N and list gens: %v", err)
	}

	// previous with current at the lowest generation -> error listing gens
	_, err = rollbackTarget(0, nil, gens)
	if err == nil {
		t.Fatal("previous below the lowest generation must error")
	}
	if !strings.Contains(err.Error(), "0 1 2") || !strings.Contains(err.Error(), "explicit") {
		t.Errorf("no-previous error must list gens and say pass N: %v", err)
	}

	// broken current alias (current < 0): no implicit target, says so
	_, err = rollbackTarget(-1, nil, gens)
	if err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Errorf("broken current must error telling the user to pass N: %v", err)
	}

	// explicit N still rejected against an empty store
	if _, err := rollbackTarget(0, p(0), nil); err == nil {
		t.Error("explicit N with no generations must error")
	}
}

// writeManifest marshals a metadata struct the same way stampGeneration does.
func writeManifest(t *testing.T, root string, gen int, meta *metadata, raw string) {
	t.Helper()
	dir := filepath.Join(root, "kod", "generation", strconv.Itoa(gen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := []byte(raw)
	if raw == "" {
		var err error
		b, err = json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generation.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseRollbackArgs(t *testing.T) {
	// N first (the documented form) with flags after it
	n, root, apk, err := parseRollbackArgs([]string{"3", "-root", "scratch/root", "-apk", "vm/apk-tools-static"})
	if err != nil || n == nil || *n != 3 || root != "scratch/root" || apk != "vm/apk-tools-static" {
		t.Errorf("N + flags: n=%v root=%q apk=%q err=%v", n, root, apk, err)
	}
	// flags before N works too (the lift is value-aware: "r" is -root's value)
	n, root, _, err = parseRollbackArgs([]string{"-root", "r", "7"})
	if err != nil || n == nil || *n != 7 || root != "r" {
		t.Errorf("flags then N: n=%v root=%q err=%v", n, root, err)
	}
	// the common no-N form: flags only, their values must not become N
	n, root, apk, err = parseRollbackArgs([]string{"-root", "scratch/root", "-apk", "vm/apk-tools-static"})
	if err != nil || n != nil || root != "scratch/root" || apk != "vm/apk-tools-static" {
		t.Errorf("flags only (no N): n=%v root=%q apk=%q err=%v", n, root, apk, err)
	}
	// --flag=value form keeps its value attached
	if n, root, _, err := parseRollbackArgs([]string{"-root=eq", "4"}); err != nil || n == nil || *n != 4 || root != "eq" {
		t.Errorf("-root=value form: n=%v root=%q err=%v", n, root, err)
	}
	if n, _, _, err := parseRollbackArgs(nil); err != nil || n != nil {
		t.Errorf("bare rollback: n=%v err=%v", n, err)
	}
	// stray positionals / junk N fail closed
	if _, _, _, err := parseRollbackArgs([]string{"3", "extra"}); err == nil {
		t.Error("stray positional must error")
	}
	if _, _, _, err := parseRollbackArgs([]string{"notagen"}); err == nil {
		t.Error("non-numeric N must error")
	}
}

func TestRollbackRefusesUnusableTargets(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, 0, &metadata{SchemaVersion: 1, Generation: 0, Distro: "alpine"}, "")
	// dir/manifest disagree: activating it would flip current to a DIFFERENT
	// generation than the user asked for
	writeManifest(t, root, 3, &metadata{SchemaVersion: 1, Generation: 4, Distro: "alpine"}, "")
	// readable manifest, but no kernel in the tree: the bootloader leg would
	// fail AFTER activation, so refuse up front
	writeManifest(t, root, 5, &metadata{SchemaVersion: 1, Generation: 5, Distro: "alpine"}, "")
	if err := os.Symlink("/kod/generation/0", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}

	// gen 0 has a kernel (a complete generation); 3 and 5 deliberately do not.
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "0", "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kod", "generation", "0", "boot", "vmlinuz-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		n    int
		want string
	}{
		{3, "declares generation 4"},
		{5, "no boot/vmlinuz"},
	} {
		n := tc.n
		if err := rollback(root, &n, "apk"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rollback %d: want error naming %q, got %v", tc.n, tc.want, err)
		}
	}
	// nothing was activated in any error path
	if s, _ := os.Readlink(filepath.Join(root, "kod", "generation", "current")); s != "/kod/generation/0" {
		t.Errorf("error paths must not move current, got %q", s)
	}
}

func TestStatusGenerationTable(t *testing.T) {
	root := t.TempDir()

	writeManifest(t, root, 0, &metadata{
		SchemaVersion: 1, Generation: 0, Distro: "alpine",
		Timestamp:  "2026-11-14T21:01:41Z",
		ConfigHash: "3bc71c0718e37452530950e2d81303a5b342db9c",
		Packages:   map[string]pkgPin{"a": {Version: "1"}, "b": {Version: "2"}, "c": {Version: "3"}},
	}, "")
	writeManifest(t, root, 1, nil, "this is not json {{{") // CORRUPT
	// generation 2: dir with NO generation.json
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "2"), 0o755); err != nil {
		t.Fatal(err)
	}
	// non-numeric junk dir + the current alias must not enter the table
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "notagen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/0", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := statusTo(&buf, root); err != nil {
		t.Fatalf("status on a readable store: %v", err)
	}
	out := buf.String()
	t.Log("\n" + out)

	if !strings.Contains(out, "current → kod/generation/0") {
		t.Errorf("missing current line:\n%s", out)
	}
	if !strings.Contains(out, "2026-11-14T21:01:41Z") || !strings.Contains(out, "3bc71c0718e3") {
		t.Errorf("good generation must show its timestamp + short config_hash:\n%s", out)
	}
	if !strings.Contains(out, "corrupt") {
		t.Errorf("corrupt generation.json must be named as corrupt:\n%s", out)
	}
	if !strings.Contains(out, "no metadata") {
		t.Errorf("manifest-less generation must say no metadata:\n%s", out)
	}
	if strings.Contains(out, "notagen") {
		t.Errorf("non-numeric entry must not appear:\n%s", out)
	}
	if !strings.Contains(out, "no grub.cfg") {
		t.Errorf("absent grub.cfg must say so:\n%s", out)
	}

	// an empty store is reported honestly (and its path named exactly once)
	buf.Reset()
	empty := t.TempDir()
	if err := statusTo(&buf, empty); err != nil {
		t.Fatalf("status on an empty store: %v", err)
	}
	if !strings.Contains(buf.String(), "no generations under "+filepath.Join(empty, "kod", "generation")+"\n") {
		t.Errorf("empty store must name the gens dir once:\n%s", buf.String())
	}
}

func TestStatusBootDefaultAndBrokenCurrent(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, 0, &metadata{
		SchemaVersion: 1, Generation: 0, Distro: "alpine", Packages: map[string]pkgPin{},
	}, "")
	if err := os.MkdirAll(filepath.Join(root, "boot", "grub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "set default=0\nmenuentry 'Kodos generation 0' {\n\tlinux /vmlinuz-virt root=UUID=x\n}\n"
	if err := os.WriteFile(filepath.Join(root, "boot", "grub", "grub.cfg"), []byte(cfg), 0o444); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := statusTo(&buf, root); err != nil {
		t.Fatalf("status with no current alias: %v", err) // absent alias is not an error
	}
	out := buf.String()
	t.Log("\n" + out)
	if !strings.Contains(out, "boot default: Kodos generation 0") {
		t.Errorf("grub.cfg menu entry title must be reported as boot default:\n%s", out)
	}
	if !strings.Contains(out, "current") || !strings.Contains(out, "no current alias") {
		t.Errorf("absent alias must be reported, not crash:\n%s", out)
	}

	// relative -root must render the same tree: chrootResolve turns host paths
	// chroot-relative, so a relative root would double the prefix and every
	// host-side read misses (status absolutizes; cmdExec now does too).
	t.Chdir(root)
	buf.Reset()
	if err := statusTo(&buf, "."); err != nil {
		t.Fatalf("status with a relative root: %v", err)
	}
	if !strings.Contains(buf.String(), "boot default: Kodos generation 0") {
		t.Errorf("relative root must read grub.cfg:\n%s", buf.String())
	}

	// present-but-broken alias (points at a deleted generation): says BROKEN
	// and exits nonzero, still listing the generations it can read.
	if err := os.Symlink("/kod/generation/9", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	err := statusTo(&buf, root)
	out = buf.String()
	if err == nil {
		t.Errorf("broken current alias must be an error, out:\n%s", out)
	}
	if !strings.Contains(out, "BROKEN") || !strings.Contains(out, "9") {
		t.Errorf("broken alias line must say BROKEN and name the target:\n%s", out)
	}

	// unparseable alias target (human hand-edit): same treatment
	if err := os.Remove(filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/notagen", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := statusTo(&buf, root); err == nil {
		t.Error("unparseable current alias must be an error")
	}
	if !strings.Contains(buf.String(), "BROKEN") {
		t.Errorf("unparseable alias must say BROKEN:\n%s", buf.String())
	}
}
