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
	"errors"
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
	base := filepath.Base(target)
	var got int
	if _, scanErr := fmt.Sscanf(base, "%d", &got); scanErr != nil {
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
//   - nil ("previous"): the highest generation STRICTLY below current.
//   - current < 0 means the alias is absent/broken/unparseable: there is no
//     safe implicit choice, so it errors and tells the user to pass N.
func rollbackTarget(current int, n *int, gens []int) (int, error) {
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
		if g < current && g > best {
			best = g
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no generation below current %d: pass an explicit generation; available generations: %s", current, genList(gens))
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
		fmt.Fprintf(w, "current: %s\n", why)
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
		return errors.New(why)
	}
	return nil
}

// bootDefault reads the boot-default menu entry title out of the grub.cfg the
// bootloader op renders (<root>/boot/grub/grub.cfg, mode 0444, written only
// after a successful grub-install). chrootResolve because a non-mounted /boot
// is a chroot-rooted link whose raw host path dangles.
func bootDefault(s *system) string {
	b, err := os.ReadFile(s.chrootResolve(filepath.Join(s.chroot, "boot", "grub", "grub.cfg")))
	if err != nil {
		return "no grub.cfg"
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "menuentry ")
		if !ok {
			continue
		}
		title := strings.TrimSpace(rest)
		title = strings.TrimPrefix(title, "'")
		title = strings.TrimPrefix(title, `"`)
		if i := strings.IndexAny(title, `"'`); i > 0 {
			title = title[:i]
		}
		if title != "" {
			return title
		}
	}
	return "no menu entry in grub.cfg"
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
	if why != "" {
		fmt.Printf("note: current alias is unusable (%s)\n", why)
	}
	target, err := rollbackTarget(cur, n, gens)
	if err != nil {
		return err
	}
	if cur >= 0 {
		fmt.Printf("rollback: current generation %d → %d\n", cur, target)
	} else {
		fmt.Printf("rollback: generation %d (no usable current alias)\n", target)
	}

	// Everything the stream needs is read BEFORE anything can be activated:
	// a target whose manifest is missing or corrupt must fail here (and name
	// what is usable), not half-activate (Review Focus 3).
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
	// The bootloader op reads the kernel/initramfs out of the target tree.
	// activate_generation (IR rank 18) runs BEFORE install_bootloader (22), so
	// a half-populated generation would already be current when the bootloader
	// leg fails: check its kernel is there before touching anything.
	if matches, _ := filepath.Glob(filepath.Join(s.genDir(target), "boot", "vmlinuz-*")); len(matches) == 0 {
		return fmt.Errorf("cannot roll back to generation %d: no boot/vmlinuz-* in %s (kernel package missing?): refusing to activate it", target, s.genDir(target))
	}
	mounts, err := fstabMounts(s)
	if err != nil {
		return err
	}

	acts := make([]map[string]any, 0, len(mounts)+3)
	acts = append(acts, mounts...)
	acts = append(acts,
		map[string]any{"op": "set_kernel_arguments", "arguments": meta.KernelArgs},
		map[string]any{"op": "activate_generation", "root": "/kod"},
		map[string]any{"op": "install_bootloader", "type": "grub", "efi": true},
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
		if fstype == "swap" || point == "none" {
			continue // swap has no mountpoint for the executor to key off
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
	fs.SetOutput(io.Discard)
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
	fs.SetOutput(io.Discard)
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
