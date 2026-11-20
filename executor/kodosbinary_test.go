package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Arch delivers kodos as an unmanaged file installed into generation 0 by
// arch-run-install.sh (no local repo / kodos.apk). create_generation rebuilds
// the new tree only from package-store contents, so without an explicit carry
// generation 1 boots with no kodos and self-management dies. Reproduce that
// exact shape: active gen 0 has the binary behind a chroot-rooted /usr link,
// the freshly assembled gen 1 does not.
func TestCarryUnmanagedKodosIntoNewGeneration(t *testing.T) {
	root := t.TempDir()
	e := &executor{s: &system{chroot: root}}

	curBin := filepath.Join(root, "kod", "generation", "0", "usr", "bin")
	if err := os.MkdirAll(curBin, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := []byte("static kodos payload")
	if err := os.WriteFile(filepath.Join(curBin, "kodos"), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/0/usr", filepath.Join(root, "usr")); err != nil {
		t.Fatal(err)
	}

	newGen := filepath.Join(root, "kod", "generation", "1")
	if err := os.MkdirAll(filepath.Join(newGen, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := e.carryUnmanagedKodos(newGen); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(newGen, "usr", "bin", "kodos"))
	if err != nil {
		t.Fatalf("new generation has no kodos binary: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("carried binary = %q, want %q", got, binary)
	}
}

// Alpine ships kodos via kodos.apk, so the assembled generation already has
// the store-backed binary. The carry must leave that file untouched.
func TestCarryUnmanagedKodosLeavesPackageBinary(t *testing.T) {
	root := t.TempDir()
	e := &executor{s: &system{chroot: root}}

	newGen := filepath.Join(root, "kod", "generation", "1")
	dst := filepath.Join(newGen, "usr", "bin", "kodos")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("from kodos.apk"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := e.carryUnmanagedKodos(newGen); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from kodos.apk" {
		t.Fatalf("package-provided binary was overwritten: %q", got)
	}
}

// A target that genuinely has no binary (test fixtures, pre-bootstrap) must
// not fail generation creation — the carry is best-effort by existence.
func TestCarryUnmanagedKodosAbsentIsNoop(t *testing.T) {
	root := t.TempDir()
	e := &executor{s: &system{chroot: root}}
	newGen := filepath.Join(root, "kod", "generation", "1")
	if err := e.carryUnmanagedKodos(newGen); err != nil {
		t.Fatalf("absent binary must be a no-op, got %v", err)
	}
}
