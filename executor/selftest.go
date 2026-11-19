package main

// selftest exercises the ported generation model in a scratch dir with a
// fake store: create -> activate -> edit through the live tree -> recreate
// with a changed upstream default -> re-activate. It asserts the contract
// that matters: edits survive, pristine baselines update, conflicts get
// .apknew files, vanished entries are pruned, live etc entries are real
// hardlinks into the generation store, and top-level links are
// chroot-rooted and resolve through `current`. Run: kodos-exec selftest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func check(cond bool, msg string, args ...any) {
	if !cond {
		fmt.Printf("SELFTEST FAIL: "+msg+"\n", args...)
		os.Exit(1)
	}
}

func readOr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func selftest() {
	chroot, err := os.MkdirTemp("", "kodos-selftest-")
	check(err == nil, "tempdir: %v", err)
	defer os.RemoveAll(chroot)
	s := &system{chroot: chroot}

	write := func(p, content string) {
		check(os.MkdirAll(filepath.Dir(p), 0o755) == nil, "mkdir %s", p)
		check(os.WriteFile(p, []byte(content), 0o644) == nil, "write %s", p)
	}

	// relative top-level symlinks (merged-usr shape) must alias the
	// generation tree, not the package (guard; Alpine 3.24 does not use
	// this shape — busybox ships real dirs and creates links post-install)
	write(filepath.Join(s.store(), "00base", "1.0", "usr", "bin", "b"), "B0\n")
	write(filepath.Join(s.store(), "00base", "1.0", "usr", "sbin", "s"), "S\n")
	check(os.Symlink("usr/bin", filepath.Join(s.store(), "00base", "1.0", "bin")) == nil, "fake bin symlink")
	check(os.Symlink("usr/sbin", filepath.Join(s.store(), "00base", "1.0", "sbin")) == nil, "fake sbin symlink")
	write(filepath.Join(s.store(), "pkgA", "1.0", "bin", "a"), "A1\n")
	write(filepath.Join(s.store(), "pkgA", "1.0", "etc", "foo"), "upstream\n")
	// symlink etc entries: (a) absolute target into a dir not yet existing
	// at activate time (/usr/libexec/rc — style), (b) relative link
	check(os.Symlink("/usr/libexec/rc/sh/functions.sh",
		filepath.Join(s.store(), "pkgA", "1.0", "etc", "abslink")) == nil, "abslink store")
	check(os.Symlink("../proc/self/mounts",
		filepath.Join(s.store(), "pkgA", "1.0", "etc", "rellink")) == nil, "rellink store")
	write(filepath.Join(s.store(), "pkgB", "2.0", "bin", "b"), "B\n")
	write(filepath.Join(s.store(), "pkgB", "2.0", "etc", "bar"), "bar\n")
	// apk-style merge-through: package content under another package's
	// symlink must merge THROUGH it (baselayout var/run -> ../run vs
	// avahi/qemu shipping var/run/<subdir>; zbus hardcodes /var/run).
	check(os.MkdirAll(filepath.Join(s.store(), "00base", "1.0", "var"), 0o755) == nil, "fake var dir")
	check(os.Symlink("../run", filepath.Join(s.store(), "00base", "1.0", "var", "run")) == nil, "fake var/run symlink")
	write(filepath.Join(s.store(), "pkgA", "1.0", "var", "run", "avahi-daemon", "pid"), "7\n")
	// alpine-baselayout ships /tmp and /var/tmp as 1777; dir modes must
	// survive into the generation (dbus-run-session needs a writable /tmp)
	fakeTmp := filepath.Join(s.store(), "00base", "1.0", "tmp")
	check(os.MkdirAll(fakeTmp, 0o777) == nil, "fake tmp dir")
	check(os.Chmod(fakeTmp, os.ModeSticky|0o777) == nil, "fake tmp sticky")
	check(os.MkdirAll(filepath.Join(chroot, "etc"), 0o755) == nil, "live etc")

	// gen 0
	g0, err := s.createGeneration(map[string]string{"pkgA": "1.0", "pkgB": "2.0", "00base": "1.0"}, 0, "")
	check(err == nil, "create gen0: %v", err)

	// Write generation.json using real stampGeneration function
	// For selftest, provide a minimal but real metadata file content as bytes to test determinism
	metaContent := []byte(`{"schema_version":1,"generation":0,"distro":"alpine","packages":{"pkgA":{"version":"1.0","checksum":""},"pkgB":{"version":"2.0","checksum":""},"00base":{"version":"1.0","checksum":""}}}`)
	meta0 := &metadata{
		SchemaVersion: 1,
		Generation:    0,
		Distro:        "alpine",
		Packages: map[string]pkgPin{
			"pkgA":   {Version: "1.0", Checksum: ""},
			"pkgB":   {Version: "2.0", Checksum: ""},
			"00base": {Version: "1.0", Checksum: ""},
		},
	}
	check(stampGeneration(g0, meta0, []string{}, metaContent, nil) == nil, "stamp generation 0")

	// generation.json must exist with metadata + executor stamps
	genJsonPath := filepath.Join(g0, "generation.json")
	check(lexists(genJsonPath), "generation.json missing at %s", genJsonPath)
	var genJson *metadata
	genJsonBytes, err := os.ReadFile(genJsonPath)
	check(err == nil, "read generation.json: %v", err)
	check(json.Unmarshal(genJsonBytes, &genJson) == nil, "generation.json parse: %v", err)

	// check fields and verify package map equals input
	check(genJson.Packages != nil && len(genJson.Packages) == 3, "generation.json has 3 packages, got %v", genJson.Packages)
	check(genJson.Timestamp != "", "timestamp present")
	check(genJson.ConfigHash != "", "config_hash present")
	check(genJson.KernelArgs != nil, "kernel_args present (even if empty)")

	// kernel_args should be empty (no set_kernel_arguments op in selftest)
	check(len(genJson.KernelArgs) == 0, "kernel_args empty in selftest, got %v", genJson.KernelArgs)

	// Verify package map matches input
	for name, pin := range meta0.Packages {
		check(genJson.Packages[name].Version == pin.Version, "pkg %s version matches", name)
	}

	// merged-usr: the relative bin/sbin symlinks must alias the generation's
	// own tree; a real dir (pkgA/bin) always wins over a later alias
	t, _ := os.Readlink(filepath.Join(g0, "sbin"))
	check(t == "/kod/generation/current/usr/sbin", "sbin aliases current/usr/sbin, got %q", t)
	check(isRealDir(filepath.Join(g0, "bin")), "real bin dir wins over alias")
	t, _ = os.Readlink(filepath.Join(g0, "bin", "a"))
	check(t == "", "gen bin/a is a hardlink, not a symlink (got %q)", t)
	check(sameInode(filepath.Join(g0, "bin", "a"), filepath.Join(s.store(), "pkgA", "1.0", "bin", "a")),
		"gen file hardlinks into store pool")
	// escaping relative link re-anchored at the live root; apk-style
	// merge-through put the content there instead of over the link
	vr, _ := os.Readlink(filepath.Join(g0, "var", "run"))
	check(vr == "/run", "gen var/run re-anchored to /run, got %q", vr)
	check(readOr(filepath.Join(chroot, "run", "avahi-daemon", "pid")) == "7\n",
		"pkg content merged through the var/run symlink")
	gt, err := os.Lstat(filepath.Join(g0, "tmp"))
	check(err == nil && gt.IsDir() && gt.Mode().Perm() == 0o777 && gt.Mode()&os.ModeSticky != 0,
		"generation dir keeps package mode 1777, got %v", gt.Mode())
	// new contract (hooks run chrooted into the RAW gen tree at create
	// time): gen/etc entries are HARDLINKS into the generation's store copy,
	// not /kod-rooted links (those would dangle inside the gen chroot).
	t, _ = os.Readlink(filepath.Join(g0, "etc", "foo"))
	check(t == "", "gen etc/foo is a hardlink, not a symlink (got %q)", t)
	check(sameInode(filepath.Join(g0, "etc", "foo"), filepath.Join(g0, "etc_store", "foo")),
		"gen etc entry hardlinks into etc_store")
	// symlink store entries are hardlinked AS symlinks in the gen tree
	tAbsStore, _ := os.Readlink(filepath.Join(g0, "etc_store", "abslink"))
	tRelStore, _ := os.Readlink(filepath.Join(g0, "etc_store", "rellink"))
	check(tAbsStore == "/usr/libexec/rc/sh/functions.sh", "etc_store abslink verbatim, got %q", tAbsStore)
	check(tRelStore == "../proc/self/mounts", "etc_store rellink verbatim, got %q", tRelStore)
	tAbsGen, _ := os.Readlink(filepath.Join(g0, "etc", "abslink"))
	check(tAbsGen == "/usr/libexec/rc/sh/functions.sh", "gen etc/abslink verbatim, got %q", tAbsGen)
	tRelGen, _ := os.Readlink(filepath.Join(g0, "etc", "rellink"))
	check(tRelGen == "../proc/self/mounts", "gen etc/rellink verbatim, got %q", tRelGen)

	check(s.activateGeneration(g0) == nil, "activate gen0")
	t, _ = os.Readlink(filepath.Join(chroot, "bin"))
	check(t == "/kod/generation/current/bin", "top-level links through current, got %q", t)
	check(sameInode(filepath.Join(chroot, "etc", "foo"), filepath.Join(g0, "etc_store", "foo")),
		"live etc entry is a hardlink into the generation store")
	check(readOr(filepath.Join(chroot, "etc", "foo")) == "upstream\n",
		"live entry carries upstream content")
	// symlink store entries must materialize in live /etc as verbatim symlinks
	// (target need not exist — etc merges before top-level dirs like usr/libexec are linked)
	tAbsLive, _ := os.Readlink(filepath.Join(chroot, "etc", "abslink"))
	tRelLive, _ := os.Readlink(filepath.Join(chroot, "etc", "rellink"))
	check(tAbsLive == "/usr/libexec/rc/sh/functions.sh",
		"live etc/abslink must be verbatim symlink, got %q", tAbsLive)
	check(tRelLive == "../proc/self/mounts",
		"live etc/rellink must be verbatim symlink, got %q", tRelLive)
	absSt, _ := os.Lstat(filepath.Join(chroot, "etc", "abslink"))
	relSt, _ := os.Lstat(filepath.Join(chroot, "etc", "rellink"))
	check(absSt != nil && absSt.Mode()&os.ModeSymlink != 0,
		"live etc/abslink is a symlink (not dropped/0-byte)")
	check(relSt != nil && relSt.Mode()&os.ModeSymlink != 0,
		"live etc/rellink is a symlink (not content-copied)")
	// create-before-delete ordering: replace abslink with a fresh gen0 re-activate
	// (same inode target → rename-over; old entry never disappears if rename fails).
	// Code-review note: the tmp+rename pattern in installEtc guarantees the old
	// entry is only removed if the new entry was atomically placed.

	check(readOr(s.chrootResolve(filepath.Join(chroot, "sbin", "s"))) == "S\n",
		"merged-usr sbin alias resolves through current")

	// local edit written through the live tree (lands in etc_store)
	check(os.WriteFile(s.chrootResolve(filepath.Join(chroot, "etc", "foo")), []byte("edited\n"), 0o644) == nil, "edit")

	// upstream changes foo in pkgA@1.1; pkgB drops out of the generation
	write(filepath.Join(s.store(), "pkgA", "1.1", "bin", "a"), "A2\n")
	write(filepath.Join(s.store(), "pkgA", "1.1", "etc", "foo"), "new-upstream\n")
	g1, err := s.createGeneration(map[string]string{"pkgA": "1.1"}, 1, g0)
	check(err == nil, "create gen1: %v", err)

	// Stamp generation 1 with its metadata content
	metaContent1 := []byte(`{"schema_version":1,"generation":1,"distro":"alpine","packages":{"pkgA":{"version":"1.1","checksum":""}}}`)
	meta1 := &metadata{
		SchemaVersion: 1,
		Generation:    1,
		Distro:        "alpine",
		Packages: map[string]pkgPin{
			"pkgA": {Version: "1.1", Checksum: ""},
		},
	}
	check(stampGeneration(g1, meta1, []string{}, metaContent1, nil) == nil, "stamp generation 1")

	check(readOr(filepath.Join(g1, "etc_default", "foo")) == "new-upstream\n", "baseline = pristine new default")
	check(readOr(filepath.Join(g1, "etc_store", "foo")) == "edited\n", "local edit carried forward")
	check(readOr(filepath.Join(g1, "etc_default", "foo.apknew-1")) == "new-upstream\n", "conflict copy apknew-1")
	check(!lexists(filepath.Join(g1, "etc", "bar")), "entry not shipped in gen1 not linked")

	check(s.activateGeneration(g1) == nil, "activate gen1")
	check(sameInode(filepath.Join(chroot, "etc", "foo"), filepath.Join(g1, "etc_store", "foo")),
		"switch re-materializes the hardlink from gen1's store")
	check(readOr(filepath.Join(chroot, "etc", "foo")) == "edited\n",
		"live config still shows the local edit after switch")
	check(!lexists(filepath.Join(chroot, "etc", "bar")), "managed entry pruned from live /etc")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "bin", "a"))) == "A2\n", "bin content follows current")

	// rollback: activate gen 0 again, edit is still the live truth there too
	check(s.activateGeneration(g0) == nil, "rollback activate gen0")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "etc", "foo"))) == "edited\n", "edit survives rollback")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "bin", "a"))) == "A1\n", "bin back at gen0")

	// (a) Re-seed/reinstall: contaminate gen/etc with stale legacy-shape entries,
	// then re-run seedEtc and assert force-replace + stale-sweep correctness.
	// (Runs after the full create/activate/switch/rollback sequence so chroot/bin
	// is already correctly set; createGeneration here exercises the re-install path.)
	{
		etcDir := filepath.Join(g0, "etc")
		// legacy /kod-rooted symlink at a seeded rel (pre-e95af09 shape)
		os.Remove(filepath.Join(etcDir, "foo"))
		check(os.Symlink("/kod/generation/0/etc_store/foo", filepath.Join(etcDir, "foo")) == nil,
			"inject legacy symlink at seeded rel")
		// stale real file with wrong content at another seeded rel
		os.Remove(filepath.Join(etcDir, "bar"))
		check(os.WriteFile(filepath.Join(etcDir, "bar"), []byte("stale\n"), 0o644) == nil,
			"inject stale real file at seeded rel")
		// stale real file at a rel NOT seeded (must be swept away)
		check(os.WriteFile(filepath.Join(etcDir, "unseeded-stale"), []byte("garbage\n"), 0o644) == nil,
			"inject unseeded stale file")
		// re-run createGeneration (same closure = re-install of same idx)
		g0b, err := s.createGeneration(map[string]string{"pkgA": "1.0", "pkgB": "2.0", "00base": "1.0"}, 0, "")
		check(err == nil, "re-seed gen0 must succeed: %v", err)
		_ = g0b
		// seeded rels must now be fresh hardlinks into etc_store (not legacy symlinks)
		fooLink, _ := os.Readlink(filepath.Join(g0, "etc", "foo"))
		check(fooLink == "", "re-seed: foo must be a hardlink, not a symlink (got %q)", fooLink)
		check(sameInode(filepath.Join(g0, "etc", "foo"), filepath.Join(g0, "etc_store", "foo")),
			"re-seed: foo hardlinks into fresh etc_store")
		barContent := readOr(filepath.Join(g0, "etc", "bar"))
		check(barContent == "bar\n",
			"re-seed: bar must carry upstream content, got %q", barContent)
		check(sameInode(filepath.Join(g0, "etc", "bar"), filepath.Join(g0, "etc_store", "bar")),
			"re-seed: bar hardlinks into fresh etc_store")
		_, unseededErr := os.Lstat(filepath.Join(g0, "etc", "unseeded-stale"))
		check(os.IsNotExist(unseededErr),
			"re-seed stale sweep: unseeded file must be removed")
	}

	fmt.Println("PASS: executor selftest (create/activate/seed/switch/prune/rollback)")
	selftestPruneLogic()
	selftestPure()
}

// prunePlan returns the list of generation numbers that should SURVIVE,
// given the cap and a set of protected generations (never pruned regardless).
// Sorted descending: newest-first; at most `keep` newest survive beyond protected set.
func prunePlan(gens []int, keep int, protect []int) []int {
	if keep < 1 {
		keep = 1
	}
	if len(gens) == 0 {
		return []int{}
	}
	protectedSet := make(map[int]bool, len(protect))
	for _, p := range protect {
		protectedSet[p] = true
	}
	// Filter out protected, sort desc
	var unprotected []int
	for _, g := range gens {
		if !protectedSet[g] {
			unprotected = append(unprotected, g)
		}
	}
	slices.SortFunc(unprotected, func(a, b int) int { return b - a })

	// Keep the top `keep` and always add all protected
	var survivors []int
	for i := 0; i < len(unprotected) && i < keep; i++ {
		survivors = append(survivors, unprotected[i])
	}
	for _, p := range protect {
		if !func() bool {
			for _, s := range survivors {
				if s == p {
					return true
				}
			}
			return false
		}() {
			survivors = append(survivors, p)
		}
	}
	slices.SortFunc(survivors, func(a, b int) int { return b - a })
	return survivors
}

func selftestPruneLogic() {
	type testCase struct {
		gens     []int
		keep     int
		protect  []int
		expected []int
	}
	cases := []testCase{
		// cap 2, gens 0..4, protect={0} → survivors {0,4,3}
		{[]int{0, 1, 2, 3, 4}, 2, []int{0}, []int{4, 3, 0}},
		// cap 2, gens 0..4, protect={2} → survivors {2,4,3}
		{[]int{0, 1, 2, 3, 4}, 2, []int{2}, []int{4, 3, 2}},
		// cap 5, gens 0..1, protect={1} → {1,0}
		{[]int{0, 1}, 5, []int{1}, []int{1, 0}},
		// cap 1, gens 0..2, protect={0} → {0,2}
		{[]int{0, 1, 2}, 1, []int{0}, []int{2, 0}},
		// keep >= len: cap 10, gens 0..2, protect={1} → all
		{[]int{0, 1, 2}, 10, []int{1}, []int{2, 1, 0}},
		// single gen, protect={0} → {0}
		{[]int{0}, 5, []int{0}, []int{0}},
		// CRITICAL: keep=1, gen 1 newly created, current→gen 0 still active
		// protect both 0 and 1 → neither pruned even though keep=1
		{[]int{0, 1}, 1, []int{0, 1}, []int{1, 0}},
	}
	for i, tc := range cases {
		got := prunePlan(tc.gens, tc.keep, tc.protect)
		if len(got) != len(tc.expected) {
			fmt.Printf("SELFTEST FAIL: prunePlan case %d length mismatch: expected %v, got %v\n",
				i, tc.expected, got)
			os.Exit(1)
		}
		for j, v := range tc.expected {
			if got[j] != v {
				fmt.Printf("SELFTEST FAIL: prunePlan case %d value mismatch at [%d]: expected %v, got %v\n",
					i, j, tc.expected, got)
				os.Exit(1)
			}
		}
	}

	// fs-backed test: create multiple generations + store entries, prune, verify
	chroot, err := os.MkdirTemp("", "kodos-prune-fs-")
	check(err == nil, "tempdir for prune fs test: %v", err)
	defer os.RemoveAll(chroot)
	s := &system{chroot: chroot}

	// Helper to write a minimal generation.json with package map
	writeGenJSON := func(genDir string, pkgs map[string]string) {
		pkgPins := map[string]pkgPin{}
		for n, v := range pkgs {
			pkgPins[n] = pkgPin{Version: v}
		}
		b, _ := json.Marshal(map[string]any{"packages": pkgPins})
		check(os.WriteFile(filepath.Join(genDir, "generation.json"), b, 0o644) == nil,
			"write generation.json in %s", genDir)
	}

	// Create store entries: sharedPkg is in gens 0 and 1; solePkg only in gen 1
	mkStore := func(pkg, ver string) {
		p := filepath.Join(s.store(), pkg, ver, "bin", pkg)
		check(os.MkdirAll(filepath.Dir(p), 0o755) == nil, "mkdir store %s", pkg)
		check(os.WriteFile(p, []byte(pkg+ver), 0o644) == nil, "write store %s", pkg)
	}
	mkStore("sharedPkg", "1.0")
	mkStore("solePkg", "1.0")

	// Gen 0: sharedPkg only
	g0 := s.genDir(0)
	check(os.MkdirAll(g0, 0o755) == nil, "mkdir gen 0")
	writeGenJSON(g0, map[string]string{"sharedPkg": "1.0"})

	// Gen 1: sharedPkg + solePkg (this will be pruned)
	g1 := s.genDir(1)
	check(os.MkdirAll(g1, 0o755) == nil, "mkdir gen 1")
	writeGenJSON(g1, map[string]string{"sharedPkg": "1.0", "solePkg": "1.0"})

	// Also create gens 2,3,4 (empty generation.json = no packages)
	for i := 2; i <= 4; i++ {
		gd := s.genDir(i)
		check(os.MkdirAll(gd, 0o755) == nil, "mkdir gen %d", i)
		writeGenJSON(gd, map[string]string{})
	}

	// Prune with keep=2, protect={0} (live current=0, new idx=5 not yet created)
	// → survivors {4,3,0}; pruned {1,2}; gen1's solePkg sole ref → store GC'd; sharedPkg kept
	err = s.pruneGenerations(2, []int{0})
	check(err == nil, "pruneGenerations: %v", err)

	// Verify generation dirs
	for _, g := range []int{0, 3, 4} {
		check(lexists(s.genDir(g)), "gen %d should survive", g)
	}
	for _, g := range []int{1, 2} {
		check(!lexists(s.genDir(g)), "gen %d should be pruned", g)
	}
	// sharedPkg referenced by survivor gen 0 → must NOT be deleted
	check(lexists(filepath.Join(s.store(), "sharedPkg", "1.0")),
		"sharedPkg/1.0 shared with survivor: must not be GC'd")
	// solePkg only in pruned gen 1 → must be deleted
	check(!lexists(filepath.Join(s.store(), "solePkg", "1.0")),
		"solePkg/1.0 sole ref was pruned gen: must be GC'd")

	// hand-edited current alias → prune refuses (fail closed, no silent ignore)
	{
		dir, err := os.MkdirTemp("", "kodos-curalias-")
		check(err == nil, "mkdtemp: %v", err)
		defer os.RemoveAll(dir)
		sc := &system{chroot: dir}
		genRoot := filepath.Join(sc.root(), "generation")
		check(os.MkdirAll(genRoot, 0o755) == nil, "mkdir gen root")
		check(os.Symlink("/kod/generation/notagen", filepath.Join(genRoot, "current")) == nil,
			"hand-edited current link")
		re := &executor{s: sc, meta: &metadata{Generation: 1, Distro: "alpine"}}
		err = re.run(map[string]any{"op": "create_generation", "packages": []any{}})
		check(err != nil && strings.Contains(err.Error(), "not a generation number"),
			"unparseable current alias must fail closed, got %v", err)
	}

	fmt.Println("PASS: prunePlan table-driven tests")
	fmt.Println("PASS: pruneGenerations fs-backed (keep=2, protect={0}, gen1+2 deleted, store GC sole refs, preserve shared, fail-closed current alias)")
}

func selftestPure() {
	// size parsing: binary rounding (MB==MiB, GB==GiB)
	m, ok := sizeMiB("64GB")
	check(ok && m == 65536, "sizeMiB 64GB = 65536, got %d %v", m, ok)
	m, ok = sizeMiB("512MB")
	check(ok && m == 512, "sizeMiB 512MB, got %d", m)

	// the latent eszkoz layout: home "100%" BEFORE swap 34GB must still
	// leave swap room (100% = rest minus later explicit sizes)
	specs := []partSpec{
		{1, "boot", "512MB", "esp"}, {2, "root", "64GB", "linux"},
		{3, "home", "100%", "linux"}, {4, "swap", "34GB", "linux-swap"},
	}
	rs, err := planPartitions(204800, specs) // 200GiB
	check(err == nil, "planPartitions: %v", err)
	check(rs[0].Start == 1 && rs[0].PartedF == "esp", "esp first, 1MiB aligned")
	check(rs[3].End == 204799, "swap reaches disk end, got %d", rs[3].End)
	check(rs[2].End < rs[3].Start && rs[2].End > rs[1].End, "100%% partition shrinks for later sizes")
	if _, err := planPartitions(65000, specs); err == nil {
		check(false, "tiny disk must be a hard error, not an overwrite")
	}

	// grub.cfg: ESP-relative kernel, UUID root, serial console for
	// -nographic; current generation first (default=0), every other
	// surviving generation as its own entry from its ESP gen-N dir
	cfg := renderGrubCfg(5, "UUID-X", []string{"quiet"}, []grubEntry{
		{4, "6.18.63-r0", "", ""}, {3, "6.18.60-r0", "/gen-3", ""}})
	check(strings.Contains(cfg, "linux /vmlinuz-6.18.63-r0 root=UUID=UUID-X ro quiet"),
		"grub cmdline, got:\n%s", cfg)
	check(strings.Contains(cfg, "initrd /gen-3/initramfs-6.18.60-r0"),
		"generation menu entry, got:\n%s", cfg)
	check(strings.Index(cfg, "generation 4") < strings.Index(cfg, "generation 3"),
		"current generation must be the default entry")
	check(strings.Contains(cfg, "terminal_output serial") &&
		!strings.Contains(cfg, "search.fs_uuid"),
		"grub serial, no search: kernel loads from ESP where grub.cfg lives")

	// sshd-style setting keys
	check(settingsKey("permit_root_login") == "PermitRootLogin", "CamelCase key")

	// {UUID:<device>} expansion in write_file content (guest fstab must
	// never carry install-time device names like /dev/nbd6p1)
	uuids := map[string]string{"/dev/nbd6p1": "BEEF", "/dev/nbd6p4": "SWAP-U"}
	got, err := expandUUIDTokens("{UUID:/dev/nbd6p1}\t/boot\tvfat\tdefaults\t0\t0\n{UUID:/dev/nbd6p4}\tnone\tswap\tsw\t0\t0\n",
		func(d string) (string, error) { return uuids[d], nil })
	check(err == nil, "uuid tokens: %v", err)
	check(strings.HasPrefix(got, "UUID=BEEF\t/boot") && strings.Contains(got, "UUID=SWAP-U\tnone"),
		"uuid substitution, got %q", got)
	if _, err := expandUUIDTokens("{UUID:/dev/ghost}", func(string) (string, error) {
		return "", fmt.Errorf("no such device")
	}); err == nil {
		check(false, "unresolvable UUID token must be a hard error")
	}
	check(strings.Contains(fmt.Sprint(func() error {
		_, err := expandUUIDTokens("{UUID:/dev/ghost}", func(string) (string, error) {
			return "", fmt.Errorf("boom")
		})
		return err
	}()), "{UUID:/dev/ghost}"), "error names the failing token")

	// apk unpack keeps setuid bits (guest sudo/su/passwd break silently
	// if Perm() truncates them) and hardlinks/symlinks survive
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range []*tar.Header{
		{Name: "tmp", Mode: 01777, Typeflag: tar.TypeDir},
		{Name: "usr/bin/sudo", Mode: 04755, Size: 4, Typeflag: tar.TypeReg},
		{Name: "usr/bin/sudodbg", Mode: 04755, Typeflag: tar.TypeLink, Linkname: "usr/bin/sudo"},
		{Name: "usr/share/man/man8/sudo.8.gz", Mode: 0644, Size: 4, Typeflag: tar.TypeReg},
	} {
		check(tw.WriteHeader(h) == nil, "tar hdr %s", h.Name)
		if h.Size > 0 {
			tw.Write([]byte("sudo"))
		}
	}
	tw.Close()
	zw.Close()
	unpackDir, err := os.MkdirTemp("", "kodos-unpack-")
	check(err == nil, "unpack tempdir: %v", err)
	defer os.RemoveAll(unpackDir)
	check(unpackApkStream(bytes.NewReader(buf.Bytes()), unpackDir, "fake") == nil, "unpackApkStream")
	dt, err := os.Lstat(filepath.Join(unpackDir, "tmp"))
	check(err == nil && dt.IsDir() && dt.Mode().Perm() == 0o777 && dt.Mode()&os.ModeSticky != 0,
		"apk dir keeps 1777 sticky, got %v", dt.Mode())
	st, err := os.Lstat(filepath.Join(unpackDir, "usr/bin/sudo"))
	check(err == nil && st.Mode().Perm() == 0o755 && st.Mode()&os.ModeSetuid != 0,
		"sudo keeps setuid, got %v", st.Mode())
	hst, err := os.Lstat(filepath.Join(unpackDir, "usr/bin/sudodbg"))
	check(err == nil && hst.Mode()&os.ModeSetuid != 0 && sameInode(
		filepath.Join(unpackDir, "usr/bin/sudodbg"), filepath.Join(unpackDir, "usr/bin/sudo")),
		"hardlink keeps setuid+inode, got %v", hst.Mode())

	// diskBusy must refuse host-active disks (the config carries real names)
	if lm, err := liveMounts(); err == nil {
		if busy := diskBusy("/dev/"+hostRootDisk(), lm); !busy {
			check(false, "diskBusy failed to flag the host root disk")
		}
	}
	check(kodosUUIDRe.MatchString(connUUID("wired")) &&
		connUUID("wired") == connUUID("wired") && connUUID("wired") != connUUID("home"),
		"connUUID must be valid, deterministic per name, distinct, got %q", connUUID("wired"))

	// checkGenRunnable: generation completeness guard — missing /sbin/init or
	// /bin/sh means a post-install hook silently failed (busybox's hook creates
	// both via --install -s). The bind-mount path (ensureGenChrootMounts) that
	// makes real hooks work is validated by the scratch E2E (runs inside
	// unshare -Urm where mounts are allowed; selftest runs without that
	// privilege, so hooks themselves can't run real binaries here).
	{
		dir, err := os.MkdirTemp("", "kodos-rnbl")
		check(err == nil, "mkdtemp runnable: %v", err)
		// complete tree: has both entries
		check(os.MkdirAll(filepath.Join(dir, "sbin"), 0o755) == nil, "mkdir sbin")
		check(os.MkdirAll(filepath.Join(dir, "bin"), 0o755) == nil, "mkdir bin")
		check(os.WriteFile(filepath.Join(dir, "bin", "sh"), []byte{}, 0o755) == nil, "write bin/sh")
		check(os.Symlink("/bin/busybox", filepath.Join(dir, "sbin", "init")) == nil, "symlink sbin/init")
		check(checkGenRunnable(dir, 99, "alpine") == nil, "complete gen must pass runnable check")
		// missing /sbin/init → hard error
		os.Remove(filepath.Join(dir, "sbin", "init"))
		err = checkGenRunnable(dir, 99, "alpine")
		check(err != nil && strings.Contains(err.Error(), "incomplete"),
			"missing sbin/init must hard-error, got: %v", err)
		// missing /bin/sh → hard error
		check(os.Symlink("/bin/busybox", filepath.Join(dir, "sbin", "init")) == nil, "restore init")
		os.Remove(filepath.Join(dir, "bin", "sh"))
		err = checkGenRunnable(dir, 99, "alpine")
		check(err != nil && strings.Contains(err.Error(), "incomplete"),
			"missing bin/sh must hard-error, got: %v", err)
		os.RemoveAll(dir)
	}

	// runlevel reconciliation: sweep stale enables; protect declared and
	// hook-added entries; streams without services touch nothing.
	{
		dir, err := os.MkdirTemp("", "kodos-rl")
		check(err == nil, "mkdtemp: %v", err)
		for _, k := range []string{"boot/keep", "boot/gone", "sysinit/hooked"} {
			p := filepath.Join(dir, "etc/runlevels", k)
			check(os.MkdirAll(filepath.Dir(p), 0o755) == nil, "mkdir %s", p)
			check(os.Symlink("../init.d/"+filepath.Base(k), p) == nil, "symlink %s", p)
		}
		// regular-file stale entry (hook-made runlevel enables now land as
		// real files after the hardlink seed shape change)
		goneFile := filepath.Join(dir, "etc/runlevels/boot/gonefile")
		check(os.WriteFile(goneFile, []byte{}, 0o644) == nil, "write stale regular runlevel entry")
		hookedFile := filepath.Join(dir, "etc/runlevels/sysinit/hookedfile")
		check(os.WriteFile(hookedFile, []byte{}, 0o644) == nil, "write hook-set regular runlevel entry")
		re := &executor{s: &system{chroot: dir},
			declaredRunlevels: map[string]bool{"boot/keep": true},
			hookRunlevels:     map[string]bool{"sysinit/hooked": true, "sysinit/hookedfile": true}}
		check(re.reconcileRunlevels() == nil, "reconcile returned error")
		_, e1 := os.Lstat(filepath.Join(dir, "etc/runlevels/boot/keep"))
		_, e2 := os.Lstat(filepath.Join(dir, "etc/runlevels/sysinit/hooked"))
		_, e3 := os.Lstat(filepath.Join(dir, "etc/runlevels/boot/gone"))
		_, e3f := os.Lstat(filepath.Join(dir, "etc/runlevels/boot/gonefile"))
		_, e2f := os.Lstat(filepath.Join(dir, "etc/runlevels/sysinit/hookedfile"))
		check(e1 == nil && e2 == nil && os.IsNotExist(e3),
			"reconcile must keep declared+hooked, sweep stale: %v %v %v", e1, e2, e3)
		check(os.IsNotExist(e3f),
			"reconcile must sweep stale regular-file runlevel entry: %v", e3f)
		check(e2f == nil,
			"reconcile must keep hook-set regular-file runlevel entry: %v", e2f)
		re2 := &executor{s: &system{chroot: dir}} // no enables in stream: hands off
		check(re2.reconcileRunlevels() == nil, "reconcile no-op returned error")
		_, e4 := os.Lstat(filepath.Join(dir, "etc/runlevels/sysinit/hooked"))
		check(e4 == nil, "no-service stream swept an entry it must not touch")
		os.RemoveAll(dir)
	}
	// initramfs cpio sanitize: drop store-duplicate records, DEREFERENCE
	// etc symlinks that pointed into them, keep the rest byte-for-byte,
	// require /init (hollow-image guard), stay re-parseable (idempotent).
	{
		var cpio bytes.Buffer
		for _, r := range []struct {
			n, b string
			mode int
		}{
			{".", "", 0o040755},
			{"./init", "#!/bin/sh", 0o100755},
			{"./etc/modprobe.d/a.conf", "/kod/generation/0/x.conf", 0o120777},
			{"./etc/keep", "k", 0o100644},
			{"./kod", "", 0o040755},
			{"./kod/generation/0/x.conf", "REALCONF", 0o100644},
			{"TRAILER!!!", "", 0},
		} {
			cpio.Write(cpioRec(r.mode, r.n, r.b))
		}
		out, hasInit, removed, derefed, err := stripCpio(cpio.Bytes(), "kod")
		check(err == nil && hasInit && removed == 2 && derefed == 1,
			"stripCpio: err=%v init=%v removed=%d derefed=%d", err, hasInit, removed, derefed)
		check(bytes.Contains(out, []byte("REALCONF")) &&
			bytes.Contains(out, []byte("etc/modprobe.d/a.conf\x00")) &&
			bytes.Contains(out, []byte("etc/keep\x00")),
			"stripCpio lost content or kept entries")
		check(!bytes.Contains(out, []byte("/kod/")), "stripCpio left dangling /kod/ target")
		_, _, r2, d2, err2 := stripCpio(out, "kod") // rewrite must parse & be a no-op
		check(err2 == nil && r2 == 0 && d2 == 0, "stripCpio output invalid/idempotent: %v r=%d d=%d", err2, r2, d2)
		var c2 bytes.Buffer
		c2.Write(cpioRec(0o100644, "bin/busybox", "x")) // no init: hollow image
		c2.Write(cpioRec(0, "TRAILER!!!", ""))
		_, hasInit2, _, _, err3 := stripCpio(c2.Bytes(), "kod")
		check(err3 == nil && !hasInit2, "stripCpio must report missing /init")
		var c3 bytes.Buffer
		c3.Write(cpioRec(0o120777, "./etc/x", "/kod/nope.conf"))
		c3.Write(cpioRec(0, "TRAILER!!!", ""))
		_, _, _, _, err4 := stripCpio(c3.Bytes(), "kod")
		check(err4 != nil, "stripCpio must fail on symlink to missing store file")
		var bad bytes.Buffer
		bad.Write(cpioRec(0o100644, "init", "x"))
		bad.Write([]byte("070701garbage-not-a-header"))
		_, _, _, _, err5 := stripCpio(bad.Bytes(), "kod")
		check(err5 != nil, "stripCpio must reject truncated records")
	}
	fmt.Println("PASS: executor selftest (planner/grub/keys/disk-guard/keyfile-uuid/runlevels/initramfs-cpio)")

	// parseMounts + isMounted: pure helpers used by format/mount guards
	{
		sample := "proc /proc proc rw 0 0\n" +
			"/dev/sda1 / ext4 rw 0 0\n" +
			"/dev/sda2 /home ext4 rw 0 0\n" +
			"/dev/nbd0p1 /mnt/boot vfat rw 0 0\n" +
			`/dev/disk/by-label/My\040Disk /mnt/space ext4 rw 0 0` + "\n"
		m := parseMounts(sample)
		check(m["/"] == "/dev/sda1", "parseMounts root, got %q", m["/"])
		check(m["/home"] == "/dev/sda2", "parseMounts /home")
		check(m["/mnt/boot"] == "/dev/nbd0p1", "parseMounts /mnt/boot")
		check(m["/mnt/space"] == "/dev/disk/by-label/My Disk", `parseMounts \040 unescaped, got %q`, m["/mnt/space"])
		check(m["/proc"] == "proc", "parseMounts proc included (all mount points tracked)")

		// isMounted: exact device match
		check(isMounted("/dev/sda1", m), "isMounted exact match")
		check(!isMounted("/dev/sda3", m), "isMounted unmounted device")
		// whole disk with mounted partition
		check(isMounted("/dev/nbd0", m), "isMounted whole disk via partition nbd0p1")
		check(!isMounted("/dev/nbd1", m), "isMounted nbd1 not a false positive for nbd0")
		check(isMounted("/dev/sda", m), "isMounted whole disk via partition sda1")
		// mountedTargetAt must hand back the LIVE device: mount /'s skip
		// branch sets rootPart from it (config names may not be blkid-resolvable)
		dev, ok := mountedTargetAt("/home", m)
		check(ok && dev == "/dev/sda2", "mountedTargetAt device, got %q %v", dev, ok)
		_, ok = mountedTargetAt("/nope", m)
		check(!ok, "mountedTargetAt on unmounted point")
	}

	// installBootloader must resolve the root UUID BEFORE wiping the ESP:
	// an unresolvable rootPart fails with "UUID of" and leaves the staged
	// grub tree intact (no half-boot state after a failed wipe).
	{
		dir, err := os.MkdirTemp("", "kodos-bootwipe-")
		check(err == nil, "mkdtemp: %v", err)
		defer os.RemoveAll(dir)
		staged := filepath.Join(dir, "boot/grub", "staged.cfg")
		check(os.MkdirAll(filepath.Dir(staged), 0o755) == nil, "mkdir staged grub")
		check(os.WriteFile(staged, []byte("x"), 0o644) == nil, "write staged grub")
		re := &executor{s: &system{chroot: dir},
			meta:     &metadata{Generation: 0, Distro: "alpine"},
			rootPart: "/dev/kodos-selftest-no-such-disk"}
		err = re.installBootloader(map[string]any{"op": "install_bootloader"})
		check(err != nil && strings.Contains(err.Error(), "UUID of"),
			"bootloader must fail at UUID lookup, got %v", err)
		_, serr := os.Stat(staged)
		check(serr == nil, "ESP grub tree wiped despite early UUID failure")
	}

	fmt.Println("PASS: executor selftest (parseMounts/isMounted/mountedTargetAt guards, bootloader fail-before-ESP-wipe)")
}

// cpioRec builds one newc record (header hex + NUL-terminated name, then
// data, each padded to 4 bytes — the mkinitfs/busybox writer's alignment)
// for the stripCpio checks above.
func cpioRec(mode int, name, body string) []byte {
	var rec bytes.Buffer
	fmt.Fprintf(&rec, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		1, mode, 0, 0, 1, 0, len(body), 0, 0, 0, 0, len(name)+1, 0)
	rec.WriteString(name)
	rec.WriteByte(0)
	for rec.Len()%4 != 0 {
		rec.WriteByte(0)
	}
	rec.WriteString(body)
	for rec.Len()%4 != 0 {
		rec.WriteByte(0)
	}
	return rec.Bytes()
}

// hostRootDisk: device of the host's / partition, e.g. /dev/nvme0n1p2 -> nvme0n1
func hostRootDisk() string {
	b, err := os.ReadFile("/proc/mounts")
	check(err == nil, "read /proc/mounts: %v", err)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "/" && strings.HasPrefix(f[0], "/dev/") {
			name := filepath.Base(f[0])
			name = strings.TrimRight(name, "0123456789")
			return strings.TrimRight(name, "p")
		}
	}
	check(false, "host / not on a /dev/ device")
	return ""
}

// selftestHookSweep wraps selftestHookSweepNS in a user namespace so
// chroot(2) is permitted without real root. Skips gracefully if unshare
// is unavailable (CI without user-ns support).
func selftestHookSweep() {
	self, err := os.Executable()
	if err != nil {
		fmt.Println("SKIP: hook-sweep NS fixture (can't find self executable)")
		return
	}
	cmd := exec.Command("unshare", "-r", self, "selftest-hook-ns")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		check(false, "hook-sweep NS fixture failed: %v", err)
	}
}

// It runs inside a user namespace (called via unshare -r) so that chroot(2)
// is permitted. It builds a minimal real gen tree with a real busybox binary,
// writes fixture hook scripts, calls runPostInstalls, and asserts:
//   - sbin/init and bin/sh created by the busybox-style hook
//   - etc/bmarker written by pkgB's #!/bin/sh hook (proves /bin/sh available
//     in-sweep before pkgB's hook runs — busybox hook creates it first)
//   - checkGenRunnable passes
//   - negative: without the busybox link-hook, pkgB's #!/bin/sh exec fails
//     and runPostInstalls returns an error
func selftestHookSweepNS() {
	dir, err := os.MkdirTemp("", "kodos-hookns")
	check(err == nil, "mkdtemp hookns: %v", err)
	defer os.RemoveAll(dir)

	// Locate a real busybox binary on the host.
	bbHost := ""
	for _, c := range []string{"/bin/busybox", "/usr/bin/busybox"} {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			bbHost = c
			break
		}
	}
	check(bbHost != "", "busybox not found on host — cannot run hook sweep NS fixture")

	// Build a fake store + gen tree under dir.
	chroot := filepath.Join(dir, "chroot")
	storeDir := filepath.Join(chroot, "kod", "store")
	check(os.MkdirAll(filepath.Join(storeDir, "busybox", "1.0"), 0o755) == nil, "mkdir store/busybox")
	check(os.MkdirAll(filepath.Join(storeDir, "pkgB", "1.0"), 0o755) == nil, "mkdir store/pkgB")

	// busybox: ship a real /bin/busybox, hook creates /sbin/init and /bin/sh
	check(os.MkdirAll(filepath.Join(storeDir, "busybox", "1.0", "bin"), 0o755) == nil, "mkdir busybox/bin")
	bbDst := filepath.Join(storeDir, "busybox", "1.0", "bin", "busybox")
	src, err2 := os.ReadFile(bbHost)
	check(err2 == nil, "read busybox: %v", err2)
	check(os.WriteFile(bbDst, src, 0o755) == nil, "write busybox binary")

	// stageMuslInterp copies the host musl ELF interpreter (if any) into a
	// gen dir's /lib so that dynamically-linked busybox binaries (Alpine's
	// busybox is musl-linked) can execute inside the fixture chroot. On glibc
	// hosts the host busybox is static and the glob finds nothing → no-op.
	stageMuslInterp := func(genD string) {
		matches, _ := filepath.Glob("/lib/ld-musl-*.so.1")
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			dst := filepath.Join(genD, "lib", filepath.Base(m))
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, b, 0o755)
		}
	}

	bbHook := "#!/bin/busybox sh\n/bin/busybox mkdir -p /bin /sbin /usr/bin /usr/sbin\n/bin/busybox --install -s\nexit 0\n"
	check(os.WriteFile(filepath.Join(storeDir, "busybox", "1.0", ".post-install"), []byte(bbHook), 0o755) == nil, "write busybox hook")

	// pkgB: hook uses #!/bin/sh (depends on busybox hook running first)
	pkgBHook := "#!/bin/sh\n/bin/busybox mkdir -p /etc\necho bmarker > /etc/bmarker\nexit 0\n"
	check(os.WriteFile(filepath.Join(storeDir, "pkgB", "1.0", ".post-install"), []byte(pkgBHook), 0o755) == nil, "write pkgB hook")

	// Create a minimal system + generation directory.
	s := &system{chroot: chroot}
	genDir := filepath.Join(chroot, "kod", "generation", "0")
	check(os.MkdirAll(filepath.Join(genDir, "bin"), 0o755) == nil, "mkdir gen/bin")
	check(os.MkdirAll(filepath.Join(genDir, "sbin"), 0o755) == nil, "mkdir gen/sbin")
	check(os.MkdirAll(filepath.Join(genDir, "etc"), 0o755) == nil, "mkdir gen/etc")
	check(os.MkdirAll(filepath.Join(genDir, "tmp"), 0o755) == nil, "mkdir gen/tmp")
	// Copy busybox into gen/bin so hook's shebang #!/bin/busybox sh resolves.
	check(os.WriteFile(filepath.Join(genDir, "bin", "busybox"), src, 0o755) == nil, "gen busybox")
	stageMuslInterp(genDir)

	meta := &metadata{Generation: 0}
	e := &executor{s: s, meta: meta, hookRunlevels: map[string]bool{}}
	toInstall := map[string]string{"busybox": "1.0", "pkgB": "1.0"}

	// POSITIVE: busybox hook runs first → /bin/sh exists → pkgB hook succeeds.
	err = e.runPostInstalls(genDir, toInstall)
	check(err == nil, "positive sweep must succeed: %v", err)
	_, initErr := os.Lstat(filepath.Join(genDir, "sbin", "init"))
	check(initErr == nil, "sbin/init must exist after sweep")
	_, shErr := os.Lstat(filepath.Join(genDir, "bin", "sh"))
	check(shErr == nil, "bin/sh must exist after sweep")
	bmarker, _ := os.ReadFile(filepath.Join(genDir, "etc", "bmarker"))
	check(strings.TrimSpace(string(bmarker)) == "bmarker", "pkgB bmarker content, got %q", string(bmarker))
	check(checkGenRunnable(genDir, 0, "alpine") == nil, "positive sweep: checkGenRunnable must pass")

	// NEGATIVE: remove the busybox-style link hook; pkgB's #!/bin/sh exec
	// will fail (ENOENT for /bin/sh interpreter) → sweep error.
	check(os.Remove(filepath.Join(storeDir, "busybox", "1.0", ".post-install")) == nil, "remove busybox hook")
	// Also clean up artifacts from the positive run so the negative path starts fresh.
	os.Remove(filepath.Join(genDir, "sbin", "init"))
	os.Remove(filepath.Join(genDir, "bin", "sh"))
	os.Remove(filepath.Join(genDir, "etc", "bmarker"))
	e2 := &executor{s: s, meta: meta, hookRunlevels: map[string]bool{}}
	err = e2.runPostInstalls(genDir, toInstall)
	check(err != nil, "negative sweep (no busybox hook) must fail — pkgB's #!/bin/sh exec errors")

	// (b) Hook-sees-shipped-etc: fixture pkg P ships etc/seeded.conf; pkg Q's
	// #!/bin/sh hook greps for the marker line and appends "extra".
	// After the sweep: etc/bmarker2 exists AND the etc_store seeded.conf
	// contains "extra" (whether written through the hardlink or re-cowed by
	// syncMutatedEtc).
	{
		// Restore the busybox link hook for this sub-fixture.
		bbHook2 := "#!/bin/busybox sh\n/bin/busybox mkdir -p /bin /sbin /usr/bin /usr/sbin\n/bin/busybox --install -s\nexit 0\n"
		check(os.WriteFile(filepath.Join(storeDir, "busybox", "1.0", ".post-install"), []byte(bbHook2), 0o755) == nil,
			"restore busybox hook for shipped-etc fixture")

		// pkgP ships etc/seeded.conf
		check(os.MkdirAll(filepath.Join(storeDir, "pkgP", "1.0", "etc"), 0o755) == nil, "mkdir pkgP/etc")
		check(os.WriteFile(filepath.Join(storeDir, "pkgP", "1.0", "etc", "seeded.conf"),
			[]byte("marker\n"), 0o644) == nil, "write pkgP seeded.conf")

		// pkgQ post-install greps for marker + appends "extra" + touches bmarker2
		qHook := "#!/bin/sh\n/bin/busybox grep -q marker /etc/seeded.conf || exit 1\necho extra >> /etc/seeded.conf\n> /etc/bmarker2\nexit 0\n"
		check(os.MkdirAll(filepath.Join(storeDir, "pkgQ", "1.0"), 0o755) == nil, "mkdir pkgQ")
		check(os.WriteFile(filepath.Join(storeDir, "pkgQ", "1.0", ".post-install"), []byte(qHook), 0o755) == nil,
			"write pkgQ hook")

		// Fresh gen dir for this sub-fixture.
		genDir2 := filepath.Join(chroot, "kod", "generation", "2")
		for _, d := range []string{"bin", "sbin", "etc", "tmp", "etc_store", "etc_default"} {
			check(os.MkdirAll(filepath.Join(genDir2, d), 0o755) == nil, "mkdir gen2/"+d)
		}
		check(os.WriteFile(filepath.Join(genDir2, "bin", "busybox"), src, 0o755) == nil, "gen2 busybox")
		stageMuslInterp(genDir2)

		// Seed etc manually: hardlink pkgP's seeded.conf into gen/etc.
		check(os.Link(filepath.Join(storeDir, "pkgP", "1.0", "etc", "seeded.conf"),
			filepath.Join(genDir2, "etc_store", "seeded.conf")) == nil, "etc_store seeded.conf")
		check(os.Link(filepath.Join(genDir2, "etc_store", "seeded.conf"),
			filepath.Join(genDir2, "etc", "seeded.conf")) == nil, "gen/etc seeded.conf hardlink")

		meta2 := &metadata{Generation: 2}
		e3 := &executor{s: s, meta: meta2, hookRunlevels: map[string]bool{}}
		toInstall2 := map[string]string{"busybox": "1.0", "pkgP": "1.0", "pkgQ": "1.0"}
		err3 := e3.runPostInstalls(genDir2, toInstall2)
		check(err3 == nil, "shipped-etc sweep must succeed: %v", err3)
		_, bm2Err := os.Lstat(filepath.Join(genDir2, "etc", "bmarker2"))
		check(bm2Err == nil, "bmarker2 must exist (pkgQ hook ran)")
		// seeded.conf store copy must contain "extra" (hook wrote through hardlink or syncMutatedEtc re-cowed)
		seededFinal, _ := os.ReadFile(filepath.Join(genDir2, "etc_store", "seeded.conf"))
		check(strings.Contains(string(seededFinal), "extra"),
			"etc_store seeded.conf must contain 'extra' after hook append, got %q", string(seededFinal))

		// Negative twin: hook greps for a marker NOT in seeded.conf → error.
		qHookBad := "#!/bin/sh\n/bin/busybox grep -q NOTHERE /etc/seeded.conf || exit 1\necho should-not-reach >> /etc/bmarker3\nexit 0\n"
		check(os.WriteFile(filepath.Join(storeDir, "pkgQ", "1.0", ".post-install"), []byte(qHookBad), 0o755) == nil,
			"write pkgQ bad hook")
		genDir3 := filepath.Join(chroot, "kod", "generation", "3")
		for _, d := range []string{"bin", "sbin", "etc", "tmp", "etc_store", "etc_default"} {
			check(os.MkdirAll(filepath.Join(genDir3, d), 0o755) == nil, "mkdir gen3/"+d)
		}
		check(os.WriteFile(filepath.Join(genDir3, "bin", "busybox"), src, 0o755) == nil, "gen3 busybox")
		stageMuslInterp(genDir3)
		check(os.Link(filepath.Join(storeDir, "pkgP", "1.0", "etc", "seeded.conf"),
			filepath.Join(genDir3, "etc_store", "seeded.conf")) == nil, "gen3 etc_store seeded.conf")
		check(os.Link(filepath.Join(genDir3, "etc_store", "seeded.conf"),
			filepath.Join(genDir3, "etc", "seeded.conf")) == nil, "gen3 gen/etc seeded.conf")
		meta3 := &metadata{Generation: 3}
		e4 := &executor{s: s, meta: meta3, hookRunlevels: map[string]bool{}}
		err4 := e4.runPostInstalls(genDir3, toInstall2)
		check(err4 != nil, "shipped-etc negative: hook greps for absent marker must fail, got nil")
	}

	fmt.Println("PASS: executor selftest (hook-sweep intra-/bin/sh dependency, positive+negative)")
}
