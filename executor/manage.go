package main

// kodos status / rollback — the machine reading and rolling itself back.
//
//	status   [-root DIR]                       read-only: current alias, the
//	                                           generation table, boot default
//	rollback [N] [-root DIR] [-apk PATH]       re-activate an older generation
//
// Rollback does not invent a second activation path: it synthesizes a small
// action stream (mount entries from the LIVE /etc/fstab, the target's own
// kernel args, activate_generation, install_bootloader) and hands it to the
// SAME executor cmdExec runs, with -metadata <root>/kod/generation/N/generation.json.
// The executor's activate_generation case keys off metadata.Generation, so
// pointing it at an older manifest flips `current` through the existing
// machinery. Every input is read and validated BEFORE anything is activated
// (Review Focus 3): a missing/corrupt manifest, an unknown N or an
// unusable current alias all exit with nothing touched.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// currentGeneration resolves <chroot>/kod/generation/current exactly the way
// create_generation/prune do (filepath.Base + %d scan). n >= 0 only when the
// alias is present, parses, AND its target directory really exists; broken
// and absent aliases both give n < 0, with why naming the problem ("" when
// there simply is no alias yet — a machine that never applied).
func currentGeneration(chroot string) (n int, why string) {
	link := filepath.Join(chroot, "kod", "generation", "current")
	target, err := os.Readlink(link)
	if err != nil {
		return -1, "" // absent: nothing has been activated yet
	}
	// strict: Atoi, so "2abc" is BROKEN rather than silently generation 2
	got, convErr := strconv.Atoi(filepath.Base(target))
	if convErr != nil {
		return -1, fmt.Sprintf("BROKEN: current alias target %q is not a generation number", target)
	}
	if !isRealDir(filepath.Join(chroot, strings.TrimPrefix(target, "/"))) {
		return -1, fmt.Sprintf("BROKEN: current alias points at %s, which does not exist", target)
	}
	return got, ""
}

// generations lists the real generation directories under kod/generation,
// ascending. The current alias is a symlink, so ReadDir's IsDir() already
// drops it; hand-made non-numeric dirs are skipped.
func generations(chroot string) []int {
	ents, err := os.ReadDir(filepath.Join(chroot, "kod", "generation"))
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func genList(gens []int) string {
	if len(gens) == 0 {
		return "none"
	}
	parts := make([]string, len(gens))
	for i, g := range gens {
		parts[i] = strconv.Itoa(g)
	}
	return strings.Join(parts, " ")
}

// readableGenerations lists generations whose generation.json still loads:
// the list a failed rollback tells the user they can actually pass.
func readableGenerations(gens []int, s *system) []int {
	var out []int
	for _, g := range gens {
		if _, err := loadMetadata(filepath.Join(s.genDir(g), "generation.json")); err == nil {
			out = append(out, g)
		}
	}
	return out
}

// rollbackTarget picks which generation to activate:
//   - explicit N: must be an existing generation directory, else a hard error
//     naming N and listing the generations (N == current is allowed: it is a
//     plain re-activation + bootloader rerun).
//   - nil ("previous"): the highest generation STRICTLY below current that also
//     passes the runnable predicate (unbootable debris is skipped with a note).
//   - current < 0 means the alias is absent/broken/unparseable: there is no
//     safe implicit choice, so it errors and tells the user to pass N.
//
// The runnable predicate is applied only during the nil (default) walk;
// explicit -to N is NOT silently skipped — the caller gates it after selection.
func rollbackTarget(current int, n *int, gens []int, runnable func(int) bool) (int, error) {
	if n != nil {
		for _, g := range gens {
			if g == *n {
				return *n, nil
			}
		}
		return 0, fmt.Errorf("no generation %d: available generations: %s", *n, genList(gens))
	}
	if current < 0 {
		return 0, fmt.Errorf("cannot pick the previous generation (no usable current alias): pass an explicit generation; available generations: %s", genList(gens))
	}
	best := -1
	for _, g := range gens {
		if g >= current {
			continue
		}
		if runnable != nil && !runnable(g) {
			fmt.Printf("note: skipping generation %d (not runnable: sbin/init or bin/sh missing)\n", g)
			continue
		}
		if g > best {
			best = g
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no bootable generation below current %d: pass an explicit generation; available generations: %s", current, genList(gens))
	}
	return best, nil
}

// ---- status ----

func status(chroot string) error { return statusTo(os.Stdout, chroot) }

// statusTo renders the generation store. Read-only. A broken/absent current
// alias is reported, never crashed on; a broken alias is also returned as an
// error (exit 1) so scripts notice.
func statusTo(w io.Writer, chroot string) error {
	// absolute (like cmdExec): chrootResolve double-prefixes a relative root
	rootAbs, err := filepath.Abs(chroot)
	if err != nil {
		return err
	}
	s := &system{chroot: rootAbs}
	cur, why := currentGeneration(rootAbs)
	switch {
	case why != "":
		// Deliberately not echoed here: the error returned below is the single
		// source of that text (main prints it as "ERROR: …"). The table and the
		// boot-default line still print, so the report stays useful.
	case cur >= 0:
		fmt.Fprintf(w, "current → kod/generation/%d\n", cur)
	default:
		fmt.Fprintf(w, "current: no current alias (nothing activated)\n")
	}

	gens := generations(rootAbs)
	if len(gens) == 0 {
		fmt.Fprintf(w, "no generations under %s\n", filepath.Join(rootAbs, "kod", "generation"))
	} else {
		fmt.Fprintf(w, "%-11s %-22s %-13s %8s\n", "GENERATION", "TIMESTAMP", "CONFIG_HASH", "PACKAGES")
		for _, g := range gens {
			meta, err := loadMetadata(filepath.Join(s.genDir(g), "generation.json"))
			switch {
			case os.IsNotExist(err):
				fmt.Fprintf(w, "%-11d %-22s %-13s %8s\n", g, "no metadata", "", "")
			case err != nil:
				fmt.Fprintf(w, "%-11d %-22s %-13s %8s\n", g, "corrupt generation.json", "", "")
			default:
				hash := meta.ConfigHash
				if len(hash) > 12 {
					hash = hash[:12] // short form; full hashes stay in the manifest
				}
				fmt.Fprintf(w, "%-11d %-22s %-13s %8d\n", g, meta.Timestamp, hash, len(meta.Packages))
			}
		}
	}
	fmt.Fprintf(w, "boot default: %s\n", bootDefault(s))
	if why != "" {
		return fmt.Errorf("current: %s", why)
	}
	return nil
}

// grubCfg reads the live ESP grub.cfg that install_bootloader rendered: the
// first menu-entry title (status's boot-default line) and the timeout the
// machine currently boots with. The timeout matters for rollback: without it
// the op falls back to its own default (5) and silently rewrites the
// machine's declared `set timeout=3|10`.
func grubCfg(s *system) (title string, timeout int, hasTimeout bool, err error) {
	b, err := os.ReadFile(s.chrootResolve(filepath.Join(s.chroot, "boot", "grub", "grub.cfg")))
	if err != nil {
		return "", 0, false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if title == "" {
			if rest, ok := strings.CutPrefix(line, "menuentry "); ok {
				title = strings.TrimPrefix(strings.TrimSpace(rest), "'")
				title = strings.TrimPrefix(title, `"`) // grub allows either quote style
				if i := strings.IndexAny(title, `"'`); i > 0 {
					title = title[:i]
				}
			}
		}
		if !hasTimeout {
			if rest, ok := strings.CutPrefix(line, "set timeout="); ok {
				if n, convErr := strconv.Atoi(rest); convErr == nil {
					timeout, hasTimeout = n, true
				}
			}
		}
	}
	return title, timeout, hasTimeout, nil
}

func bootDefault(s *system) string {
	title, _, _, err := grubCfg(s)
	if err != nil {
		return "no grub.cfg"
	}
	if title == "" {
		return "no menu entry in grub.cfg"
	}
	return title
}

// ---- rollback ----

func rollback(chroot string, n *int, apkBin string) error {
	rootAbs, err := filepath.Abs(chroot)
	if err != nil {
		return err
	}
	s := &system{chroot: rootAbs}

	gens := generations(rootAbs)
	if len(gens) == 0 {
		return fmt.Errorf("no generations under %s", filepath.Join(rootAbs, "kod", "generation"))
	}
	cur, why := currentGeneration(rootAbs)
	// Runnable predicate: used by the nil-case walk to skip stranded generations.
	runnableFn := func(g int) bool {
		return checkGenRunnable(s.genDir(g), g, s.Distro) == nil
	}
	target, err := rollbackTarget(cur, n, gens, runnableFn)
	if err != nil {
		return err
	}

	// Everything the stream needs is read BEFORE anything can be activated:
	metaPath := filepath.Join(s.genDir(target), "generation.json")
	meta, err := loadMetadata(metaPath)
	if err != nil {
		return fmt.Errorf("cannot roll back to generation %d: %s is missing or corrupt (%v); usable generations: %s",
			target, metaPath, err, genList(readableGenerations(gens, s)))
	}
	if meta.Generation != target {
		return fmt.Errorf("cannot roll back to generation %d: its manifest declares generation %d", target, meta.Generation)
	}
	if meta.Distro != "alpine" {
		return fmt.Errorf("cannot roll back to generation %d: distro %q (this build runs the alpine/apk family only)", target, meta.Distro)
	}
	// activate_generation (IR rank 19) runs BEFORE install_bootloader (23), so a
	// generation the bootloader would fail on must never become current on the
	// way there. Same convention the op itself enforces: <gen>/boot/vmlinuz-<kver>
	// and the initramfs named after that exact kver.
	gen := s.genDir(target)
	matches, _ := filepath.Glob(filepath.Join(gen, "boot", "vmlinuz-*"))
	if len(matches) == 0 {
		return fmt.Errorf("cannot roll back to generation %d: no boot/vmlinuz-* in %s (kernel package missing?): refusing to activate it", target, gen)
	}
	kver := strings.TrimPrefix(filepath.Base(matches[0]), "vmlinuz-")
	if !lexists(filepath.Join(gen, "boot", "initramfs-"+kver)) {
		return fmt.Errorf("cannot roll back to generation %d: no boot/initramfs-%s in %s (mkinitfs never ran?): refusing to activate it", target, kver, gen)
	}
	// Explicit -to N: check the userland tree is runnable after all structural
	// checks pass (the nil walk already skipped non-runnable gens silently).
	// Fail closed: current alias + grub.cfg untouched.
	if n != nil {
		if err := checkGenRunnable(s.genDir(target), target, s.Distro); err != nil {
			return fmt.Errorf("rollback: %w", err)
		}
	}
	mounts, err := fstabMounts(s)
	if err != nil {
		return err
	}
	// Only now that every input validated does the banner state a fact rather
	// than an intent (it used to print before the checks failed).
	if why != "" {
		fmt.Printf("note: current alias is unusable (%s)\n", why)
	}
	if cur >= 0 {
		fmt.Printf("rollback: current generation %d → %d\n", cur, target)
	} else {
		fmt.Printf("rollback: generation %d (no usable current alias)\n", target)
	}

	_, timeout, hasTimeout, _ := grubCfg(s) // the machine's own grub timeout
	acts := make([]map[string]any, 0, len(mounts)+3)
	acts = append(acts, mounts...)
	acts = append(acts,
		map[string]any{"op": "set_kernel_arguments", "arguments": meta.KernelArgs},
		// activate_generation: not gated here — its tree was gated at create_generation
		// time (checkGenRunnable) and again above at rollback selection.
		map[string]any{"op": "activate_generation", "root": "/kod"},
		bootloaderAction(timeout, hasTimeout),
	)
	stream, err := json.Marshal(actionsFile{SchemaVersion: 1, Distro: meta.Distro, Actions: acts})
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "kodos-rollback-*.json") // 0600 by creation
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // removed once cmdExec has returned
	if _, err := tmp.Write(stream); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := cmdExec([]string{"-actions", name, "-metadata", metaPath, "-root", rootAbs, "-apk", apkBin}); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	fmt.Printf("reboot to boot generation %d\n", target)
	return nil
}

// bootloaderAction builds the synthesized install_bootloader action with only
// the fields the op actually reads: timeout comes from the live grub.cfg so a
// rollback keeps the machine's declared grub timeout instead of the op's
// default (5). Absent/unparseable -> field omitted, op default applies.
func bootloaderAction(timeout int, hasTimeout bool) map[string]any {
	act := map[string]any{"op": "install_bootloader"}
	if hasTimeout {
		act["timeout"] = timeout
	}
	return act
}

// fstabMounts turns the LIVE /etc/fstab into mount actions for the rollback
// stream. Those devices are already mounted (the machine is running from
// them), so the executor's skip-if-mounted guard turns each into a no-op —
// what the stream actually needs them for is capturing the root device,
// which install_bootloader insists on having seen mounted at /.
func fstabMounts(s *system) ([]map[string]any, error) {
	path := s.chrootResolve(filepath.Join(s.chroot, "etc", "fstab"))
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rollback needs the live fstab: %w", err)
	}
	var out []map[string]any
	hasRoot := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || strings.HasPrefix(f[0], "#") {
			continue
		}
		spec, point, fstype, options := f[0], f[1], f[2], f[3]
		if fstype == "swap" || point == "none" || fstype == "tmpfs" {
			continue // pseudo filesystems have no device for the executor to mount
		}
		if strings.Contains(options, "noauto") {
			continue // declared NOT to mount at boot: `mount -o noauto` would just fail
		}
		if point == "/" {
			hasRoot = true
		}
		out = append(out, map[string]any{
			"op": "mount", "partition": fstabDevice(spec), "point": point, "options": options,
		})
	}
	if !hasRoot {
		return nil, fmt.Errorf(`%s: no " / " entry: cannot roll back without the root device`, path)
	}
	return out, nil
}

// fstabDevice turns an fstab source spec into a device path. The bootloader
// op reads the root UUID/TYPE off it, and blkid does NOT accept a bare
// "UUID=..." argument (exit 2), so UUID=/LABEL= specs are resolved via
// `blkid -o device -t` first, then the udev symlink, and only as a last
// resort passed through verbatim (mount itself does accept NAME=value).
func fstabDevice(spec string) string {
	kind, val, ok := strings.Cut(spec, "=")
	if !ok {
		return spec
	}
	if dev, err := runOut("blkid", "-o", "device", "-t", kind+"="+val); err == nil && dev != "" {
		return dev
	}
	if p, err := filepath.EvalSymlinks("/dev/disk/by-" + strings.ToLower(kind) + "/" + val); err == nil {
		return p
	}
	return spec
}

// ---- CLI ----

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: kodos status [-root DIR]") }
	root := fs.String("root", "/", "target root filesystem")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return status(*root)
}

// parseRollbackArgs mirrors parseApplyArgs: Go's flag stops at the first
// non-flag, but the documented form puts flags after N, so N is lifted out
// first and any leftover positional is a hard error (never silently dropped).
func parseRollbackArgs(args []string) (n *int, root, apkBin string, err error) {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: kodos rollback [N] [-root DIR] [-apk PATH]") }
	fs.StringVar(&root, "root", "/", "target root filesystem")
	fs.StringVar(&apkBin, "apk", "apk", "apk binary for dependency resolution (executor passthrough)")
	pos, flagArgs := splitPositional(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return nil, "", "", err
	}
	if fs.NArg() != 0 {
		return nil, "", "", fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if pos != "" {
		v, err := strconv.Atoi(pos)
		if err != nil {
			return nil, "", "", fmt.Errorf("rollback: N must be a number, got %q (form: kodos rollback [N] [-root DIR] [-apk PATH])", pos)
		}
		n = &v
	}
	return n, root, apkBin, nil
}

func cmdRollback(args []string) error {
	n, root, apkBin, err := parseRollbackArgs(args)
	if err != nil {
		return err
	}
	return rollback(root, n, apkBin)
}
