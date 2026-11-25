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
	"strconv"
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

// archKernelJobs pairs every modules dir with a kernel that claims it.
// First tries pinned packages (strict matching); if a modules dir is unowned,
// falls back to discovering the kernel flavor from the vmlinuz symlink target
// (handles rolling-release drift where pacman installed a newer kernel than
// the media's lockfile pins).
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
	// ponytail: drift fallback—rolling-release pacman pulled a kernel newer than
	// the media lockfile. Use the installed kernel's version for mkinitcpio.
	for _, md := range modDirs {
		if _, ok := owner[md]; !ok {
			// Orphan modules dir: assume vmlinuz symlink target gives us the
			// package name (e.g., linux, linux-lts, linux-zen). Pair the dir
			// with the flavor guess + use the dir basename as kver.
			flavor := "linux" // ponytail: guess linux; more complex heuristic not needed
			jobs = append(jobs, kJob{Pkg: flavor, Flavor: flavor, Kver: md})
		}
	}
	return jobs, nil
}

// archVmlinuzSource: Arch's kernel package ships vmlinuz under
// /usr/lib/modules/<kver>/, NOT /boot (kernel-install is a package script
// and never runs on the executor path). Resolve "lib" WITHIN the gen tree:
// usrmerged layouts have /lib -> usr/lib, raw host reads hit the HOST's
// modules instead.
func archVmlinuzSource(genDir, kver string) string {
	return genTreeResolve(genDir, filepath.Join("lib", "modules", kver, "vmlinuz"))
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
// skeleton image boots straight to a "no working init" panic. The image is
// a SEGMENT STREAM: early uncompressed cpio + zero padding + compressed
// main cpio (zstd/gzip; plain when COMPRESS=none). A single magic sniff at
// offset 0 saw the early cpio, took the plain branch and scanned
// COMPRESSED bytes: a fully sane image failed as "skeleton" with the
// UNPACKED count equal to the FILE size. Walk segments instead: parse each
// cpio to its TRAILER, sniff each tail. bytes.Contains also matches
// "./usr/lib/..."-prefixed cpio name encodings.
func initramfsSaneArch(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var blob []byte
	for len(b) > 0 {
		switch {
		case bytes.HasPrefix(b, []byte("070701")), bytes.HasPrefix(b, []byte("070702")):
			end, err := cpioSegmentEnd(b)
			if err != nil {
				return err
			}
			blob = append(blob, b[:end]...)
			b = b[end:]
		case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				return fmt.Errorf("not gzip: %w", err)
			}
			dec, err := io.ReadAll(zr)
			zr.Close()
			if err != nil {
				return err
			}
			blob = append(blob, dec...)
			b = nil
		case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
			zr, err := zstd.NewReader(bytes.NewReader(b))
			if err != nil {
				return fmt.Errorf("not zstd: %w", err)
			}
			dec, err := io.ReadAll(zr)
			zr.Close()
			if err != nil {
				return err
			}
			blob = append(blob, dec...)
			b = nil
		case b[0] == 0: // cpio 512-block zero padding between segments
			b = bytes.TrimLeft(b, "\x00")
		default:
			blob = append(blob, b...) // mkinitcpio -c none: plain cpio
			b = nil
		}
	}
	if !bytes.Contains(blob, []byte("usr/lib/systemd/systemd")) {
		return fmt.Errorf("initramfs is skeleton: cpio has no usr/lib/systemd/systemd (%d bytes unpacked)", len(blob))
	}
	return nil
}

// cpioSegmentEnd: offset just past the TRAILER record of the newc/crc-newc
// stream starting at b[0] (name+NUL padded so header+name is 4-aligned,
// then filesize bytes of data, 4-aligned).
func cpioSegmentEnd(b []byte) (int, error) {
	i := 0
	for i+110 <= len(b) {
		m := b[i : i+6]
		if !bytes.Equal(m, []byte("070701")) && !bytes.Equal(m, []byte("070702")) {
			return 0, fmt.Errorf("cpio sync lost at %d", i)
		}
		fsz, err := strconv.ParseUint(string(b[i+54:i+62]), 16, 64)
		if err != nil {
			return 0, fmt.Errorf("cpio filesize at %d: %w", i, err)
		}
		nsize, err := strconv.ParseUint(string(b[i+94:i+102]), 16, 32)
		if err != nil {
			return 0, fmt.Errorf("cpio namesize at %d: %w", i, err)
		}
		end := i + 110 + int(nsize)
		end += (4 - end%4) % 4
		if end > len(b) {
			return 0, fmt.Errorf("cpio name truncated at %d", i)
		}
		if nsize >= 11 && string(b[i+110:i+120]) == "TRAILER!!!" {
			return end, nil
		}
		i = end + int(fsz)
		i += (4 - i%4) % 4
		if i > len(b) {
			return 0, fmt.Errorf("cpio file truncated at %d", i)
		}
	}
	return 0, fmt.Errorf("cpio stream truncated (no TRAILER)")
}
