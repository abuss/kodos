package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// archpkg.go — the arch (ALPM side) package-file layer: mirrors fetchUnpack/
// unpackApkStream for .pkg.tar.zst. Trust chain: signed sync DB -> lockfile
// pins (version+sha256) -> verifySha256 here. pacman is never invoked.

// archPkgURL: mirrors the DB's %FILENAME% layout — every tarball lives under
// the DIR arch (os/x86_64) but its name carries its OWN arch component
// (x86_64 or any for noarch packages).
func archPkgURL(mirror, repo, dirArch, name, version, pkgArch string) string {
	return mirror + "/" + repo + "/os/" + dirArch + "/" + name + "-" + version + "-" + pkgArch + ".pkg.tar.zst"
}

// archArchiveBase is the fallback for pinned files that a rolling mirror has
// already dropped. The SHA256 pin is still enforced on whatever comes back.
var archArchiveBase = "https://archive.archlinux.org"

func archArchiveURL(base, name, version, pkgArch string) string {
	return strings.TrimRight(base, "/") + "/packages/" + strings.ToLower(name[:1]) +
		"/" + name + "/" + name + "-" + version + "-" + pkgArch + ".pkg.tar.zst"
}

// pkgFetchTimeout is the per-package HTTP ceiling for Arch package downloads.
// The Alpine-era 5m cap is too tight for a rolling mirror: a kernel from a slow
// mirror or from archive.archlinux.org can be ~130 MB and needs more. Override
// with KODOS_PKG_FETCH_TIMEOUT (a Go duration, e.g. "45m"); unparseable values
// fall back to the default so an env typo can't hang or abort an install.
// ponytail: one ceiling for every package; raise it (or stream in chunks) if a
// single package ever legitimately needs longer than the override allows.
func pkgFetchTimeout() time.Duration {
	if v := os.Getenv("KODOS_PKG_FETCH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 20 * time.Minute
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
