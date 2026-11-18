package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// archboot.go — the arch boot-side helpers: kernel-pin to modules-dir
// pairing, mkinitcpio image guards, and within-tree symlink resolution for
// the usrmerged generation layout. pacman is never invoked.

type kJob struct {
	Pkg    string
	Flavor string // arch flavor == package name: vmlinuz-<Flavor>, initramfs-<Flavor>.img
	Kver   string // modules dir basename; the arg depmod/mkinitcpio take
}

var archKernelPkgRe = regexp.MustCompile(`^linux(-[a-z]+)*$`)

// ownsModulesDir reports whether kernel pkgver `base` claims modules dir
// `dir`. Arch kernels name the dir from pkgver with the PKGBUILD rule
// (dot->dash only where the dot precedes a NON-digit: 7.3.4.arch1 ->
// 7.3.4-arch1) and the dir may add decorations ONLY after a dash
// (pkgrel/reluffix). A dot continuing past the pkgver means a DEEPER
// version — that false-prefix bit e2e for real: linux-api-headers 7.3-1
// token-prefixed the kernel dir 7.3.4-arch1-2 (dots are structure).
func ownsModulesDir(version, dir string) bool {
	base := version
	if i := strings.LastIndex(version, "-"); i > 0 {
		base = version[:i]
	}
	t := make([]byte, len(base))
	for i := 0; i < len(base); i++ {
		t[i] = base[i]
		if base[i] == '.' && i+1 < len(base) && !(base[i+1] >= '0' && base[i+1] <= '9') {
			t[i] = '-'
		}
	}
	return dir == string(t) || strings.HasPrefix(dir, string(t)+"-")
}

// archKernelJobs pairs every pinned kernel package with the modules dir it
// owns. Arch modules dirs carry the PKGBUILD's -archN reluffix (pin
// linux 6.12.9-1 ships /usr/lib/modules/6.12.9-arch1-1), so pairing matches
// the pkgver prefix. An orphan modules dir means a half-installed kernel —
// error naming it BEFORE any mutation; likewise an ambiguous double claim.
func archKernelJobs(pins map[string]pkgPin, modDirs []string) ([]kJob, error) {
	var names []string
	for n := range pins {
		names = append(names, n)
	}
	sort.Strings(names) // stable job order: sorted by Pkg
	var jobs []kJob
	owner := map[string]string{} // modules dir -> kernel pkg claiming it
	for _, pkg := range names {
		if !archKernelPkgRe.MatchString(pkg) {
			continue
		}
		pv := pins[pkg].Version
		for _, md := range modDirs {
			if !ownsModulesDir(pv, md) {
				continue
			}
			if prev, dup := owner[md]; dup {
				return nil, fmt.Errorf("modules dir %s claimed by both %s and %s (ambiguous kernel pairing)", md, prev, pkg)
			}
			owner[md] = pkg
			jobs = append(jobs, kJob{Pkg: pkg, Flavor: pkg, Kver: md})
		}
	}
	for _, md := range modDirs {
		if _, ok := owner[md]; !ok {
			return nil, fmt.Errorf("modules dir %s has no pinned kernel package", md)
		}
	}
	return jobs, nil
}

// genTreeResolve resolves rel inside the tree rooted at root, following
// symlinks (final component included) but re-anchoring absolute targets at
// root. The usrmerged gen tree's bin/sbin/lib are dir links and store links
// may be chroot-rooted absolutes: RAW host reads either dangle or — worse —
// silently hit the HOST's paths (the host is an Arch-family system).
// Bounded walk guards symlink cycles.
func genTreeResolve(root, rel string) string {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		for depth := 0; depth < 32; depth++ {
			st, err := os.Lstat(cur)
			if err != nil || st.Mode()&os.ModeSymlink == 0 {
				break
			}
			tgt, err := os.Readlink(cur)
			if err != nil {
				break
			}
			if filepath.IsAbs(tgt) {
				// Kod-generation dir links point at SYSTEM paths
				// (/kod/generation/current/usr/bin): while checking gen N,
				// "current" resolves to THIS tree — strip the system root
				// and re-anchor inside. Bare absolutes re-anchor whole.
				if rest, ok := strings.CutPrefix(tgt, "/kod/generation/current/"); ok {
					cur = filepath.Join(root, rest)
				} else {
					cur = filepath.Join(root, tgt)
				}
			} else {
				cur = filepath.Join(filepath.Dir(cur), tgt)
			}
		}
	}
	return cur
}

// archModulesDirs: basenames of the gen tree's /lib/modules entries,
// resolved WITHIN the tree (usrmerged /lib is a dir link; a raw genDir/lib
// glob follows it to an absolute target that lands on the HOST's modules).
func archModulesDirs(genDir string) []string {
	var out []string
	dirs, _ := filepath.Glob(filepath.Join(genTreeResolve(genDir, "lib"), "modules", "*"))
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, filepath.Base(d))
		}
	}
	return out
}

// initramfsSaneArch: arch form of the hollow-image guard. A real mkinitcpio
// image always carries usr/lib/systemd/systemd (switch_root's target); a
// skeleton image boots straight to a "no working init" panic. Compression
// is sniffed (gzip default; zstd/plain tolerated) so a COMPRESS=zstd
// mkinitcpio.conf never false-alarms this guard. bytes.Contains also
// matches "./usr/lib/..."-prefixed cpio name encodings.
func initramfsSaneArch(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var blob []byte
	switch {
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("not gzip: %w", err)
		}
		blob, err = io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return err
		}
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("not zstd: %w", err)
		}
		blob, err = io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return err
		}
	default:
		blob = b // mkinitcpio -c none: plain cpio
	}
	if !bytes.Contains(blob, []byte("usr/lib/systemd/systemd")) {
		return fmt.Errorf("initramfs is skeleton: cpio has no usr/lib/systemd/systemd (%d bytes unpacked)", len(blob))
	}
	return nil
}
