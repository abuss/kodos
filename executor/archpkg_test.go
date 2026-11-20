package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func fixturePkgTzst(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	add := func(name string, mode int64, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode,
			Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, content)
	}
	add(".PKGINFO", 0o644, "pkgname = hello\npkgver = 1.0-1\n")
	add("usr/bin/hello", 0o755, "x")
	add("etc/hello.conf", 0o644, "c")
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestUnpackArchStream(t *testing.T) {
	dst := t.TempDir()
	if err := unpackArchStream(bytes.NewReader(fixturePkgTzst(t)), dst, "hello"); err != nil {
		t.Fatal(err)
	}
	for f, wantPerm := range map[string]os.FileMode{
		".PKGINFO":       0o644,
		"usr/bin/hello":  0o755,
		"etc/hello.conf": 0o644,
	} {
		st, err := os.Stat(filepath.Join(dst, f))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if st.Mode().Perm() != wantPerm {
			t.Errorf("%s mode %o want %o", f, st.Mode().Perm(), wantPerm)
		}
	}
}

func TestArchPkgURL(t *testing.T) {
	got := archPkgURL("https://geo.mirror.pkgbuild.com", "core", "x86_64",
		"glibc", "2.40-1", "x86_64")
	want := "https://geo.mirror.pkgbuild.com/core/os/x86_64/glibc-2.40-1-x86_64.pkg.tar.zst"
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
	// noarch packages live under the dir-arch but filename says "any"
	got = archPkgURL("https://geo.mirror.pkgbuild.com", "core", "x86_64",
		"linux-firmware", "20261019-1", "any")
	want = "https://geo.mirror.pkgbuild.com/core/os/x86_64/linux-firmware-20261019-1-any.pkg.tar.zst"
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestArchArchiveURL(t *testing.T) {
	got := archArchiveURL("https://archive.archlinux.org", "linux", "7.3.4.arch1-2", "x86_64")
	want := "https://archive.archlinux.org/packages/l/linux/linux-7.3.4.arch1-2-x86_64.pkg.tar.zst"
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestInstallPackagesArchArchiveFallback(t *testing.T) {
	pkg := fixturePkgTzst(t)
	sum := sha256.Sum256(pkg)
	mirrorHits, archiveHits := 0, 0

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mirror.Close()
	archive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archiveHits++
		if r.URL.Path != "/packages/h/hello/hello-1.0-1-x86_64.pkg.tar.zst" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(pkg)
	}))
	defer archive.Close()
	oldBase := archArchiveBase
	archArchiveBase = archive.URL
	t.Cleanup(func() { archArchiveBase = oldBase })

	root := t.TempDir()
	e := &executor{s: &system{chroot: root}, arch: "x86_64",
		meta: &metadata{Distro: "arch", Generation: 0, Packages: map[string]pkgPin{
			"hello": {Version: "1.0-1", Checksum: hex.EncodeToString(sum[:]), Repo: "core", Arch: "x86_64"},
		}}}
	e.mirror = mirror.URL
	e.branches = []string{"core"}
	if err := e.installPackages(map[string]any{"names": []any{"hello"}}); err != nil {
		t.Fatal(err)
	}
	if mirrorHits != 1 || archiveHits != 1 {
		t.Errorf("hits mirror=%d archive=%d, want 1 each", mirrorHits, archiveHits)
	}
	if _, err := os.Stat(filepath.Join(root, "kod/store/hello/1.0-1/usr/bin/hello")); err != nil {
		t.Errorf("archive store content missing: %v", err)
	}
}

func TestInstallPackagesArch(t *testing.T) {
	pkg := fixturePkgTzst(t)
	sum := sha256.Sum256(pkg)
	mux := http.NewServeMux()
	mux.HandleFunc("/core/os/x86_64/hello-1.0-1-x86_64.pkg.tar.zst",
		func(w http.ResponseWriter, r *http.Request) { w.Write(pkg) })
	ts := httptest.NewServer(mux)
	defer ts.Close()

	root := t.TempDir()
	e := &executor{s: &system{chroot: root}, arch: "x86_64",
		meta: &metadata{Distro: "arch", Generation: 0, Packages: map[string]pkgPin{
			"hello": {Version: "1.0-1", Checksum: hex.EncodeToString(sum[:]), Repo: "core", Arch: "x86_64"},
		}}}
	e.mirror = ts.URL
	e.branches = []string{"core"}
	if err := e.installPackages(map[string]any{"names": []any{"hello"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "kod/store/hello/1.0-1/usr/bin/hello")); err != nil {
		t.Errorf("store content missing: %v", err)
	}
	if e.resolved["hello"] != "1.0-1" {
		t.Errorf("resolved = %v", e.resolved)
	}

	// a request the lock lacks fails closed
	err := e.installPackages(map[string]any{"names": []any{"ghost"}})
	if err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Errorf("unpinned request must error 'not pinned', got: %v", err)
	}
}

func TestVerifySha256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pkg.tar.zst")
	content := []byte("hello pkg")
	os.WriteFile(p, content, 0o644)
	sum := sha256.Sum256(content)
	if err := verifySha256(p, hex.EncodeToString(sum[:])); err != nil {
		t.Errorf("own hash must pass: %v", err)
	}
	err := verifySha256(p, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("bad hash must fail naming the file, got: %v", err)
	}
}

func TestPkgFetchTimeoutDefault(t *testing.T) {
	os.Unsetenv("KODOS_PKG_FETCH_TIMEOUT")
	if got := pkgFetchTimeout(); got != 20*time.Minute {
		t.Errorf("default = %v, want 20m", got)
	}
}

func TestPkgFetchTimeoutEnvOverride(t *testing.T) {
	os.Setenv("KODOS_PKG_FETCH_TIMEOUT", "45m")
	defer os.Unsetenv("KODOS_PKG_FETCH_TIMEOUT")
	if got := pkgFetchTimeout(); got != 45*time.Minute {
		t.Errorf("override = %v, want 45m", got)
	}
	// Unparseable must fall back to the default, never crash the install.
	os.Setenv("KODOS_PKG_FETCH_TIMEOUT", "not-a-duration")
	if got := pkgFetchTimeout(); got != 20*time.Minute {
		t.Errorf("bad value = %v, want 20m default", got)
	}
}
