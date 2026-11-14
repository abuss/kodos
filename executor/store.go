package main

// Generation/store primitives — faithful port of install-apks.py
// (symlink_chroot, symlink_tree, chroot_resolve, seed_etc, install_etc,
// activate_etc, activate_generation, create_generation). The store model:
//   <chroot>/kod/store/<pkg>/<version>/...   package contents (read-only)
//   <chroot>/kod/generation/<N>/            symlink tree + etc_default/etc_store
//   <chroot>/kod/generation/current         -> active generation
// All generated links are chroot-rooted ("/kod/..."): they dangle on the
// host but resolve once this tree is used as /. None of this needs root.

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type system struct {
	chroot string // host path of the target root (e.g. ./rootfs)
}

func (s *system) root() string  { return filepath.Join(s.chroot, "kod") }
func (s *system) store() string { return filepath.Join(s.root(), "store") }
func (s *system) cache() string { return filepath.Join(s.root(), "cache") }
func (s *system) genDir(n int) string {
	return filepath.Join(s.root(), "generation", strconv.Itoa(n))
}

func lexists(p string) bool { _, err := os.Lstat(p); return err == nil }
func isRealDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

// symlinkChroot links dst to "/" + target-relative-to-chroot; unlink does
// not follow links, so this also clears dangling ones.
func symlinkChroot(dst, target, chroot string) error {
	rel, err := filepath.Rel(chroot, target)
	if err != nil {
		return err
	}
	os.Remove(dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink("/"+rel, dst)
}

func symlinkTree(src, dst, chroot string) error {
	if isRealDir(src) {
		if lexists(dst) && !isRealDir(dst) {
			// merge conflict: a real dir beats a package-shipped symlink
			// (e.g. var/run as symlink in baselayout vs files under
			// var/run/ in other packages — apk itself merges through it)
			if err := os.Remove(dst); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := symlinkTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), chroot); err != nil {
				return err
			}
		}
		return nil
	}
	if isRealDir(dst) {
		return nil // merged real dir wins over this package's symlink
	}
	// Regular files become HARDLINKS into the store (store and generation
	// dirs share the filesystem under <chroot>/kod): distro tools assume
	// live paths are real files. mkinitfs' cpio pass-through in particular
	// refuses to collect chroot-rooted symlinks, silently producing an
	// initramfs without busybox or kernel modules. Package-shipped
	// symlinks (applets, .so SONAMEs, cert dirs) stay links, verbatim.
	if _, lerr := os.Readlink(src); lerr != nil {
		os.Remove(dst)
		if err := os.Link(src, dst); err == nil {
			return nil
		}
		return cowCopy(src, dst) // cross-device edge: plain copy
	}
	// Package-shipped symlinks (applets, SONAMEs, cert dirs) are recreated
	// with their target VERBATIM, not pointed through the store: their
	// targets are root-relative in any tree used as /, and a store detour
	// makes mkinitfs copy links into the initramfs that dangle there
	// (no /kod/store inside the image -> /bin/sh broken -> boot panic).
	t, _ := os.Readlink(src)
	os.Remove(dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(t, dst)
}

// chrootResolve resolves p the way the chroot's kernel would: absolute link
// targets are rooted at the chroot, so chains through chroot-rooted links
// (e.g. the current alias) resolve fully on the host.
func (s *system) chrootResolve(p string) string {
	todo := strings.Split(p, "/")
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(s.chroot, p)
		if err != nil {
			return p
		}
		todo = strings.Split(rel, "/")
	}
	var cur []string
	depth := 0
	for len(todo) > 0 {
		part := todo[0]
		todo = todo[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			continue
		}
		cur = append(cur, part)
		host := filepath.Join(append([]string{s.chroot}, cur...)...)
		if st, err := os.Lstat(host); err == nil && st.Mode()&os.ModeSymlink != 0 {
			depth++
			if depth > 40 { // symlink loop
				break
			}
			target, _ := os.Readlink(host)
			cur = cur[:len(cur)-1] // the link itself: its parent is the base
			var next []string
			if strings.HasPrefix(target, "/") {
				next = strings.Split(strings.TrimPrefix(target, "/"), "/") // restart at chroot root
				cur = nil
			} else {
				next = strings.Split(target, "/")
			}
			todo = append(next, todo...)
		}
	}
	if len(cur) == 0 {
		return s.chroot
	}
	return filepath.Join(append([]string{s.chroot}, cur...)...)
}

// sameContent: byte-equal, treating symlinks by their target string.
func sameContent(a, b string) bool {
	la, ea := os.Readlink(a)
	lb, eb := os.Readlink(b)
	if ea == nil && eb == nil {
		return la == lb
	}
	fa, err := os.Stat(a)
	fb, err2 := os.Stat(b)
	if err == nil && err2 == nil && fa.Mode().IsRegular() && fb.Mode().IsRegular() {
		ab, e1 := os.ReadFile(a)
		bb, e2 := os.ReadFile(b)
		return e1 == nil && e2 == nil && string(ab) == string(bb)
	}
	return false
}

// cowCopy: copy src -> dst preserving mode. ponytail: plain copy; these are
// small etc config files. If ever huge, reflink via unix.CopyFileRange.
func cowCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := in.Stat(); err == nil {
		os.Chmod(dst, st.Mode().Perm())
	}
	return nil
}

// copyEtcEntry seeds one entry under etc_default/etc_store. Symlinks are
// recreated verbatim (absolute targets are machine/chroot-rooted, relative
// targets resolve in-place at live time); real files are copied.
func (s *system) copyEtcEntry(src, dst string) error {
	os.Remove(dst)
	if t, err := os.Readlink(src); err == nil {
		return os.Symlink(t, dst)
	}
	real := s.chrootResolve(src)
	if st, err := os.Lstat(real); err == nil && st.Mode().IsRegular() {
		return cowCopy(real, dst)
	}
	return nil // dir: content copied through its children
}

func walkTree(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !isRealDir(p) { // files and symlinks; real dirs carry no content of their own
			files = append(files, p)
		}
		return nil
	})
	slices.Sort(files)
	return files, err
}

// seedEtc builds the generation's private etc_default (pristine upstream
// copies) + etc_store (mutable working copies), preserving local edits,
// then links generation/etc chroot-rooted into the private trees
// (etc_store wins on collision).
func (s *system) seedEtc(generation string, toInstall map[string]string, prevGeneration string) error {
	etcDefault := filepath.Join(generation, "etc_default")
	etcStore := filepath.Join(generation, "etc_store")
	for _, d := range []string{etcDefault, etcStore} {
		os.RemoveAll(d)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(toInstall))
	for p := range toInstall {
		names = append(names, p)
	}
	slices.Sort(names)

	ships := map[string]string{} // first package (sorted) wins: deterministic
	for _, pkg := range names {
		etcSrc := filepath.Join(s.store(), pkg, toInstall[pkg], "etc")
		if !isRealDir(etcSrc) {
			continue
		}
		paths, err := walkTree(etcSrc)
		if err != nil {
			return err
		}
		for _, f := range paths {
			rel, _ := filepath.Rel(etcSrc, f)
			if _, seen := ships[rel]; !seen {
				ships[rel] = f
			}
		}
	}

	stored := map[string]string{}
	prevStore := ""
	if prevGeneration != "" {
		prevStore = filepath.Join(prevGeneration, "etc_store")
		if isRealDir(prevStore) {
			paths, err := walkTree(prevStore)
			if err != nil {
				return err
			}
			for _, f := range paths {
				rel, _ := filepath.Rel(prevStore, f)
				stored[rel] = f
			}
		}
	}
	prevDefault := ""
	if prevGeneration != "" {
		prevDefault = filepath.Join(prevGeneration, "etc_default")
	}

	rels := make([]string, 0, len(ships)+len(stored))
	for r := range ships {
		rels = append(rels, r)
	}
	for r := range stored {
		if _, dup := ships[r]; !dup {
			rels = append(rels, r)
		}
	}
	slices.Sort(rels)

	for _, rel := range rels {
		newSrc, storeSrc := ships[rel], stored[rel]
		var baseline string
		if prevDefault != "" {
			baseline = filepath.Join(prevDefault, rel)
		}
		edited := storeSrc != "" &&
			(baseline == "" || !sameContent(baseline, storeSrc))
		dstDefault := filepath.Join(etcDefault, rel)
		dstStore := filepath.Join(etcStore, rel)
		os.MkdirAll(filepath.Dir(dstDefault), 0o755)
		os.MkdirAll(filepath.Dir(dstStore), 0o755)

		switch {
		case newSrc != "" && !edited:
			s.copyEtcEntry(newSrc, dstDefault)
			s.copyEtcEntry(newSrc, dstStore)
		case newSrc != "":
			// edited: baseline gets the pristine default, working copy keeps the edit
			s.copyEtcEntry(newSrc, dstDefault)
			s.copyEtcEntry(storeSrc, dstStore)
			if baseline != "" && lexists(baseline) && !sameContent(baseline, newSrc) {
				dir := filepath.Dir(filepath.Join(etcDefault, rel))
				base := filepath.Base(rel)
				for n := 1; ; n++ {
					name := fmt.Sprintf("%s.apknew-%d", base, n)
					if !lexists(filepath.Join(dir, name)) {
						s.copyEtcEntry(newSrc, filepath.Join(dir, name))
						fmt.Printf("config conflict: kept local edit of etc/%s; new default in etc_default/%s\n",
							rel, filepath.Join(filepath.Dir(rel), name))
						break
					}
				}
			}
		case edited:
			// upstream dropped it but it was edited: carry the edit as new baseline
			s.copyEtcEntry(storeSrc, dstDefault)
			s.copyEtcEntry(storeSrc, dstStore)
		}
		// else: shipped by nobody, never edited -> activation prunes it
	}

	// generation/etc: one chroot-rooted link per entry, etc_store first
	etc := filepath.Join(generation, "etc")
	for _, tree := range []string{etcStore, etcDefault} {
		paths, err := walkTree(tree)
		if err != nil {
			return err
		}
		for _, f := range paths {
			rel, _ := filepath.Rel(tree, f)
			link := filepath.Join(etc, rel)
			if lexists(link) {
				continue // etc_store already linked (it wins)
			}
			if err := symlinkChroot(link, f, s.chroot); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameInode reports whether both paths are the same underlying file.
func sameInode(a, b string) bool {
	as, err := os.Lstat(a)
	if err != nil {
		return false
	}
	bs, err := os.Lstat(b)
	if err != nil {
		return false
	}
	return os.SameFile(as, bs)
}

// etcLinked: a live etc entry materialized from a generation's store: an
// old-style symlink into /kod, or a hardlink sharing an inode with some
// generation's etc_store copy. Anything else was created or edited locally
// and must never be replaced silently (NixOS/Guix rule).
func (s *system) etcLinked(dst, rel string) bool {
	if t, err := os.Readlink(dst); err == nil {
		return strings.HasPrefix(t, "/kod/") // legacy symlink shape
	}
	// ponytail: scans every kept generation's store; few generations, tiny
	// fan-out, fine. A per-generation manifest would be the upgrade.
	ms, _ := filepath.Glob(filepath.Join(s.root(), "generation", "*", "etc_store", rel))
	for _, m := range ms {
		if sameInode(dst, m) {
			return true
		}
	}
	return false
}

// hardlink materializes target at dst sharing its inode: a plain real file
// to every distro tool, while staying pinned to the generation's copy.
func (s *system) hardlink(target, dst string) error {
	if err := os.Link(target, dst); err != nil {
		return cowCopy(target, dst) // cross-device store: fall back to a copy
	}
	return nil
}

// storeEntry: the store path behind a generation/etc link, without chasing
// the entry's own final target: hardlinking THAT inode (a real file, or a
// verbatim package symlink like ssl1.1/certs -> /etc/ssl/certs) is what
// materializes the entry in live /etc.
func (s *system) storeEntry(src string) string {
	if t, err := os.Readlink(src); err == nil && strings.HasPrefix(t, "/") {
		return filepath.Join(s.chroot, strings.TrimPrefix(t, "/"))
	}
	return s.chrootResolve(src)
}

// installEtc activates one etc entry as a hardlink into the generation's
// store copy. Live /etc entries must be real files: shadow 4.18 opens
// /etc/passwd & friends via openat2(RESOLVE_NO_SYMLINKS) and refuses to
// even follow a symlink chain there (ELOOP "Symbolic link loop"), while
// the real kodos machine (and every package post-install) treats /etc as
// plain mutable files inside the generation's rootfs snapshot. Hardlinks
// keep that behavior while preserving the shared-inode rollback bookkeeping.
func (s *system) installEtc(src, dst, rel string) {
	newP := s.storeEntry(src) // the store entry (real file or verbatim link)
	ls, lerr := os.Lstat(dst)
	if lerr != nil {
		s.hardlink(newP, dst) // fresh entry
		return
	}
	if ls.Mode()&os.ModeSymlink != 0 {
		os.Remove(dst) // old-style managed symlink: migrate to hardlink
		s.hardlink(newP, dst)
		return
	}
	if !ls.Mode().IsRegular() {
		return // dir/fifo/socket: not ours to replace
	}
	if sameInode(dst, newP) {
		return // already hardlinked to this generation
	}
	if sameContent(newP, dst) {
		os.Remove(dst)
		s.hardlink(newP, dst) // byte-identical: hand it back to the generations
		return
	}
	base := filepath.Base(dst)
	dir := filepath.Dir(dst)
	n := 1
	conflict := filepath.Join(dir, fmt.Sprintf("%s.apknew-%d", base, n))
	for lexists(conflict) {
		n++
		conflict = filepath.Join(dir, fmt.Sprintf("%s.apknew-%d", base, n))
	}
	if st, err := os.Lstat(newP); err == nil && st.Mode().IsRegular() {
		cowCopy(newP, conflict)
	}
	fmt.Printf("config conflict: kept local %s; new default in %s\n",
		strings.TrimPrefix(dst, s.chroot+"/"), strings.TrimPrefix(conflict, s.chroot+"/"))
}

func (s *system) activateEtc(generationEtc, etc string) error {
	paths, err := walkTree(generationEtc)
	if err != nil {
		return err
	}
	for _, src := range paths {
		rel, _ := filepath.Rel(generationEtc, src)
		dst := filepath.Join(etc, rel)
		// gen/etc leaves are links into the store; dirs are real dirs
		if st, err := os.Lstat(src); err == nil && st.IsDir() {
			if s.etcLinked(dst, rel) {
				os.Remove(dst) // managed file entry, now ships as a dir
			}
			os.MkdirAll(dst, 0o755)
		} else {
			os.MkdirAll(filepath.Dir(dst), 0o755)
			s.installEtc(src, dst, rel)
		}
	}

	// prune deepest-first: managed links whose entry vanished are safe to
	// remove; user files/dirs are never managed links
	emptied := map[string]bool{}
	var live []string
	filepath.WalkDir(etc, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != etc {
			live = append(live, p)
		}
		return nil
	})
	slices.SortFunc(live, func(a, b string) int {
		return strings.Count(b, "/") - strings.Count(a, "/") // deepest first
	})
	for _, item := range live {
		rel, _ := filepath.Rel(etc, item)
		if s.etcLinked(item, rel) && !lexists(filepath.Join(generationEtc, rel)) {
			os.Remove(item)
			fmt.Printf("removed %s (no longer in the generation)\n", strings.TrimPrefix(item, s.chroot+"/"))
			emptied[filepath.Dir(item)] = true
		}
	}
	for changed := true; changed; {
		changed = false
		dirs := make([]string, 0, len(emptied))
		for d := range emptied {
			dirs = append(dirs, d)
		}
		slices.SortFunc(dirs, func(a, b string) int {
			return strings.Count(b, "/") - strings.Count(a, "/")
		})
		for _, d := range dirs {
			rel, _ := filepath.Rel(etc, d)
			if d == etc || lexists(filepath.Join(generationEtc, rel)) || dirNotEmpty(d) {
				continue
			}
			os.Remove(d)
			delete(emptied, d)
			emptied[filepath.Dir(d)] = true
			changed = true
		}
	}
	return nil
}

func dirNotEmpty(d string) bool {
	f, err := os.Open(d)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.ReadDir(1)
	return err == nil
}

// activateGeneration points current at the generation and flips the live
// root: top-level dirs merge (etc) or relink (the rest), so repointing
// current atomically flips everything that links through it.
func (s *system) activateGeneration(generation string) error {
	current := filepath.Join(s.root(), "generation", "current")
	os.Remove(current)
	if err := symlinkChroot(current, generation, s.chroot); err != nil {
		return err
	}
	entries, err := os.ReadDir(generation)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "etc_default" || e.Name() == "etc_store" {
			continue // private bookkeeping, not part of the live root
		}
		item := filepath.Join(generation, e.Name())
		dst := filepath.Join(s.chroot, e.Name())
		if isRealDir(dst) {
			if isMountpoint(dst) {
				// a mountpoint belongs to the storage ops, not to the
				// generation: never symlink-merge into vfat (/boot ESP) or
				// re-root a live /home. Content reaches it via post-install
				// hooks (kernel -> /boot/vmlinuz+initramfs) and user tools.
				fmt.Printf("note: /%s is a mountpoint: generation does not merge into it\n", e.Name())
				continue
			}
			if e.Name() == "etc" {
				if err := s.activateEtc(item, dst); err != nil {
					return err
				}
			} else if err := symlinkTree(item, dst, s.chroot); err != nil {
				return err
			}
		} else {
			if err := symlinkChroot(dst, filepath.Join(current, e.Name()), s.chroot); err != nil {
				return err
			}
		}
	}
	return nil
}

// createGeneration links the store contents of toInstall into a generation
// tree (skipping .-metadata and etc, which seedEtc copies instead).
func (s *system) createGeneration(toInstall map[string]string, idx int, prevGeneration string) (string, error) {
	generation := s.genDir(idx)
	if err := os.MkdirAll(generation, 0o755); err != nil {
		return "", err
	}
	names := make([]string, 0, len(toInstall))
	for p := range toInstall {
		names = append(names, p)
	}
	slices.Sort(names)
	// Guard for packages shipping top-level dirs as RELATIVE symlinks
	// (merged-usr style): linking them verbatim would resolve them inside
	// the package instead of the generation's merged tree. (Alpine 3.24
	// does NOT do this — the /bin/sh ENOENT that motivated this was
	// busybox creating applet links in its post-install hook — but the
	// shape is legal for packages, so the guard stays.)
	type dirAlias struct{ name, target string }
	var aliases []dirAlias
	for _, pkg := range names {
		src := filepath.Join(s.store(), pkg, toInstall[pkg])
		if !isRealDir(src) {
			return "", fmt.Errorf("create_generation: %s not in store (install_packages must fetch it first)", pkg)
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "etc" {
				continue // apk metadata / private etc trees
			}
			path := filepath.Join(src, e.Name())
			if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
				if ln, err := os.Readlink(path); err == nil && !strings.HasPrefix(ln, "/") {
					aliases = append(aliases, dirAlias{e.Name(), ln})
					continue
				}
			}
			if err := symlinkTree(path, filepath.Join(generation, e.Name()), s.chroot); err != nil {
				return "", err
			}
		}
	}
	for _, a := range aliases {
		dst := filepath.Join(generation, a.name)
		if lexists(dst) {
			continue // some package ships a real dir there: it wins
		}
		// resolved through `current` so every generation's alias follows its
		// own merged tree, before and after rollback
		if err := os.Symlink("/kod/generation/current/"+strings.TrimPrefix(a.target, "./"), dst); err != nil {
			return "", err
		}
	}
	if err := s.seedEtc(generation, toInstall, prevGeneration); err != nil {
		return "", err
	}
	return generation, nil
}

// pruneGenerations removes old generation directories based on keep cap,
// protecting all generations in protect (live current + newly created idx).
// For each pruned generation, GCs store entries not referenced by any survivor.
func (s *system) pruneGenerations(keep int, protect []int) error {
	if keep < 1 {
		keep = 1
	}

	genRoot := filepath.Join(s.root(), "generation")
	entries, err := os.ReadDir(genRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var existingGens []int
	for _, e := range entries {
		if e.Name() == "current" || e.Name() == "etc" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "%d", &n); err == nil {
			existingGens = append(existingGens, n)
		}
	}
	if len(existingGens) == 0 {
		return nil
	}

	survivors := prunePlan(existingGens, keep, protect)
	survivorMap := make(map[int]bool)
	for _, g := range survivors {
		survivorMap[g] = true
	}

	// Build set of pkg@version referenced by ALL survivors (for store GC)
	survivorRefs := map[string]bool{}
	for _, g := range survivors {
		refs, ok := readGenPackages(s.genDir(g))
		if !ok {
			// No generation.json: conservatively keep everything this gen might reference
			// by skipping GC entirely (we cannot know what it owns)
			return s.pruneGenDirs(existingGens, survivorMap)
		}
		for ref := range refs {
			survivorRefs[ref] = true
		}
	}

	// Collect pkg@version refs owned by pruned gens (but not survivors)
	pruneRefs := map[string]bool{}
	for _, g := range existingGens {
		if survivorMap[g] {
			continue
		}
		refs, ok := readGenPackages(s.genDir(g))
		if !ok {
			continue // conservative: skip store GC for this gen
		}
		for ref := range refs {
			if !survivorRefs[ref] {
				pruneRefs[ref] = true
			}
		}
	}

	// Remove pruned generation dirs
	if err := s.pruneGenDirs(existingGens, survivorMap); err != nil {
		return err
	}

	// GC store entries only referenced by pruned gens
	for ref := range pruneRefs {
		pkg, ver, ok := strings.Cut(ref, "@")
		if !ok {
			continue
		}
		storeDir := filepath.Join(s.store(), pkg, ver)
		if err := os.RemoveAll(storeDir); err != nil {
			return fmt.Errorf("store GC %s: %w", ref, err)
		}
	}

	return nil
}

// pruneGenDirs removes generation directories not in survivorMap.
func (s *system) pruneGenDirs(existingGens []int, survivorMap map[int]bool) error {
	for _, g := range existingGens {
		if !survivorMap[g] {
			if err := os.RemoveAll(s.genDir(g)); err != nil {
				return fmt.Errorf("remove generation %d: %w", g, err)
			}
		}
	}
	return nil
}

// readGenPackages reads generation.json from a gen dir and returns "pkg@ver" set.
// Returns (nil, false) if generation.json is missing or unparseable.
func readGenPackages(genDir string) (map[string]bool, bool) {
	b, err := os.ReadFile(filepath.Join(genDir, "generation.json"))
	if err != nil {
		return nil, false
	}
	var manifest struct {
		Packages map[string]struct{ Version string } `json:"packages"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, false
	}
	refs := make(map[string]bool, len(manifest.Packages))
	for name, pin := range manifest.Packages {
		refs[name+"@"+pin.Version] = true
	}
	return refs, true
}
