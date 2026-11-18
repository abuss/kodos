package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// archpkg.go — the arch (ALPM side) package-file layer: mirrors fetchUnpack/
// unpackApkStream for .pkg.tar.zst. Trust chain: signed sync DB -> lockfile
// pins (version+sha256) -> verifySha256 here. pacman is never invoked.

func archPkgURL(mirror, repo, arch, name, version string) string {
	return mirror + "/" + repo + "/os/" + arch + "/" + name + "-" + version + ".pkg.tar.zst"
}

func verifySha256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", path, got, want)
	}
	return nil
}

// unpackArchStream unzstd's a .pkg.tar.zst stream into dst.
func unpackArchStream(body io.Reader, dst, name string) error {
	zr, err := zstd.NewReader(body)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", name, err)
	}
	defer zr.Close()
	return unpackTarStream(zr, dst, name)
}
