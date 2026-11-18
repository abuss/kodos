package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchKernelJobs(t *testing.T) {
	pins := map[string]pkgPin{
		"linux":          {Version: "6.12-1"},
		"linux-lts":      {Version: "6.6-1"},
		"linux-firmware": {Version: "20260101-1"}, // matches the name regex, owns no modules dir
		"filesystem":     {Version: "2025.10-1"},
	}
	jobs, err := archKernelJobs(pins, []string{"6.12-1", "6.6-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs, got %v", jobs)
	}
	if jobs[0].Pkg != "linux" || jobs[0].Flavor != "linux" || jobs[0].Kver != "6.12-1" {
		t.Errorf("job[0] = %+v", jobs[0])
	}
	if jobs[1].Pkg != "linux-lts" || jobs[1].Kver != "6.6-1" {
		t.Errorf("job[1] = %+v", jobs[1])
	}

	// modules dir no pinned kernel owns: fail closed naming it
	_, err = archKernelJobs(pins, []string{"6.12-1", "6.13-1"})
	if err == nil || !strings.Contains(err.Error(), "6.13-1") {
		t.Errorf("orphan modules dir must name itself, got: %v", err)
	}

	// arch reality: modules dirs carry the -archN reluffix, e.g. pin
	// linux 6.12.9-1 -> /usr/lib/modules/6.12.9-arch1-1
	jobs, err = archKernelJobs(map[string]pkgPin{"linux": {Version: "6.12.9-1"}},
		[]string{"6.12.9-arch1-1"})
	if err != nil || len(jobs) != 1 || jobs[0].Kver != "6.12.9-arch1-1" {
		t.Errorf("pkgver-vs-modulesdir pairing (6.12.9-1 ~ 6.12.9-arch1-1): %v %v", jobs, err)
	}

	// and the CURRENT real shape: pkgver dots become dashes in the modules
	// dir (measured Nov 2026: linux-7.3.4.arch1-2 -> 7.3.4-arch1-2)
	jobs, err = archKernelJobs(map[string]pkgPin{"linux": {Version: "7.3.4.arch1-2"}},
		[]string{"7.3.4-arch1-2"})
	if err != nil || len(jobs) != 1 || jobs[0].Kver != "7.3.4-arch1-2" {
		t.Errorf("pkgver-vs-modulesdir pairing (7.3.4.arch1-2 ~ 7.3.4-arch1-2): %v %v", jobs, err)
	}
}

func gzipCpio(t *testing.T, names ...string) string {
	t.Helper()
	var recs bytes.Buffer
	for _, n := range names {
		recs.Write(cpioRec(0o755, n, "x"))
	}
	recs.WriteString("0707010000000000000000000000000000000000000000000000000000000000TRAILER!!!")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(recs.Bytes()); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	p := filepath.Join(t.TempDir(), "initramfs-test.img")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInitramfsSaneArch(t *testing.T) {
	// mkinitcpio images carry systemd at usr/lib/systemd/systemd; the
	// scanner must tolerate "./"-prefixed encodings.
	ok := gzipCpio(t, "./init", "./usr/bin/sh", "./usr/lib/systemd/systemd")
	if err := initramfsSaneArch(ok); err != nil {
		t.Errorf("real image must pass: %v", err)
	}
	bad := gzipCpio(t, "./init", "./usr/bin/sh", "./usr/bin/busybox")
	err := initramfsSaneArch(bad)
	if err == nil || !strings.Contains(err.Error(), "skeleton") {
		t.Errorf("image without systemd must fail with skeleton error, got: %v", err)
	}
}

func archGenFixture(t *testing.T, initTarget string) string {
	t.Helper()
	gen := t.TempDir()
	// usrmerged shape exactly as the arch store assembles it: top-level
	// bin/sbin are DIR links, init/sh live under usr.
	os.MkdirAll(filepath.Join(gen, "usr/lib/systemd"), 0o755)
	os.MkdirAll(filepath.Join(gen, "usr/bin"), 0o755)
	os.MkdirAll(filepath.Join(gen, "usr/sbin"), 0o755)
	os.WriteFile(filepath.Join(gen, "usr/lib/systemd/systemd"), []byte("e"), 0o755)
	os.WriteFile(filepath.Join(gen, "usr/bin/sh"), []byte("e"), 0o755)
	os.Symlink("usr/bin", filepath.Join(gen, "bin"))
	os.Symlink("usr/sbin", filepath.Join(gen, "sbin"))
	os.Symlink(initTarget, filepath.Join(gen, "usr/sbin/init"))
	return gen
}

func TestCheckGenRunnableArch(t *testing.T) {
	// Ruling (preflight): raw Lstat on usrmerged trees false-PASSES — the
	// final init link may point at a guaranteed-missing target and a raw
	// Lstat still sees the link itself. The check must resolve within the
	// tree before judging.
	gen := archGenFixture(t, "/kodos-test-missing-target/systemd")
	err := checkGenRunnable(gen, 7, "arch")
	if err == nil {
		t.Fatal("init resolving outside the gen tree must fail closed")
	}
	if !strings.Contains(err.Error(), "sbin/init") {
		t.Errorf("error should name sbin/init, got: %v", err)
	}
	// happy arch tree: init -> ../lib/systemd/systemd, resolves inside
	gen2 := archGenFixture(t, "../lib/systemd/systemd")
	if err := checkGenRunnable(gen2, 7, "arch"); err != nil {
		t.Errorf("complete arch gen must pass: %v", err)
	}
	// alpine semantics unchanged: raw entries
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "kod/generation/9/sbin"), 0o755)
	os.MkdirAll(filepath.Join(root, "kod/generation/9/bin"), 0o755)
	os.WriteFile(filepath.Join(root, "kod/generation/9/sbin/init"), []byte("b"), 0o755)
	os.WriteFile(filepath.Join(root, "kod/generation/9/bin/sh"), []byte("b"), 0o755)
	if err := checkGenRunnable(filepath.Join(root, "kod/generation/9"), 9, "alpine"); err != nil {
		t.Errorf("alpine regression: %v", err)
	}
	if err := checkGenRunnable(root+"/empty", 9, "alpine"); err == nil {
		t.Error("alpine missing-init regression")
	}
}
