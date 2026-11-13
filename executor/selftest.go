package main

// selftest exercises the ported generation model in a scratch dir with a
// fake store: create -> activate -> edit through the live tree -> recreate
// with a changed upstream default -> re-activate. It asserts the contract
// that matters: edits survive, pristine baselines update, conflicts get
// .apknew files, vanished entries are pruned, and every generated link is
// chroot-rooted and resolves through `current`. Run: kodos-exec selftest

import (
	"fmt"
	"os"
	"path/filepath"
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

	// fake store: pkgA@1.0 and pkgB@2.0
	write(filepath.Join(s.store(), "pkgA", "1.0", "bin", "a"), "A1\n")
	write(filepath.Join(s.store(), "pkgA", "1.0", "etc", "foo"), "upstream\n")
	write(filepath.Join(s.store(), "pkgB", "2.0", "bin", "b"), "B\n")
	write(filepath.Join(s.store(), "pkgB", "2.0", "etc", "bar"), "bar\n")
	check(os.MkdirAll(filepath.Join(chroot, "etc"), 0o755) == nil, "live etc")

	// gen 0
	g0, err := s.createGeneration(map[string]string{"pkgA": "1.0", "pkgB": "2.0"}, 0, "")
	check(err == nil, "create gen0: %v", err)
	t, _ := os.Readlink(filepath.Join(g0, "bin", "a"))
	check(t == "/kod/store/pkgA/1.0/bin/a", "store link chroot-rooted, got %q", t)
	t, _ = os.Readlink(filepath.Join(g0, "etc", "foo"))
	check(strings.HasPrefix(t, "/kod/generation/0/etc_store/"), "etc link into etc_store, got %q", t)

	check(s.activateGeneration(g0) == nil, "activate gen0")
	t, _ = os.Readlink(filepath.Join(chroot, "bin"))
	check(t == "/kod/generation/current/bin", "top-level links through current, got %q", t)
	t, _ = os.Readlink(filepath.Join(chroot, "etc", "foo"))
	check(t == "/kod/generation/current/etc/foo", "live etc entry links through current, got %q", t)
	check(readOr(s.chrootResolve(filepath.Join(chroot, "etc", "foo"))) == "upstream\n",
		"chain resolves through current to upstream content")

	// local edit written through the live tree (lands in etc_store)
	check(os.WriteFile(s.chrootResolve(filepath.Join(chroot, "etc", "foo")), []byte("edited\n"), 0o644) == nil, "edit")

	// upstream changes foo in pkgA@1.1; pkgB drops out of the generation
	write(filepath.Join(s.store(), "pkgA", "1.1", "bin", "a"), "A2\n")
	write(filepath.Join(s.store(), "pkgA", "1.1", "etc", "foo"), "new-upstream\n")
	g1, err := s.createGeneration(map[string]string{"pkgA": "1.1"}, 1, g0)
	check(err == nil, "create gen1: %v", err)
	check(readOr(filepath.Join(g1, "etc_default", "foo")) == "new-upstream\n", "baseline = pristine new default")
	check(readOr(filepath.Join(g1, "etc_store", "foo")) == "edited\n", "local edit carried forward")
	check(readOr(filepath.Join(g1, "etc_default", "foo.apknew-1")) == "new-upstream\n", "conflict copy apknew-1")
	check(!lexists(filepath.Join(g1, "etc", "bar")), "entry not shipped in gen1 not linked")

	check(s.activateGeneration(g1) == nil, "activate gen1")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "etc", "foo"))) == "edited\n",
		"live config still shows the local edit after switch")
	check(!lexists(filepath.Join(chroot, "etc", "bar")), "managed entry pruned from live /etc")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "bin", "a"))) == "A2\n", "bin content follows current")

	// rollback: activate gen 0 again, edit is still the live truth there too
	check(s.activateGeneration(g0) == nil, "rollback activate gen0")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "etc", "foo"))) == "edited\n", "edit survives rollback")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "bin", "a"))) == "A1\n", "bin back at gen0")

	fmt.Println("PASS: executor selftest (create/activate/seed/switch/prune/rollback)")
	selftestPure()
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

	// grub.cfg: ESP-relative kernel, UUID root, serial console for -nographic
	cfg := renderGrubCfg(5, "UUID-X", "6.18.63-r0", []string{"quiet"}, 0)
	check(strings.Contains(cfg, "linux /vmlinuz-6.18.63-r0 root=UUID=UUID-X ro quiet"),
		"grub cmdline, got:\n%s", cfg)
	check(strings.Contains(cfg, "terminal_output serial") &&
		strings.Contains(cfg, "search.fs_uuid UUID-X root"), "grub serial+search")

	// sshd-style setting keys
	check(settingsKey("permit_root_login") == "PermitRootLogin", "CamelCase key")

	// diskBusy must refuse host-active disks (the config carries real names)
	if busy := diskBusy("/dev/" + hostRootDisk()); busy {
		// expected on any normal machine
	} else {
		check(false, "diskBusy failed to flag the host root disk")
	}
	fmt.Println("PASS: executor selftest (planner/grub/keys/disk-guard)")
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
