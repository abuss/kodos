package main

// store_cowcopy_test.go — cowCopy is the cross-device fallback when hardlinking
// a generation entry into the live tree fails (e.g. /home is its own
// partition). Generation trees contain SPECIAL files (baselayout /dev nodes,
// daemon sockets/FIFOs under home/run dirs). Reading one blocks forever: an
// activate_generation froze 85 minutes parked in epoll on such an fd
// (eszkoz VM round 2026-11-20, goroutine dump: io.Copy under cowCopy).
// cowCopy must skip non-regular files, never copy their "contents".
import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCowCopySkipsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")

	// FIFO with an attached writer: open WOULD succeed, so the hang is in
	// the read — exactly the production stack. cowCopy must return fast.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := exec.Command("sh", "-c", "exec 3>"+fifo+"; sleep 30")
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	defer writer.Process.Kill()

	done := make(chan error, 1)
	go func() { done <- cowCopy(fifo, dst) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cowCopy(fifo): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cowCopy blocked reading a fifo: special file was copied, not skipped")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("cowCopy must not create a copy of a special file")
	}

	// char device: must skip without even opening/reading it
	if _, err := os.Stat("/dev/console"); err == nil {
		dst2 := filepath.Join(dir, "dst2")
		if err := cowCopy("/dev/console", dst2); err != nil {
			t.Fatalf("cowCopy(/dev/console): %v", err)
		}
		if _, err := os.Stat(dst2); !os.IsNotExist(err) {
			t.Fatal("cowCopy created a copy of a device node")
		}
	}
}

func TestCowCopyStillCopiesRegularFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := cowCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "hello" {
		t.Fatalf("regular file must still copy: %q %v", b, err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o644 {
		t.Fatal("mode not preserved")
	}

	// setuid must survive too: a merge that cow-copies /usr/bin/sudo with
	// Perm() silently un-privileges it (odin 2026-11-27: sudo bricked,
	// twice, same root cause).
	suid := filepath.Join(dir, "suid")
	if err := os.WriteFile(suid, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(suid, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	sdst := filepath.Join(dir, "sdst")
	if err := cowCopy(suid, sdst); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(sdst); st.Mode()&os.ModeSetuid == 0 {
		t.Fatalf("setuid bit dropped: mode = %v", st.Mode())
	}
}

// TestSymlinkTreeReMergeRelinks: merging into a POPULATED dst must relink
// (remove + link), never overwrite in place or copy: odin's pre-#17 rounds
// turned generations into multi-GB private-copy forests. Second run must
// produce zero new inodes.
func TestSymlinkTreeReMergeRelinks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	for _, f := range []string{"bin", "sub/file"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte("data"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &system{chroot: dir}
	dst := filepath.Join(dir, "dst")

	// stale private copies in the destination (old farm shape)
	os.MkdirAll(filepath.Join(dst, "sub"), 0o755)
	for _, f := range []string{"bin", "sub/file"} {
		if err := os.WriteFile(filepath.Join(dst, f), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inodes := func() [2]uint64 {
		var out [2]uint64
		for i, f := range []string{"bin", "sub/file"} {
			st, err := os.Stat(filepath.Join(dst, f))
			if err != nil {
				t.Fatal(err)
			}
			out[i] = st.Sys().(*syscall.Stat_t).Ino
		}
		return out
	}
	if err := s.symlinkTree(src, dst); err != nil {
		t.Fatal(err)
	}
	first := inodes()
	for _, f := range []string{"bin", "sub/file"} {
		a, _ := os.Stat(filepath.Join(src, f))
		b, _ := os.Stat(filepath.Join(dst, f))
		if a.Sys().(*syscall.Stat_t).Ino != b.Sys().(*syscall.Stat_t).Ino {
			t.Fatalf("%s not hardlinked into src after re-merge", f)
		}
		if b.Sys().(*syscall.Stat_t).Nlink < 2 {
			t.Fatalf("%s link count %d: content copied, not shared", f, b.Sys().(*syscall.Stat_t).Nlink)
		}
	}
	if err := s.symlinkTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if second := inodes(); second != first {
		t.Fatal("re-merge allocated new inodes (copy churn)")
	}
}
