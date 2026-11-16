package main

// ops_test.go — the copy_tree self-copy guard (ops.go): the guest's own config
// declares repositories.kodos.src = "/kod/repo", i.e. the SAME directory
// the mapped dst resolves to, so every in-guest `kodos apply` walks straight
// into it. Walking a tree onto itself must be a no-op, and a genuine copy must
// still land (dirs 0755, files 0644).
import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeSelfCopySkip(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(src, "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "x86_64", "kodos-test-fixture.apk"), []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &executor{s: &system{chroot: root}}

	if err := e.copyTree(map[string]any{
		"op": "copy_tree", "name": "kodos", "src": src, "dst": "/repo",
	}); err != nil {
		t.Fatalf("src == mapped dst must skip, got: %v", err)
	}

	if err := e.copyTree(map[string]any{
		"op": "copy_tree", "name": "kodos", "src": src, "dst": "/kod/repo",
	}); err != nil {
		t.Fatalf("copy to a different dst: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "kod/repo/x86_64/kodos-test-fixture.apk"))
	if err != nil || string(b) != "apk" {
		t.Fatalf("copied content missing/wrong: %q %v", string(b), err)
	}
}

// paste round 2 (2026-11-14): a REAL installed root ships top-level dirs as
// chroot-rooted symlinks into /kod/generation/... (e.g. <chroot>/var ->
// /kod/generation/current/var, meaningful only under the chroot). copy_tree
// must resolve such links (like every other target-root-touching op does via
// s.chrootResolve) — a raw Join dies at MkdirAll with "file exists".
func TestCopyTreeChrootRootedSymlinkDst(t *testing.T) {
	root := t.TempDir()
	realVar := filepath.Join(root, "kod/generation/current/var")
	if err := os.MkdirAll(realVar, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/current/var", filepath.Join(root, "var")); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "hostrepo")
	if err := os.MkdirAll(filepath.Join(src, "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "x86_64", "kodos-test-fixture.apk"), []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &executor{s: &system{chroot: root}}
	if err := e.copyTree(map[string]any{
		"op": "copy_tree", "name": "kodos", "src": src, "dst": "/var/repo",
	}); err != nil {
		t.Fatalf("copy under a symlinked top dir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(realVar, "repo/x86_64/kodos-test-fixture.apk"))
	if err != nil || string(b) != "apk" {
		t.Fatalf("content missing at the link's real target: %q %v", string(b), err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "var")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("<chroot>/var must still be the symlink, not a materialized dir: %v %v", fi, err)
	}
}

// the config `files` mode must reach the file even when the file already
// exists with another mode (os.WriteFile alone would keep the old one)
func TestWriteFileExplicitMode(t *testing.T) {
	root := t.TempDir()
	e := &executor{s: &system{chroot: root}}
	p := filepath.Join(root, "etc/kodos/secrets.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.writeFile(map[string]any{
		"op": "write_file", "path": "/etc/kodos/secrets.json",
		"content": "{}", "mode": "0600",
	}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
	if err := e.writeFile(map[string]any{
		"op": "write_file", "path": "/etc/kodos/secrets.json",
		"content": "{}", "mode": "rwxr",
	}); err == nil {
		t.Fatal("non-octal mode must be a hard error, not a silent default")
	}
}

// Real bare-metal round (2026-11-16): a disk with a STALE SWAP signature on
// the partition config wants as ext4 (left by a crashed/partial run or an
// accidental mkswap) made format "succeed" via repairFS(no-op for swap), and
// the later mount died on "unknown filesystem type 'swap'". A swap signature
// is not user data: formatting must self-heal it. A REAL filesystem — even a
// foreign one — stays protected. Mirror rule for swap: mkswap must refuse to
// silently destroy a real fs sitting on the swap-designated partition.
func TestStaleSwapVsRealFS(t *testing.T) {
	if existingBlocksFormat("swap", "ext4") || existingBlocksFormat("swap", "vfat") {
		t.Fatal("stale swap signature must not block mkfs on an fs-designated partition")
	}
	if !existingBlocksFormat("ext4", "ext4") || !existingBlocksFormat("ntfs", "ext4") {
		t.Fatal("real filesystems must be protected (repair/reuse), foreign ones too")
	}
	if existingBlocksFormat("", "ext4") {
		t.Fatal("empty partition formats normally")
	}
	if !mkswapDestroysRealFS("ext4") || !mkswapDestroysRealFS("ntfs") {
		t.Fatal("mkswap must refuse partitions carrying a real fs")
	}
	if mkswapDestroysRealFS("swap") || mkswapDestroysRealFS("") {
		t.Fatal("swap-or-empty swap partitions are fine")
	}
}
