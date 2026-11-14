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

	// relative top-level symlinks (merged-usr shape) must alias the
	// generation tree, not the package (guard; Alpine 3.24 does not use
	// this shape — busybox ships real dirs and creates links post-install)
	write(filepath.Join(s.store(), "00base", "1.0", "usr", "bin", "b"), "B0\n")
	write(filepath.Join(s.store(), "00base", "1.0", "usr", "sbin", "s"), "S\n")
	check(os.Symlink("usr/bin", filepath.Join(s.store(), "00base", "1.0", "bin")) == nil, "fake bin symlink")
	check(os.Symlink("usr/sbin", filepath.Join(s.store(), "00base", "1.0", "sbin")) == nil, "fake sbin symlink")
	write(filepath.Join(s.store(), "pkgA", "1.0", "bin", "a"), "A1\n")
	write(filepath.Join(s.store(), "pkgA", "1.0", "etc", "foo"), "upstream\n")
	write(filepath.Join(s.store(), "pkgB", "2.0", "bin", "b"), "B\n")
	write(filepath.Join(s.store(), "pkgB", "2.0", "etc", "bar"), "bar\n")
	check(os.MkdirAll(filepath.Join(chroot, "etc"), 0o755) == nil, "live etc")

	// gen 0
	g0, err := s.createGeneration(map[string]string{"pkgA": "1.0", "pkgB": "2.0", "00base": "1.0"}, 0, "")
	check(err == nil, "create gen0: %v", err)
	// merged-usr: the relative bin/sbin symlinks must alias the generation's
	// own tree; a real dir (pkgA/bin) always wins over a later alias
	t, _ := os.Readlink(filepath.Join(g0, "sbin"))
	check(t == "/kod/generation/current/usr/sbin", "sbin aliases current/usr/sbin, got %q", t)
	check(isRealDir(filepath.Join(g0, "bin")), "real bin dir wins over alias")
	t, _ = os.Readlink(filepath.Join(g0, "bin", "a"))
	check(t == "", "gen bin/a is a hardlink, not a symlink (got %q)", t)
	check(sameInode(filepath.Join(g0, "bin", "a"), filepath.Join(s.store(), "pkgA", "1.0", "bin", "a")),
		"gen file hardlinks into store pool")
	t, _ = os.Readlink(filepath.Join(g0, "etc", "foo"))
	check(strings.HasPrefix(t, "/kod/generation/0/etc_store/"), "etc link into etc_store, got %q", t)

	check(s.activateGeneration(g0) == nil, "activate gen0")
	t, _ = os.Readlink(filepath.Join(chroot, "bin"))
	check(t == "/kod/generation/current/bin", "top-level links through current, got %q", t)
	check(sameInode(filepath.Join(chroot, "etc", "foo"), filepath.Join(g0, "etc_store", "foo")),
		"live etc entry is a hardlink into the generation store")
	check(readOr(filepath.Join(chroot, "etc", "foo")) == "upstream\n",
		"live entry carries upstream content")
	check(readOr(s.chrootResolve(filepath.Join(chroot, "sbin", "s"))) == "S\n",
		"merged-usr sbin alias resolves through current")

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
	st, err := os.Lstat(filepath.Join(unpackDir, "usr/bin/sudo"))
	check(err == nil && st.Mode().Perm() == 0o755 && st.Mode()&os.ModeSetuid != 0,
		"sudo keeps setuid, got %v", st.Mode())
	hst, err := os.Lstat(filepath.Join(unpackDir, "usr/bin/sudodbg"))
	check(err == nil && hst.Mode()&os.ModeSetuid != 0 && sameInode(
		filepath.Join(unpackDir, "usr/bin/sudodbg"), filepath.Join(unpackDir, "usr/bin/sudo")),
		"hardlink keeps setuid+inode, got %v", hst.Mode())

	// diskBusy must refuse host-active disks (the config carries real names)
	if busy := diskBusy("/dev/" + hostRootDisk()); busy {
		// expected on any normal machine
	} else {
		check(false, "diskBusy failed to flag the host root disk")
	}
	check(kodosUUIDRe.MatchString(connUUID("wired")) &&
		connUUID("wired") == connUUID("wired") && connUUID("wired") != connUUID("home"),
		"connUUID must be valid, deterministic per name, distinct, got %q", connUUID("wired"))

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
		re := &executor{s: &system{chroot: dir},
			declaredRunlevels: map[string]bool{"boot/keep": true},
			hookRunlevels:     map[string]bool{"sysinit/hooked": true}}
		check(re.reconcileRunlevels() == nil, "reconcile returned error")
		_, e1 := os.Lstat(filepath.Join(dir, "etc/runlevels/boot/keep"))
		_, e2 := os.Lstat(filepath.Join(dir, "etc/runlevels/sysinit/hooked"))
		_, e3 := os.Lstat(filepath.Join(dir, "etc/runlevels/boot/gone"))
		check(e1 == nil && e2 == nil && os.IsNotExist(e3),
			"reconcile must keep declared+hooked, sweep stale: %v %v %v", e1, e2, e3)
		re2 := &executor{s: &system{chroot: dir}} // no enables in stream: hands off
		check(re2.reconcileRunlevels() == nil, "reconcile no-op returned error")
		_, e4 := os.Lstat(filepath.Join(dir, "etc/runlevels/sysinit/hooked"))
		check(e4 == nil, "no-service stream swept an entry it must not touch")
		os.RemoveAll(dir)
	}
	// initramfs cpio sanitize: drop duplicated store entries, keep the
	// rest byte-for-byte, report a missing /init (hollow image guard).
	{
		var cpio bytes.Buffer
		for _, r := range []struct{ n, b string }{
			{".", ""},
			{"./init", "#!/bin/sh"},
			{"./kod", ""},
			{"./kod/generation/0/etc_store/m.conf", "conf"},
			{"./etc/modprobe.d/a.conf", "a"},
			{"TRAILER!!!", ""},
		} {
			cpio.Write(cpioRec(r.n, r.b))
		}
		out, hasInit, removed, err := stripCpio(cpio.Bytes(), "kod")
		check(err == nil && hasInit && removed == 2,
			"stripCpio: err=%v init=%v removed=%d", err, hasInit, removed)
		check(bytes.Contains(out, []byte("init\x00")) &&
			bytes.Contains(out, []byte("etc/modprobe.d/a.conf\x00")),
			"stripCpio dropped entries it must keep")
		check(!bytes.Contains(out, []byte("kod")), "stripCpio kept store entries")
		var c2 bytes.Buffer
		c2.Write(cpioRec("bin/busybox", "x")) // no init: hollow image
		c2.Write(cpioRec("TRAILER!!!", ""))
		_, hasInit2, _, err2 := stripCpio(c2.Bytes(), "kod")
		check(err2 == nil && !hasInit2, "stripCpio must report missing /init")
		var bad bytes.Buffer
		bad.Write(cpioRec("init", "x"))
		bad.Write([]byte("070701garbage-not-a-header"))
		_, _, _, err3 := stripCpio(bad.Bytes(), "kod")
		check(err3 != nil, "stripCpio must reject truncated records")
	}
	fmt.Println("PASS: executor selftest (planner/grub/keys/disk-guard/keyfile-uuid/runlevels/initramfs-cpio)")
}

// cpioRec builds one newc record (header hex + NUL-terminated name,
// then data, each padded to 4 bytes — the mkinitfs/busybox writer's
// alignment) for the stripCpio checks above.
func cpioRec(name, body string) []byte {
	var rec bytes.Buffer
	fmt.Fprintf(&rec, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		1, 0o100644, 0, 0, 1, 0, len(body), 0, 0, 0, 0, len(name)+1, 0)
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
