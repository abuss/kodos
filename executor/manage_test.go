package main

// Unit tests for `kodos status` / `kodos rollback`:
//   rollbackTarget — pure target selection (previous = highest gen strictly
//   below current; explicit N must exist, error names the usable list)
//   statusTo       — rendering over a temp kod tree with one good gen, one
//   CORRUPT generation.json and one gen with no manifest at all, plus the
//   broken/absent current alias cases.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRollbackTarget(t *testing.T) {
	gens := []int{0, 1, 2}
	p := func(n int) *int { return &n }

	// explicit N present in gens -> that N
	if got, err := rollbackTarget(2, p(0), gens, nil); err != nil || got != 0 {
		t.Errorf("explicit 0: got %d, %v", got, err)
	}
	// explicit N == current is allowed (re-activate the same generation)
	if got, err := rollbackTarget(2, p(2), gens, nil); err != nil || got != 2 {
		t.Errorf("explicit == current: got %d, %v", got, err)
	}
	// previous = highest generation strictly below current
	if got, err := rollbackTarget(2, nil, gens, nil); err != nil || got != 1 {
		t.Errorf("previous of 2: got %d, %v", got, err)
	}
	if got, err := rollbackTarget(5, nil, []int{0, 1, 3, 4}, nil); err != nil || got != 4 {
		t.Errorf("previous of 5 over {0,1,3,4}: got %d, %v", got, err)
	}
	if got, err := rollbackTarget(1, nil, []int{0, 4, 7}, nil); err != nil || got != 0 {
		t.Errorf("previous of 1 over {0,4,7}: got %d, %v", got, err)
	}

	// explicit N missing -> hard error naming N AND the generation list
	_, err := rollbackTarget(2, p(5), gens, nil)
	if err == nil {
		t.Fatal("explicit 5 must be a hard error")
	}
	if !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "0 1 2") {
		t.Errorf("explicit-missing error must name N and list gens: %v", err)
	}

	// previous with current at the lowest generation -> error listing gens
	_, err = rollbackTarget(0, nil, gens, nil)
	if err == nil {
		t.Fatal("previous below the lowest generation must error")
	}
	if !strings.Contains(err.Error(), "0 1 2") || !strings.Contains(err.Error(), "explicit") {
		t.Errorf("no-previous error must list gens and say pass N: %v", err)
	}

	// broken current alias (current < 0): no implicit target, says so
	_, err = rollbackTarget(-1, nil, gens, nil)
	if err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Errorf("broken current must error telling the user to pass N: %v", err)
	}

	// explicit N still rejected against an empty store
	if _, err := rollbackTarget(0, p(0), nil, nil); err == nil {
		t.Error("explicit N with no generations must error")
	}
}

// writeManifest marshals a metadata struct the same way stampGeneration does.
func writeManifest(t *testing.T, root string, gen int, meta *metadata, raw string) {
	t.Helper()
	dir := filepath.Join(root, "kod", "generation", strconv.Itoa(gen))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := []byte(raw)
	if raw == "" {
		var err error
		b, err = json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generation.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseRollbackArgs(t *testing.T) {
	// N first (the documented form) with flags after it
	n, root, apk, err := parseRollbackArgs([]string{"3", "-root", "scratch/root", "-apk", "vm/apk-tools-static"})
	if err != nil || n == nil || *n != 3 || root != "scratch/root" || apk != "vm/apk-tools-static" {
		t.Errorf("N + flags: n=%v root=%q apk=%q err=%v", n, root, apk, err)
	}
	// flags before N works too (the lift is value-aware: "r" is -root's value)
	n, root, _, err = parseRollbackArgs([]string{"-root", "r", "7"})
	if err != nil || n == nil || *n != 7 || root != "r" {
		t.Errorf("flags then N: n=%v root=%q err=%v", n, root, err)
	}
	// the common no-N form: flags only, their values must not become N
	n, root, apk, err = parseRollbackArgs([]string{"-root", "scratch/root", "-apk", "vm/apk-tools-static"})
	if err != nil || n != nil || root != "scratch/root" || apk != "vm/apk-tools-static" {
		t.Errorf("flags only (no N): n=%v root=%q apk=%q err=%v", n, root, apk, err)
	}
	// --flag=value form keeps its value attached
	if n, root, _, err := parseRollbackArgs([]string{"-root=eq", "4"}); err != nil || n == nil || *n != 4 || root != "eq" {
		t.Errorf("-root=value form: n=%v root=%q err=%v", n, root, err)
	}
	if n, _, _, err := parseRollbackArgs(nil); err != nil || n != nil {
		t.Errorf("bare rollback: n=%v err=%v", n, err)
	}
	// stray positionals / junk N fail closed
	if _, _, _, err := parseRollbackArgs([]string{"3", "extra"}); err == nil {
		t.Error("stray positional must error")
	}
	if _, _, _, err := parseRollbackArgs([]string{"notagen"}); err == nil {
		t.Error("non-numeric N must error")
	}
}

func TestRollbackRefusesUnusableTargets(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, 0, &metadata{SchemaVersion: 1, Generation: 0, Distro: "alpine"}, "")
	// dir/manifest disagree: activating it would flip current to a DIFFERENT
	// generation than the user asked for
	writeManifest(t, root, 3, &metadata{SchemaVersion: 1, Generation: 4, Distro: "alpine"}, "")
	// readable manifest, but no kernel in the tree: the bootloader leg would
	// fail AFTER activation, so refuse up front
	writeManifest(t, root, 5, &metadata{SchemaVersion: 1, Generation: 5, Distro: "alpine"}, "")
	if err := os.Symlink("/kod/generation/0", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}

	// gen 0 has a kernel (a complete generation); 3 and 5 deliberately do not.
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "0", "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kod", "generation", "0", "boot", "vmlinuz-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// gen 6: kernel present but the initramfs named after that kver is missing
	writeManifest(t, root, 6, &metadata{SchemaVersion: 1, Generation: 6, Distro: "alpine"}, "")
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "6", "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kod", "generation", "6", "boot", "vmlinuz-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		n    int
		want string
	}{
		{3, "declares generation 4"},
		{5, "no boot/vmlinuz"},
		{6, "no boot/initramfs-virt"},
	} {
		n := tc.n
		if err := rollback(root, &n, "apk"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rollback %d: want error naming %q, got %v", tc.n, tc.want, err)
		}
	}
	// nothing was activated in any error path
	if s, _ := os.Readlink(filepath.Join(root, "kod", "generation", "current")); s != "/kod/generation/0" {
		t.Errorf("error paths must not move current, got %q", s)
	}

	// (A) Runnable gate: gen 7 has vmlinuz + initramfs but NO sbin/init.
	// Explicit -to 7: must error naming "incomplete"; current must not move.
	writeManifest(t, root, 7, &metadata{SchemaVersion: 1, Generation: 7, Distro: "alpine"}, "")
	bootDir7 := filepath.Join(root, "kod", "generation", "7", "boot")
	if err := os.MkdirAll(bootDir7, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootDir7, "vmlinuz-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bootDir7, "initramfs-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No sbin/init in gen 7 → checkGenRunnable fires.
	seven := 7
	err := rollback(root, &seven, "apk")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("explicit rollback to incomplete gen must error with 'incomplete', got %v", err)
	}
	if s, _ := os.Readlink(filepath.Join(root, "kod", "generation", "current")); s != "/kod/generation/0" {
		t.Errorf("incomplete-gate must not move current, got %q", s)
	}

	// (B) Default rollback (n==nil) skips gen 7 (incomplete) and also gen 0
	// (which is current). There's no bootable lower gen → error naming the skip.
	// Make gen 7 the current so the nil walk looks at gen 0 (which has vmlinuz
	// but no sbin/init either in this fixture) and gen 5 (no vmlinuz) etc.
	// Simpler: add a runnable gen 1 (sbin/init present) below current gen 7;
	// nil walk must land on gen 1 skipping non-runnable ones.
	// Set current to gen 7 first.
	os.Remove(filepath.Join(root, "kod", "generation", "current"))
	if err := os.Symlink("/kod/generation/7", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	// Gen 1: has vmlinuz + initramfs + sbin/init + bin/sh (fully runnable).
	writeManifest(t, root, 1, &metadata{SchemaVersion: 1, Generation: 1, Distro: "alpine"}, "")
	gen1Boot := filepath.Join(root, "kod", "generation", "1", "boot")
	if err := os.MkdirAll(gen1Boot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen1Boot, "vmlinuz-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen1Boot, "initramfs-virt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"sbin/init", "bin/sh"} {
		p := filepath.Join(root, "kod", "generation", "1", f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// nil rollback from gen 7 must skip gens 0,3,5,6 (bad manifest or no kernel
	// or no sbin/init) and land on gen 1.
	rollbackTarget_got, rtErr := rollbackTarget(7, nil, []int{0, 1, 3, 5, 6, 7}, func(g int) bool {
		return checkGenRunnable(filepath.Join(root, "kod", "generation", strconv.Itoa(g)), g, "alpine") == nil
	})
	if rtErr != nil || rollbackTarget_got != 1 {
		t.Errorf("nil rollback must skip non-runnable and pick gen 1, got %d err %v", rollbackTarget_got, rtErr)
	}
}

func TestBootTimeoutSurvivesRollback(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boot", "grub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "set default=0\nset timeout=3\nmenuentry 'Kodos generation 4' {\n\tlinux /vmlinuz-virt root=UUID=x\n}\n"
	if err := os.WriteFile(filepath.Join(root, "boot", "grub", "grub.cfg"), []byte(cfg), 0o444); err != nil {
		t.Fatal(err)
	}
	title, timeout, has, err := grubCfg(&system{chroot: root})
	if err != nil || title != "Kodos generation 4" || timeout != 3 || !has {
		t.Fatalf("grubCfg: title=%q timeout=%d has=%v err=%v", title, timeout, has, err)
	}
	act := bootloaderAction(timeout, has)
	if act["op"] != "install_bootloader" || act["timeout"] != 3 {
		t.Errorf("synthesized bootloader action must carry the machine's timeout: %v", act)
	}
	if len(act) != 2 {
		t.Errorf("only op+timeout belong in the action (no inert type/efi): %v", act)
	}

	// no grub.cfg (first run, or a bootloader leg that never finished): the op's
	// own default applies, so the field is omitted rather than defaulted here.
	_, timeout, has, err = grubCfg(&system{chroot: t.TempDir()})
	if err == nil || has || timeout != 0 {
		t.Errorf("absent grub.cfg must give timeout 0/false: %d %v %v", timeout, has, err)
	}
	if act := bootloaderAction(timeout, has); len(act) != 1 {
		t.Errorf("absent timeout must not add a field: %v", act)
	}

	// unparseable timeout: same (op default), title still read
	bad := filepath.Join(t.TempDir(), "boot", "grub")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "grub.cfg"), []byte("set timeout=fast\nmenuentry \"Kodos gen 7\" {\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	title, _, has, _ = grubCfg(&system{chroot: filepath.Dir(filepath.Dir(bad))})
	if has || title != "Kodos gen 7" {
		t.Errorf("junk timeout must fall back to the op default, title still parsed (double-quoted title): has=%v title=%q", has, title)
	}
}

func TestFstabMounts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	const fstab = `# kodos guest fstab — bogus UUIDs on purpose: no host device
# resolves them, so each spec must pass through verbatim (hermetic test)
UUID=00000000-1111-4222-8333-444444444444	/	ext4	noatime	0	1
UUID=11111111-2222-4333-8444-555555555555	/boot	vfat	defaults	0	0
UUID=22222222-3333-4444-8555-666666666666	/home	ext4	noauto	0	2
UUID=33333333-4444-4555-8666-777777777777	none	swap	sw	0	0
/dev/sr0	/media/cdrom	auto	ro,noauto	0	0
LABEL=kodos-no-such-volume	/data	ext4	defaults	0	2
tmpfs	/tmp	tmpfs	mode=1777,nosuid,nodev,size=25%	0	0

`
	fstabPath := filepath.Join(root, "etc", "fstab")
	if err := os.WriteFile(fstabPath, []byte(fstab), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts, err := fstabMounts(&system{chroot: root})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{ // point -> {partition, options}
		"/":     {"UUID=00000000-1111-4222-8333-444444444444", "noatime"},
		"/boot": {"UUID=11111111-2222-4333-8444-555555555555", "defaults"},
		"/data": {"LABEL=kodos-no-such-volume", "defaults"},
	}
	if len(mounts) != len(want) {
		t.Fatalf("comment/swap/none/noauto lines must be skipped, got %d actions: %v", len(mounts), mounts)
	}
	for _, a := range mounts {
		point := field(a, "point")
		w, ok := want[point]
		if !ok {
			t.Errorf("unexpected mount action for %q: %v", point, a)
			continue
		}
		if got := field(a, "partition"); got != w[0] {
			t.Errorf("point %s: partition %q, want %q (unresolvable specs pass through verbatim)", point, got, w[0])
		}
		if got := field(a, "options"); got != w[1] {
			t.Errorf("point %s: options %q, want %q", point, got, w[1])
		}
		if field(a, "op") != "mount" {
			t.Errorf("op must be mount: %v", a)
		}
	}

	// a plain device spec needs no UUID resolution at all
	if dev := fstabDevice("/dev/nbd0p2"); dev != "/dev/nbd0p2" {
		t.Errorf("device spec must stay verbatim: %q", dev)
	}

	// fstab without a / entry: no root device means no rollback (fail closed)
	if err := os.WriteFile(fstabPath, []byte("/dev/sda1 /data ext4 defaults 0 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fstabMounts(&system{chroot: root}); err == nil ||
		!strings.Contains(err.Error(), " / ") || !strings.Contains(err.Error(), fstabPath) {
		t.Errorf("missing root entry must error naming the fstab path: %v", err)
	}

	// no live fstab at all: same class
	if err := os.Remove(fstabPath); err != nil {
		t.Fatal(err)
	}
	if _, err := fstabMounts(&system{chroot: root}); err == nil ||
		!strings.Contains(err.Error(), "fstab") {
		t.Errorf("absent fstab must error, got %v", err)
	}
}

func TestStatusGenerationTable(t *testing.T) {
	root := t.TempDir()

	writeManifest(t, root, 0, &metadata{
		SchemaVersion: 1, Generation: 0, Distro: "alpine",
		Timestamp:  "2026-11-14T21:01:41Z",
		ConfigHash: "3bc71c0718e37452530950e2d81303a5b342db9c",
		Packages:   map[string]pkgPin{"a": {Version: "1"}, "b": {Version: "2"}, "c": {Version: "3"}},
	}, "")
	writeManifest(t, root, 1, nil, "this is not json {{{") // CORRUPT
	// generation 2: dir with NO generation.json
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "2"), 0o755); err != nil {
		t.Fatal(err)
	}
	// non-numeric junk dir + the current alias must not enter the table
	if err := os.MkdirAll(filepath.Join(root, "kod", "generation", "notagen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/0", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := statusTo(&buf, root); err != nil {
		t.Fatalf("status on a readable store: %v", err)
	}
	out := buf.String()
	t.Log("\n" + out)

	if !strings.Contains(out, "current → kod/generation/0") {
		t.Errorf("missing current line:\n%s", out)
	}
	if !strings.Contains(out, "2026-11-14T21:01:41Z") || !strings.Contains(out, "3bc71c0718e3") {
		t.Errorf("good generation must show its timestamp + short config_hash:\n%s", out)
	}
	if !strings.Contains(out, "corrupt") {
		t.Errorf("corrupt generation.json must be named as corrupt:\n%s", out)
	}
	if !strings.Contains(out, "no metadata") {
		t.Errorf("manifest-less generation must say no metadata:\n%s", out)
	}
	if strings.Contains(out, "notagen") {
		t.Errorf("non-numeric entry must not appear:\n%s", out)
	}
	if !strings.Contains(out, "no grub.cfg") {
		t.Errorf("absent grub.cfg must say so:\n%s", out)
	}

	// an empty store is reported honestly (and its path named exactly once)
	buf.Reset()
	empty := t.TempDir()
	if err := statusTo(&buf, empty); err != nil {
		t.Fatalf("status on an empty store: %v", err)
	}
	if !strings.Contains(buf.String(), "no generations under "+filepath.Join(empty, "kod", "generation")+"\n") {
		t.Errorf("empty store must name the gens dir once:\n%s", buf.String())
	}
}

func TestStatusBootDefaultAndBrokenCurrent(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, 0, &metadata{
		SchemaVersion: 1, Generation: 0, Distro: "alpine", Packages: map[string]pkgPin{},
	}, "")
	if err := os.MkdirAll(filepath.Join(root, "boot", "grub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "set default=0\nmenuentry 'Kodos generation 0' {\n\tlinux /vmlinuz-virt root=UUID=x\n}\n"
	if err := os.WriteFile(filepath.Join(root, "boot", "grub", "grub.cfg"), []byte(cfg), 0o444); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := statusTo(&buf, root); err != nil {
		t.Fatalf("status with no current alias: %v", err) // absent alias is not an error
	}
	out := buf.String()
	t.Log("\n" + out)
	if !strings.Contains(out, "boot default: Kodos generation 0") {
		t.Errorf("grub.cfg menu entry title must be reported as boot default:\n%s", out)
	}
	if !strings.Contains(out, "current") || !strings.Contains(out, "no current alias") {
		t.Errorf("absent alias must be reported, not crash:\n%s", out)
	}

	// relative -root must render the same tree: chrootResolve turns host paths
	// chroot-relative, so a relative root would double the prefix and every
	// host-side read misses (status absolutizes; cmdExec now does too).
	t.Chdir(root)
	buf.Reset()
	if err := statusTo(&buf, "."); err != nil {
		t.Fatalf("status with a relative root: %v", err)
	}
	if !strings.Contains(buf.String(), "boot default: Kodos generation 0") {
		t.Errorf("relative root must read grub.cfg:\n%s", buf.String())
	}

	// present-but-broken alias (points at a deleted generation): says BROKEN
	// and exits nonzero, still listing the generations it can read.
	if err := os.Symlink("/kod/generation/9", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	err := statusTo(&buf, root)
	out = buf.String()
	if err == nil {
		t.Fatalf("broken current alias must be an error, out:\n%s", out)
	}
	// single source: the BROKEN text lives in the returned error (main prints
	// it once as "ERROR: …"), never duplicated onto stdout
	if !strings.Contains(err.Error(), "BROKEN") || !strings.Contains(err.Error(), "9") {
		t.Errorf("broken alias error must say BROKEN and name the target: %v", err)
	}
	if strings.Contains(out, "BROKEN") {
		t.Errorf("broken text must not also go to stdout:\n%s", out)
	}
	if !strings.Contains(out, "GENERATION") || !strings.Contains(out, "boot default: Kodos generation 0") {
		t.Errorf("table + boot line must still render under a broken alias:\n%s", out)
	}

	// unparseable alias target (human hand-edit): same treatment
	if err := os.Remove(filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/notagen", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := statusTo(&buf, root); err == nil || !strings.Contains(err.Error(), "BROKEN") {
		t.Errorf("unparseable current alias must be a BROKEN error, got %v", err)
	}

	// "2abc" must NOT be believed as generation 2: strict Atoi, not a %d scan
	// (the dir does not exist either, so only a strict parse says so)
	if err := os.Remove(filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/kod/generation/2abc", filepath.Join(root, "kod", "generation", "current")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	err = statusTo(&buf, root)
	if err == nil || !strings.Contains(err.Error(), "not a generation number") {
		t.Errorf("alias target %q must not parse as generation 2, got %v", "2abc", err)
	}
}

func TestRollbackGenBootable(t *testing.T) {
	root := t.TempDir()
	mk := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// arch: kernel lives at usr/lib/modules/<kver>/vmlinuz-<flavor>; the
	// initramfs is rebuilt by the rollback stream's own install_bootloader.
	arch := filepath.Join(root, "gen-arch")
	if err := rollbackGenBootable(arch, "arch"); err == nil {
		t.Fatal("empty arch gen must not be bootable")
	}
	mk(filepath.Join(arch, "usr/lib/modules/6.18.65-1/vmlinuz-linux"))
	if err := rollbackGenBootable(arch, "arch"); err != nil {
		t.Fatalf("arch gen with kernel must be bootable: %v", err)
	}

	// alpine: needs the exact-kver initramfs pair in /boot.
	alp := filepath.Join(root, "gen-alp")
	mk(filepath.Join(alp, "boot/vmlinuz-6.18.63-r0"))
	if err := rollbackGenBootable(alp, "alpine"); err == nil {
		t.Fatal("alpine gen without initramfs must not be bootable")
	}
	mk(filepath.Join(alp, "boot/initramfs-6.18.63-r0"))
	if err := rollbackGenBootable(alp, "alpine"); err != nil {
		t.Fatalf("complete alpine gen must be bootable: %v", err)
	}
}
