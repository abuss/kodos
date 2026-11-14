package main

// ops_test.go — the copy_tree self-copy guard (ops.go): the guest's own config
// declares repositories.kodos.src = "/var/kodos/repo", i.e. the SAME directory
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
	if err := os.WriteFile(filepath.Join(src, "x86_64", "kodos-0.1.0-r0.apk"), []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &executor{s: &system{chroot: root}}

	if err := e.copyTree(map[string]any{
		"op": "copy_tree", "name": "kodos", "src": src, "dst": "/repo",
	}); err != nil {
		t.Fatalf("src == mapped dst must skip, got: %v", err)
	}

	if err := e.copyTree(map[string]any{
		"op": "copy_tree", "name": "kodos", "src": src, "dst": "/var/kodos/repo",
	}); err != nil {
		t.Fatalf("copy to a different dst: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "var/kodos/repo/x86_64/kodos-0.1.0-r0.apk"))
	if err != nil || string(b) != "apk" {
		t.Fatalf("copied content missing/wrong: %q %v", string(b), err)
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
