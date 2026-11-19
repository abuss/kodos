package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
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

	// REAL collision from first e2e: linux-api-headers passes the kernel-name
	// regex and pins 7.3-1; token-prefix matching let "7.3" claim the kernel
	// dir 7.3.4-arch1-2 (dots are structure, not padding). The dir must END
	// at pkgver or continue with a DASH reluffix - a DOT means deeper version.
	pins2 := map[string]pkgPin{
		"linux":             {Version: "7.3.4.arch1-2"},
		"linux-api-headers": {Version: "7.3-1"},
	}
	jobs2, err := archKernelJobs(pins2, []string{"7.3.4-arch1-2"})
	if err != nil {
		t.Fatalf("headers 7.3 must not claim dir 7.3.4-arch1-2: %v", err)
	}
	if len(jobs2) != 1 || jobs2[0].Pkg != "linux" {
		t.Errorf("jobs2 = %+v, want single linux job", jobs2)
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

// cpioNewc renders a tiny newc cpio (name,size per entry) ending with the
// TRAILER record — enough header fidelity for our readers' record walk.
func cpioNewc(t *testing.T, entries [][2]any) []byte {
	t.Helper()
	var out []byte
	rec := func(name string, size int, isTrailer bool) {
		var body []byte
		if !isTrailer {
			body = append(body, []byte(name+"\x00")...)
			pad := (4 - (110+len(name)+1)%4) % 4 // newc pads header+name to 4
			body = append(body, make([]byte, pad)...)
			body = append(body, make([]byte, size)...)
			sizePad := (4 - (110+len(body))%4) % 4 // whole record (hdr+name+data) 4-aligned
			body = append(body, make([]byte, sizePad)...)
		} else {
			body = append([]byte(name+"\x00"), make([]byte, (4-(110+len(name)+1)%4)%4)...)
		}
		nsize := len(name) + 1
		hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			0, 0o100644, 0, 0, 1, 0, size, 0, 0, 0, 0, nsize, 0)
		out = append(out, []byte(hdr)...)
		out = append(out, body...)
	}
	for _, e := range entries {
		rec(e[0].(string), e[1].(int), false)
	}
	rec("TRAILER!!!", 0, true)
	// cpio streams pad to a 512-byte block after the trailer
	for len(out)%512 != 0 {
		out = append(out, 0)
	}
	return out
}

// e2e finding: mkinitcpio images are CONCATENATED — early uncompressed
// cpio + zero padding + zstd-compressed main cpio. A single-magic sniff
// saw the early cpio, fell through to "plain", and bytes.Contains scanned
// compressed bytes: a fully sane image failed the guard ("cpio has no
// usr/lib/systemd/systemd (22413280 bytes unpacked)" where 22413280 was
// the FILE size, not an unpacked size).
func TestInitramfsSaneArchEarlyCpioConcat(t *testing.T) {
	early := cpioNewc(t, [][2]any{{"early_cpio", 0}, {"usr/lib/modules/x/intel.ko.zst", 64}})
	mainCpio := cpioNewc(t, [][2]any{{"init", 0}, {"usr/lib/systemd/systemd", 8}})
	var zbuf bytes.Buffer
	zw, _ := zstd.NewWriter(&zbuf)
	zw.Write(mainCpio)
	zw.Close()
	img := append(early, zbuf.Bytes()...)
	tar := filepath.Join(t.TempDir(), "initramfs-linux.img")
	if err := os.WriteFile(tar, img, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initramfsSaneArch(tar); err != nil {
		t.Fatalf("early-cpio concat image must pass: %v", err)
	}
	// hollow variant: early cpio + zstd WITHOUT systemd must still fail
	hollow := cpioNewc(t, [][2]any{{"usr/lib/systemd/network/99-default.link", 0}, {"usr/bin/busybox", 4}})
	var hbuf bytes.Buffer
	hw, _ := zstd.NewWriter(&hbuf)
	hw.Write(hollow)
	hw.Close()
	himg := append(cpioNewc(t, [][2]any{{"early_cpio", 0}}), hbuf.Bytes()...)
	hpath := filepath.Join(t.TempDir(), "hollow.img")
	if err := os.WriteFile(hpath, himg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initramfsSaneArch(hpath); err == nil {
		t.Fatal("hollow early-cpio concat image must fail")
	}
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
	// usrmerged shape EXACTLY as create_generation assembles it: top-level
	// bin/sbin are DIR links rooted at /kod/generation/current (kod-rooted
	// absolutes, NOT package-relative), init/sh live under usr.
	os.MkdirAll(filepath.Join(gen, "usr/lib/systemd"), 0o755)
	os.MkdirAll(filepath.Join(gen, "usr/bin"), 0o755)
	os.MkdirAll(filepath.Join(gen, "usr/sbin"), 0o755)
	os.MkdirAll(filepath.Join(gen, "usr/lib/modules/6.12-1"), 0o755)
	os.WriteFile(filepath.Join(gen, "usr/lib/systemd/systemd"), []byte("e"), 0o755)
	os.WriteFile(filepath.Join(gen, "usr/bin/sh"), []byte("e"), 0o755)
	os.Symlink("/kod/generation/current/usr/bin", filepath.Join(gen, "bin"))
	os.Symlink("/kod/generation/current/usr/sbin", filepath.Join(gen, "sbin"))
	os.Symlink("/kod/generation/current/usr/lib", filepath.Join(gen, "lib"))
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
	// same kod-rooted links for archModulesDirs (real e2e would otherwise
	// glob a nonexistent <gen>/kod/... and see zero kernel modules)
	mods := archModulesDirs(gen2)
	if len(mods) != 1 || mods[0] != "6.12-1" {
		t.Errorf("archModulesDirs via /kod-rooted lib link = %v, want [6.12-1]", mods)
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
