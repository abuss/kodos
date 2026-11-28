package main

// aur.go — user-level AUR builds (#13, v1). Flow: aurweb git clone + makepkg
// run AS THE REQUESTING USER (makepkg refuses root; the issue's build-user
// idea is covered by whatever user the config's users section carries).
// No helper binary: yay/paru wrap exactly this clone+makepkg dance, and
// their sudo-driven dep installs cannot work in a non-TTY apply. v1 rule:
// PKGBUILD makedepends must already be system packages — makepkg runs
// WITHOUT -s, so a missing one fails loudly naming it, and the operator
// declares it in the system packages list.
//
// Built artifacts (.pkg.tar.zst) are harvested into the ordinary store and
// pinned into the generation metadata (repo="aur", sha256 of the artifact),
// so generation linking, pruning and rollback treat them like any package.
//
// Skip heuristic: static PKGBUILDs rebuild only when their pkgver-pkgrel
// differs from the stored version; a pkgver() function (VCS pkgs) always
// rebuilds. ponytail: -bin packages whose payload changes under an unchanged
// pkgver are NOT re-harvested; delete the store entry to force.

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// aurPkgInfo reads name/version/arch from the .PKGINFO inside a
// .pkg.tar.zst artifact.
func aurPkgInfo(path string) (name, version, arch string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return "", "", "", fmt.Errorf("%s: not zstd: %w", path, err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", "", fmt.Errorf("read %s: %w", path, err)
		}
		if base := filepath.Base(hdr.Name); base != ".PKGINFO" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return "", "", "", err
		}
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, " = ")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "pkgname":
				name = v
			case "pkgver":
				version = v
			case "arch":
				arch = v
			}
		}
	}
	if name == "" || version == "" {
		return "", "", "", fmt.Errorf("%s: .PKGINFO lacks pkgname/pkgver", path)
	}
	return name, version, arch, nil
}

var (
	pkgverRe = regexp.MustCompile(`(?m)^pkgver=(\S+)`)
	pkgrelRe = regexp.MustCompile(`(?m)^pkgrel=(\S+)`)
)

// aurNeedsBuild: rebuild unless the PKGBUILD is static AND its pkgver-pkgrel
// matches the version already in the store.
func aurNeedsBuild(pkgbuild, storedVersion string) bool {
	if storedVersion == "" {
		return true
	}
	if strings.Contains(pkgbuild, "pkgver()") {
		return true // dynamic (VCS) version: the grep below cannot see it
	}
	pv, pr := pkgverRe.FindStringSubmatch(pkgbuild), pkgrelRe.FindStringSubmatch(pkgbuild)
	if pv == nil || pr == nil {
		return true
	}
	return pv[1]+"-"+pr[1] != storedVersion
}

func (e *executor) userExists(name string) bool {
	b, err := os.ReadFile(filepath.Join(e.s.chroot, "etc", "passwd"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if u, _, ok := strings.Cut(l, ":"); ok && u == name {
			return true
		}
	}
	return false
}

func (e *executor) installAurPackages(act map[string]any) error {
	names := strList(act, "names")
	if len(names) == 0 {
		return nil
	}
	if e.meta == nil || e.meta.Distro != "arch" {
		fmt.Printf("WARNING: install_aur_packages: AUR is arch-only — skipped %d\n", len(names))
		return nil
	}
	builders := map[string]string{}
	if m, ok := act["users"].(map[string]any); ok {
		for k, v := range m {
			builders[k], _ = v.(string)
		}
	}
	helper := field(act, "helper")

	for _, name := range names {
		user := builders[name]
		if user == "" || user == "root" {
			// v1 is user-level AUR only (resolve.aur_packages enforces this
			// in the generator); root builds would abort makepkg anyway.
			fmt.Printf("WARNING: install_aur_packages: %s has no builder user — skipped (user-level AUR only)\n", name)
			continue
		}
		if !e.userExists(user) {
			// create_user runs LATER in the stream: provision the builder now
			// (useradd's configured shell, locked password — su from root works).
			if out, err := e.chrootShOut(fmt.Sprintf("useradd -m %s", user)); err != nil {
				return fmt.Errorf("aur %s: create builder %s: %s", name, user, out)
			}
		}
		if helper != "" && helper != "yay" && helper != "paru" {
			return fmt.Errorf("aur: unsupported helper %q (yay/paru both reduce to git+makepkg)", helper)
		}

		repoDir := fmt.Sprintf("/home/%s/.kodos-aur/%s", user, name)
		clone := fmt.Sprintf("su - %s -c \"mkdir -p .kodos-aur && (git clone --depth=1 https://aur.archlinux.org/%s.git .kodos-aur/%s 2>/dev/null || git -C .kodos-aur/%s pull --ff-only)\"", user, name, name, name)
		if out, err := e.chrootShOut(clone); err != nil {
			fmt.Printf("WARNING: aur %s: clone/update failed: %s\n", name, strings.TrimSpace(out))
			continue
		}

		abs := filepath.Join(e.s.chroot, strings.TrimPrefix(repoDir, "/"))
		pb, _ := os.ReadFile(filepath.Join(abs, "PKGBUILD"))
		verDir := filepath.Join(e.s.store(), name)
		stored := ""
		if ents, err := os.ReadDir(verDir); err == nil && len(ents) > 0 {
			stored = ents[len(ents)-1].Name() // store holds one version per pkg under AUR harvest
		}
		if !aurNeedsBuild(string(pb), stored) {
			fmt.Printf("aur %s: up to date (%s)\n", name, stored)
			e.pinAur(name, stored, "")
			continue
		}

		// --nodeps: makepkg's dep check shells out to `pacman -T`, which is
		// fatal in a kodos world (no /var/lib/pacman db — kodos IS the
		// package manager). v1 rule stands: makedepends must be declared as
		// system packages; a missing one fails during the build itself.
		build := fmt.Sprintf("su - %s -c \"cd .kodos-aur/%s && makepkg -c --noconfirm --skipinteg --ignorearch --nodeps\"", user, name)
		fmt.Printf("  aur build %s as %s\n", name, user)
		if out, err := e.chrootShOut(build); err != nil {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) > 6 {
				lines = lines[len(lines)-6:]
			}
			fmt.Printf("WARNING: aur %s: build failed:\n%s\n", name, strings.Join(lines, "\n"))
			continue
		}

		arts, _ := filepath.Glob(filepath.Join(abs, "*.pkg.tar.zst"))
		if len(arts) == 0 {
			fmt.Printf("WARNING: aur %s: makepkg produced no artifact\n", name)
			continue
		}
		for _, art := range arts {
			base := filepath.Base(art)
			if strings.HasSuffix(base, "-debug.pkg.tar.zst") || strings.Contains(base, "-debug-") {
				continue // debug splits bloat every generation for nobody
			}
			pname, pver, parch, err := aurPkgInfo(art)
			if err != nil {
				return fmt.Errorf("aur harvest: %w", err)
			}
			b, err := os.ReadFile(art)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			checksum := hex.EncodeToString(sum[:])
			dst := filepath.Join(e.s.store(), pname, pver)
			if !isRealDir(dst) {
				rf, err := os.Open(art)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(dst, 0o755); err != nil {
					rf.Close()
					return err
				}
				err = unpackArchStream(rf, dst, pname)
				rf.Close()
				if err != nil {
					return fmt.Errorf("aur harvest %s: %w", base, err)
				}
				fmt.Printf("  aur harvested %s %s\n", pname, pver)
			}
			e.pinAur(pname, pver, checksum)
			_ = parch
		}
	}
	return nil
}

// pinAur records the built package in the generation metadata so linking,
// manifest and pruning treat it like any other package. An existing pin
// (same name, pinned by the lock stage — cannot happen for pure AUR names)
// is not overwritten.
func (e *executor) pinAur(name, version, checksum string) {
	if _, ok := e.meta.Packages[name]; ok && e.meta.Packages[name].Repo != "aur" {
		return
	}
	e.meta.Packages[name] = pkgPin{Version: version, Checksum: checksum, Repo: "aur", Arch: "x86_64"}
	if e.resolved != nil {
		e.resolved[name] = version
	}
}
