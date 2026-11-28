package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func writeAurArtifact(t *testing.T, path, pkginfo string) {
	t.Helper()
	var raw bytes.Buffer
	zw, err := zstd.NewWriter(&raw)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{
		Name: ".PKGINFO", Mode: 0o644, Size: int64(len(pkginfo)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(pkginfo)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	zw.Close()
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAurPkgInfo(t *testing.T) {
	dir := t.TempDir()
	art := filepath.Join(dir, "visual-studio-code-bin-1.105.0-1-x86_64.pkg.tar.zst")
	writeAurArtifact(t, art, "pkgname = visual-studio-code-bin\npkgver = 1.105.0-1\narch = x86_64\npackager = kodos\n")
	name, ver, arch, err := aurPkgInfo(art)
	if err != nil {
		t.Fatal(err)
	}
	if name != "visual-studio-code-bin" || ver != "1.105.0-1" || arch != "x86_64" {
		t.Fatalf("got %q %q %q", name, ver, arch)
	}
}

func TestAurNeedsBuild(t *testing.T) {
	static := "pkgver=1.105.0\npkgrel=1\n"
	if aurNeedsBuild(static, "1.105.0-1") {
		t.Fatal("static PKGBUILD at stored version must skip the rebuild")
	}
	if !aurNeedsBuild(static, "1.104.2-1") {
		t.Fatal("version bump must rebuild")
	}
	if !aurNeedsBuild(static, "") {
		t.Fatal("nothing stored must build")
	}
	dynamic := "pkgver() { git describe ... }\npkgver=x\npkgrel=1\n"
	if !aurNeedsBuild(dynamic, "x-1") {
		t.Fatal("pkgver() (VCS) functions defeat the grep: must always rebuild")
	}
}
