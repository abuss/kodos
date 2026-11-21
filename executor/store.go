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
	Distro string // "alpine" (zero value keeps alpine naming) | "arch":
	// only the conf-conflict extension differs (.apknew vs .pacnew)
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

func (s *system) symlinkTree(src, dst string) error {
	if isRealDir(src) {
		if lexists(dst) && !isRealDir(dst) {
			// A package dir landing on another package's symlink merges
			// THROUGH the link, like apk writing into a live root: baselayout
			// ships /var/run -> ../run, avahi/qemu ship /var/run/<subdir>.
			// Replacing the link with a real dir breaks clients that hardcode
			// /var/run (zbus/cosmic-greeter-daemon does; libdbus tools use
			// /run) — that mismatch respawn-looped the greeter on ENOENT.
			dst = s.chrootResolve(dst)
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		// Directory modes are package content: alpine-baselayout ships
		// /tmp + /var/tmp 1777, /root 0700, /var/empty 0555. Flattening
		// them to 0755 makes /tmp unwritable for ordinary users, and
		// start-cosmic's dbus-run-session then fails to create
		// /tmp/dbus-XXXXXX at login.
		if st, err := os.Stat(src); err == nil {
			if err := os.Chmod(dst, st.Mode()); err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := s.symlinkTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if isRealDir(dst) {
		// Reverse order (a dir package linked before the symlink package):
		// the dir wins. Sorted package names make baselayout (the owner of
		// /var/run) precede avahi/qemu, so the merge-through above is what
		// runs in practice.
		return nil
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
	t = s.reanchor(dst, t)
	os.Remove(dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(t, dst)
}

// reanchor keeps a relative package symlink pointing at the same LIVE path
// after it is linked into the generation tree. Generation top-level dirs are
// mounted at the live root (/var -> .../generation/current/var), so a link
// under one of them that points out of that dir lands somewhere else than on
// a plain rootfs: baselayout's /var/run -> ../run would mean
// <generation>/run, while /run is the boot tmpfs. Rewriting such targets to
// the absolute live path is also right inside a chroot of the generation,
// where the generation IS the root. Field proof: zbus hardcodes
// /var/run/dbus/system_bus_socket, so cosmic-greeter-daemon exited with
// InputOutput(NotFound) and respawn-looped without this.
func (s *system) reanchor(dst, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	rel, err := filepath.Rel(s.chroot, dst)
	if err != nil || strings.HasPrefix(rel, "..") {
		return target
	}
	parts := strings.Split(rel, "/") // kod/generation/<N>/<top>/...
	if len(parts) < 5 || parts[0] != "kod" || parts[1] != "generation" {
		return target // not in a generation tree (activation merge, top-level)
	}
	treeRel := strings.Join(parts[3:], "/") // <top>/rest
	resolved := filepath.Clean(filepath.Join(filepath.Dir(treeRel), target))
	if strings.HasPrefix(resolved, "..") ||
		resolved == parts[3] || strings.HasPrefix(resolved, parts[3]+"/") {
		return target // stays inside its own top-level dir: relative is fine
	}
	return "/" + resolved
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
// Removes the partial dst on any failure — no 0-byte relics.
func cowCopy(src, dst string) error {
	// Special files (device nodes, FIFOs, sockets) exist in generation
	// trees (baselayout /dev, daemon run/home dirs) and cowCopy is only
	// reached for them when hardlinking fails cross-device (/home is its
	// own partition). Their "contents" are never the point and reading one
	// blocks forever (eszkoz VM round froze 85 min in epoll here). Skip.
	if st, err := os.Stat(src); err == nil && !st.Mode().IsRegular() {
		fmt.Printf("cowCopy: skipped special file %s (%v)\n", src, st.Mode().Type())
		return nil
	}
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
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
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
		// The LIVE root's /etc is the only record of edits made before the
		// first generation (bootstrap write_file ops land there while no
		// generation exists yet) and of stream writes on later applies. If
		// the live content of a shipped conffile differs from its pristine
		// default, that IS the local edit: live wins. (NanoVM field round:
		// dropping this lost the mkinitfs.conf feature trim, and gen0's
		// chrooted mkinitfs built Alpine's stock 180MB `kms` image.)
		if newSrc != "" {
			if live := filepath.Join(s.chroot, "etc", rel); lexists(live) &&
				!sameContent(newSrc, live) &&
				(baseline == "" || !sameContent(baseline, live)) {
				storeSrc, edited = live, true
			}
		}
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

	// generation/etc: one entry per seed entry, HARDLINKED into the private
	// trees (etc_store wins on collision). Re-runs (reinstall of same idx,
	// retry after aborted create) MUST replace any existing entry — a stale
	// /kod-rooted symlink from a pre-e95af09 shape dangles inside the gen
	// chroot (paste round 5: mkinitfs abort, chown warnings, add-shell error).
	etc := filepath.Join(generation, "etc")
	seeded := map[string]bool{} // rel → true for every entry we (re)link
	for _, tree := range []string{etcStore, etcDefault} {
		paths, err := walkTree(tree)
		if err != nil {
			return err
		}
		for _, f := range paths {
			rel, _ := filepath.Rel(tree, f)
			if seeded[rel] {
				continue // etc_store already linked (it wins on collision)
			}
			seeded[rel] = true
			link := filepath.Join(etc, rel)
			os.MkdirAll(filepath.Dir(link), 0o755)
			os.Remove(link) // force-replace: stale legacy symlinks must not survive
			if err := s.hardlink(f, link); err != nil {
				return err
			}
		}
	}
	// Seed-time stale sweep: remove gen/etc file/symlink entries whose rel
	// is not in the seeded set (leftover from an aborted create or a package
	// that was dropped). Dirs are left alone — empty dirs are harmless.
	filepath.WalkDir(etc, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == etc {
			return nil
		}
		rel, _ := filepath.Rel(etc, p)
		if !seeded[rel] {
			os.Remove(p)
		}
		return nil
	})
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

// storeEntry: the store path behind a generation/etc entry, resolved
// STRUCTURALLY by rel path so it works regardless of whether the gen/etc
// entry is a /kod-rooted symlink (old shape) or a hardlink to the store
// file (current shape from seedEtc). etc_store wins; falls back to
// etc_default. Returns "" when neither exists (hook-created unmanaged file).
func (s *system) storeEntry(src string) string {
	// Derive the generation root: src is <gen>/etc/<rel>; split on "/etc/"
	// taking the left side as the gen root (rel itself never starts with
	// "/etc/" because it is relative).
	idx := strings.LastIndex(src, "/etc/")
	if idx < 0 {
		return "" // unexpected path shape
	}
	gen := src[:idx]
	rel := src[idx+len("/etc/"):]
	for _, base := range []string{"etc_store", "etc_default"} {
		if c := filepath.Join(gen, base, rel); lexists(c) {
			return c
		}
	}
	return "" // hook-created file, no store candidate
}

// installEtc activates one etc entry into the live /etc tree.
//
// Real-file store entries: materialized as a hardlink sharing the
// store inode (rollback bookkeeping, shadow RESOLVE_NO_SYMLINKS compat).
//
// Symlink store entries (openrc's functions.sh -> /usr/libexec/…,
// ssl1.1/certs -> /etc/ssl/certs, mtab -> /proc/self/mounts…): kept
// verbatim as symlinks — the target need not exist at activation time
// (etc merges before top-level dirs like usr/libexec are linked).
//
// Ordering: CREATE-BEFORE-DELETE — we establish the new entry at a tmp
// path first, then rename over; if the tmp creation fails, the existing
// live entry is never touched (atomic-ish replacement on the same fs).
// Hardlink/symlink creation errors are returned (path-annotated) and
// propagated up through activateEtc → activateGeneration.
func (s *system) installEtc(src, dst, rel string) error {
	newP := s.storeEntry(src) // structural store path (etc_store > etc_default)
	if newP == "" {
		return nil // hook-created unmanaged file; activation leaves it
	}

	// Determine what to materialize: symlink store entries stay symlinks;
	// real files become hardlinks (shadow RESOLVE_NO_SYMLINKS compat).
	stSrc, err := os.Lstat(newP)
	if err != nil {
		return fmt.Errorf("installEtc %s: stat store entry: %w", rel, err)
	}
	isLink := stSrc.Mode()&os.ModeSymlink != 0

	// CREATE-BEFORE-DELETE: build the new entry at a sibling tmp path,
	// then rename over. If creation fails, the existing dst is untouched.
	dir := filepath.Dir(dst)
	tmp := dst + ".kodos-new"
	os.Remove(tmp) // clean up any leftover from a previous half-run

	if isLink {
		target, _ := os.Readlink(newP)
		if err := os.Symlink(target, tmp); err != nil {
			return fmt.Errorf("installEtc %s: symlink: %w", rel, err)
		}
	} else {
		if err := s.hardlink(newP, tmp); err != nil {
			return fmt.Errorf("installEtc %s: hardlink: %w", rel, err)
		}
	}

	// Check the existing entry.
	ls, lerr := os.Lstat(dst)
	if lerr == nil {
		if ls.Mode()&os.ModeSymlink != 0 {
			// old-style managed symlink: replace it (rename-over)
			if err := os.Rename(tmp, dst); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("installEtc %s: rename: %w", rel, err)
			}
			return nil
		}
		if !ls.Mode().IsRegular() {
			os.Remove(tmp)
			return nil // dir/fifo/socket: not ours to replace
		}
		if isLink {
			// Store entry is a symlink; existing live entry is a regular
			// file — replace unconditionally (pre-hardlink legacy shape
			// may have content-copied it; now we restore the proper link).
			if err := os.Rename(tmp, dst); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("installEtc %s: rename: %w", rel, err)
			}
			return nil
		}
		// Both are regular files.
		if sameInode(dst, newP) {
			os.Remove(tmp)
			return nil // already hardlinked to this generation
		}
		if sameContent(newP, dst) {
			// Byte-identical: hand it back to the generations.
			if err := os.Rename(tmp, dst); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("installEtc %s: rename: %w", rel, err)
			}
			return nil
		}
		// Content conflict: keep the local version; drop tmp and place
		// the new default as <conflict-ext> (.apknew/.pacnew per distro).
		os.Remove(tmp)
		ext := ".apknew"
		if s.Distro == "arch" {
			ext = ".pacnew"
		}
		base := filepath.Base(dst)
		n := 1
		conflict := filepath.Join(dir, fmt.Sprintf("%s%s-%d", base, ext, n))
		for lexists(conflict) {
			n++
			conflict = filepath.Join(dir, fmt.Sprintf("%s%s-%d", base, ext, n))
		}
		if err := s.hardlink(newP, conflict); err != nil {
			return fmt.Errorf("installEtc %s: %s hardlink: %w", rel, ext, err)
		}
		fmt.Printf("config conflict: kept local %s; new default in %s\n",
			strings.TrimPrefix(dst, s.chroot+"/"),
			strings.TrimPrefix(conflict, s.chroot+"/"))
		return nil
	}

	// No existing entry: simple rename.
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("installEtc %s: rename (fresh): %w", rel, err)
	}
	return nil
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
			if err := s.installEtc(src, dst, rel); err != nil {
				return err
			}
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
// current atomically flips everything that links through it. /root and /home
// are the exception: they are operator state, not system generations.
func (s *system) activateGeneration(generation string) error {
	entries, err := os.ReadDir(generation)
	if err != nil {
		return err
	}
	// Materialize /root and /home BEFORE flipping `current`: an older install
	// has them as symlinks into the outgoing generation, which is where the
	// user's SSH keys / files live. Resolving now preserves that data.
	for _, e := range entries {
		if e.Name() != "root" && e.Name() != "home" {
			continue
		}
		live := filepath.Join(s.chroot, e.Name())
		if isMountpoint(live) {
			continue // separately mounted /home is already persistent
		}
		if err := s.persistHomeDir(filepath.Join(generation, e.Name()), live); err != nil {
			return err
		}
	}

	current := filepath.Join(s.root(), "generation", "current")
	os.Remove(current)
	if err := symlinkChroot(current, generation, s.chroot); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "etc_default" || name == "etc_store" || name == "root" || name == "home" ||
			name == "proc" || name == "sys" {
			// proc/sys: pseudo-fs, never package-owned. Present in a
			// generation tree only as contamination from a crashed round
			// that merged the mounted chroot pseudo-fs (2026-11-20:
			// drivers_probe junk aborted activation) - never merge live.
			continue
		}
		item := filepath.Join(generation, name)
		dst := filepath.Join(s.chroot, name)
		if isRealDir(dst) {
			if isMountpoint(dst) {
				// a mountpoint belongs to the storage ops, not to the
				// generation: never symlink-merge into vfat (/boot ESP) or
				// re-root a live /home. Content reaches it via post-install
				// hooks (kernel -> /boot/vmlinuz+initramfs) and user tools.
				fmt.Printf("note: /%s is a mountpoint: generation does not merge into it\n", name)
				continue
			}
			if name == "etc" {
				if err := s.activateEtc(item, dst); err != nil {
					return err
				}
			} else if err := s.symlinkTree(item, dst); err != nil {
				return err
			}
		} else {
			if err := symlinkChroot(dst, filepath.Join(current, name), s.chroot); err != nil {
				return err
			}
		}
	}
	return nil
}

// persistHomeDir keeps /root and /home as single real dirs on the live root,
// independent of generations. Package-provided files merge in (and update)
// on each activation; user files are never removed. A live path left as a
// symlink by an older kodos is migrated before `current` flips, so the
// outgoing generation's user data survives instead of being orphaned.
func (s *system) persistHomeDir(genDir, live string) error {
	if lexists(live) && !isRealDir(live) {
		old := s.chrootResolve(live) // still resolves through the OLD current
		os.Remove(live)
		if isRealDir(old) {
			if err := s.symlinkTree(old, live); err != nil {
				return err
			}
		}
	}
	if !lexists(genDir) {
		return os.MkdirAll(live, 0o755)
	}
	return s.symlinkTree(genDir, live)
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
			if err := s.symlinkTree(path, filepath.Join(generation, e.Name())); err != nil {
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
		if n, err := strconv.Atoi(e.Name()); err == nil { // strict: "3xyz" is not generation 3
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
