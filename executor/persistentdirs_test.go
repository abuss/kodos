package main

import (
	"os"
	"path/filepath"
	"testing"
)

func pdWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pdAssertRealDir(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s missing: %v", path, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a real directory: %v", path, fi.Mode())
	}
}

func pdAssertContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != want {
		t.Fatalf("%s = %q, want %q", path, b, want)
	}
}

// /root and /home hold operator state (SSH keys, shell history, user files).
// They must be ONE persistent pair of dirs on the live root, not relinked
// through `current` on every generation switch like /usr is.
func TestRootAndHomePersistAcrossGenerations(t *testing.T) {
	root := t.TempDir()
	s := &system{chroot: root}

	g0 := filepath.Join(root, "kod/generation/0")
	pdWrite(t, filepath.Join(g0, "root/.bashrc"), "bash v0\n")
	pdWrite(t, filepath.Join(g0, "home/alice/.profile"), "profile v0\n")
	if err := s.activateGeneration(g0); err != nil {
		t.Fatal(err)
	}

	pdAssertRealDir(t, filepath.Join(root, "root"))
	pdAssertRealDir(t, filepath.Join(root, "home"))

	// data written after install must live on the persistent root
	pdWrite(t, filepath.Join(root, "root/.ssh/id_ed25519"), "ssh key\n")
	pdWrite(t, filepath.Join(root, "home/alice/notes.txt"), "notes\n")

	g1 := filepath.Join(root, "kod/generation/1")
	pdWrite(t, filepath.Join(g1, "root/.bashrc"), "bash v1\n")
	pdWrite(t, filepath.Join(g1, "home/alice/.profile"), "profile v1\n")
	if err := s.activateGeneration(g1); err != nil {
		t.Fatal(err)
	}

	pdAssertRealDir(t, filepath.Join(root, "root"))
	pdAssertRealDir(t, filepath.Join(root, "home"))
	pdAssertContent(t, filepath.Join(root, "root/.ssh/id_ed25519"), "ssh key\n")
	pdAssertContent(t, filepath.Join(root, "home/alice/notes.txt"), "notes\n")
	pdAssertContent(t, filepath.Join(root, "root/.bashrc"), "bash v1\n")
	pdAssertContent(t, filepath.Join(root, "home/alice/.profile"), "profile v1\n")
}

// Upgrading a target installed by the old code: live /root and /home are
// chroot-rooted symlinks into `current`. The user data sitting in the OUTGOING
// generation must be materialized into real dirs, not lost when current flips.
func TestLegacyRootHomeSymlinkMigrationKeepsUserData(t *testing.T) {
	root := t.TempDir()
	s := &system{chroot: root}

	g0 := filepath.Join(root, "kod/generation/0")
	pdWrite(t, filepath.Join(g0, "root/.bashrc"), "bash v0\n")
	pdWrite(t, filepath.Join(g0, "root/.ssh/id_ed25519"), "ssh key\n")
	pdWrite(t, filepath.Join(g0, "home/alice/notes.txt"), "notes\n")
	if err := os.Symlink("/kod/generation/0", filepath.Join(root, "kod/generation/current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/current/root", filepath.Join(root, "root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/current/home", filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}

	g1 := filepath.Join(root, "kod/generation/1")
	pdWrite(t, filepath.Join(g1, "root/.bashrc"), "bash v1\n")
	pdWrite(t, filepath.Join(g1, "home/alice/.profile"), "profile v1\n")
	if err := s.activateGeneration(g1); err != nil {
		t.Fatal(err)
	}

	pdAssertRealDir(t, filepath.Join(root, "root"))
	pdAssertRealDir(t, filepath.Join(root, "home"))
	pdAssertContent(t, filepath.Join(root, "root/.ssh/id_ed25519"), "ssh key\n")
	pdAssertContent(t, filepath.Join(root, "home/alice/notes.txt"), "notes\n")
	pdAssertContent(t, filepath.Join(root, "root/.bashrc"), "bash v1\n")
	pdAssertContent(t, filepath.Join(root, "home/alice/.profile"), "profile v1\n")
}

// Packages never own /proc or /sys. A round that had the pseudo-fs mounted
// under the chroot while an old freeze was running (later fixed) pulled
// sysfs content into the generation tree (field round 2026-11-20:
// ERROR: open .../generation/0/sys/bus/ac97/drivers_probe: permission
// denied). Activation must never merge those top-level dirs live.
func TestActivationNeverMergesProcSys(t *testing.T) {
	root := t.TempDir()
	s := &system{chroot: root}

	g0 := filepath.Join(root, "kod/generation/0")
	pdWrite(t, filepath.Join(g0, "usr/share/thing"), "ok\n")
	// sysfs-shaped contamination: write-only control files
	pdWrite(t, filepath.Join(g0, "sys/bus/ac97/drivers_probe"), "")
	if err := os.Chmod(filepath.Join(g0, "sys/bus/ac97/drivers_probe"), 0o200); err != nil {
		t.Fatal(err)
	}
	pdWrite(t, filepath.Join(g0, "sys/kernel/mm/transparent_hugepage/enabled"), "always\n")

	if err := s.activateGeneration(g0); err != nil {
		t.Fatalf("activation must ignore proc/sys contamination, got: %v", err)
	}
	for _, junk := range []string{"sys", "proc"} {
		if _, err := os.Lstat(filepath.Join(root, junk)); !os.IsNotExist(err) {
			t.Fatalf("live /%s was merged from generation junk", junk)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "usr/share/thing")); err != nil {
		t.Fatalf("normal usr merge lost: %v", err)
	}
}
