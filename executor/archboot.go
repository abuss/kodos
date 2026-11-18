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

// tokenVer: alpm/rpm-style tokenization into digit-runs and letter-runs
// (separators . - _ dropped). "7.3.4.arch1" -> [7 3 4 arch 1].
func tokenVer(s string) []string {
	class := func(c byte) int {
		switch {
		case c >= '0' && c <= '9':
			return 1
		case (c|0x20) >= 'a' && (c|0x20) <= 'z':
			return 2
		}
		return 0
	}
	var out []string
	for i := 0; i < len(s); {
		cl := class(s[i])
		if cl == 0 {
			i++
			continue
		}
		j := i
		for j < len(s) && class(s[j]) == cl {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// pkgverTokens: token list of the pin's pkgver (version before the final
// -pkgrel). 7.3.4.arch1-2 -> tokens of 7.3.4.arch1.
func pkgverTokens(version string) []string {
	base := version
	if i := strings.LastIndex(version, "-"); i > 0 {
		base = version[:i]
	}
	return tokenVer(base)
}

// ownsModulesDir reports whether kernel pkgver `base` claims modules dir
// `dir`: dir's tokens start with base's tokens (the dir then adds the
// -archN / -pkgrel decorations). Handles every real arch kernel shape:
// 6.12-1~6.12-1, 6.12.9-1~6.12.9-arch1-1, 7.3.4.arch1-2~7.3.4-arch1-2.
func ownsModulesDir(version, dir string) bool {
	bt, dt := pkgverTokens(version), tokenVer(dir)
	if len(bt) > len(dt) {
		return false
	}
	for i := range bt {
		if bt[i] != dt[i] {
			return false
		}
	}
	return true
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
				cur = filepath.Join(root, tgt)
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
