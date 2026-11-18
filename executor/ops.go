package main

// ops.go — the ops the spike previously refused: storage (partition/format/
// swap/mount), chroot infrastructure (pseudo-fs mounts, chroot sh,
// .post-install scripts), per-distro file rendering (locale/keyboard/
// timezone/modules/service settings), users, services, bootloader.
// All of these need root on a real target; the pure logic (partition
// planning, grub.cfg rendering, settings keys) is unit-checked in selftest.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "),
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// parseMounts parses /proc/mounts text and returns mountpoint→device map.
// Handles octal-escaped spaces (\040) in fields.
// All entries are kept (tmpfs/bind mounts with non-/ source are valid mountpoints;
// extra refusals are always safe — a false refusal is better than a missed one).
func parseMounts(text string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		m[unescapeMount(f[1])] = unescapeMount(f[0])
	}
	return m
}

// isMounted returns true if device itself or any partition of it (the whole
// disk case: /dev/nbd0 → /dev/nbd0p2, /dev/sda3) appears as a mounted device.
// ponytail: path-based matching only; dm/LVM targets show as /dev/dm-N and
// are not resolved to their underlying disks. Upgrade: compare st_dev of the
// device node against mounted devices' st_dev if dm/LVM support is needed.
func isMounted(device string, mounts map[string]string) bool {
	for _, dev := range mounts {
		if dev == device {
			return true
		}
		// partition: device name + optional "p" + digits (e.g. nbd0p1, sda3)
		if strings.HasPrefix(dev, device) {
			rest := dev[len(device):]
			if len(rest) > 0 && (rest[0] == 'p' || (rest[0] >= '0' && rest[0] <= '9')) {
				// rest must be only "p" + digits or just digits
				check := rest
				if check[0] == 'p' {
					check = check[1:]
				}
				allDigits := len(check) > 0
				for _, c := range check {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					return true
				}
			}
		}
	}
	return false
}

func liveMounts() (map[string]string, error) {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	return parseMounts(string(b)), nil
}

// mountedAt / diskBusy read /proc/mounts: the executor must never carve up
// a disk the HOST is using (the config carries the real machine's device
// names; a wrong layer or typo must not nuke the running system).
func mountedTargetAt(target string, mounts map[string]string) (string, bool) {
	dev, ok := mounts[target]
	return dev, ok
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func diskBusy(device string, mounts map[string]string) bool {
	return isMounted(device, mounts)
}

// isMountpoint: different st_dev than the parent (no external tools).
func isMountpoint(path string) bool {
	var a, b syscall.Stat_t
	if syscall.Lstat(path, &a) != nil || syscall.Lstat(filepath.Dir(path), &b) != nil {
		return false
	}
	return a.Dev != b.Dev
}

// ---- partition planning (pure, selftested) ----

type partSpec struct {
	Index int
	Name  string
	Size  string // "512MB" | "64GB" | "34GB" | "100%"
	Type  string // esp | linux | linux-swap
}

type partRange struct {
	Index   int
	Name    string
	Start   int64  // MiB
	End     int64  // MiB, inclusive
	PartedF string // semantic type (esp|linux|linux-swap)
	Hint    string // parted fs-type token for mkpart (type-GUID hint)
}

func sizeMiB(size string) (int64, bool) {
	// ponytail: binary rounding (MB==MiB, GB==GiB); GPT tolerates the
	// few-percent slack and no consumer cares about exact byte counts.
	num := strings.TrimRight(size, "ABGMB%")
	isPct := strings.HasSuffix(size, "%")
	n, err := strconv.ParseInt(strings.TrimRight(num, "GMB"), 10, 64)
	if err != nil {
		return 0, false
	}
	switch {
	case isPct:
		return n, true // percent, caller resolves
	case strings.HasSuffix(size, "GB"):
		return n * 1024, true
	default: // MB
		return n, true
	}
}

// planPartitions lays out MiB ranges; "100%" = rest of the disk MINUS the
// explicit sizes that come AFTER it (the latent eszkoz bug: home 100%
// before swap 34G would otherwise leave swap no room and fail parted).
func planPartitions(diskMiB int64, specs []partSpec) ([]partRange, error) {
	var out []partRange
	start := int64(1) // 1MiB alignment
	for i, sp := range specs {
		size, ok := sizeMiB(sp.Size)
		if !ok {
			return nil, fmt.Errorf("partition %s: bad size %q", sp.Name, sp.Size)
		}
		isPct := strings.HasSuffix(sp.Size, "%")
		var end int64
		if isPct {
			if size != 100 {
				return nil, fmt.Errorf("partition %s: only 100%% supported", sp.Name)
			}
			end = diskMiB - 1
			for _, later := range specs[i+1:] {
				lsz, lok := sizeMiB(later.Size)
				if !lok {
					return nil, fmt.Errorf("partition %s: bad size %q", later.Name, later.Size)
				}
				if strings.HasSuffix(later.Size, "%") {
					return nil, fmt.Errorf("partition %s: only one 100%% partition allowed", later.Name)
				}
				end -= lsz
			}
		} else {
			end = start + size - 1
		}
		if end <= start {
			return nil, fmt.Errorf("partition %s: no room on a %dMiB disk (start %d, end %d)",
				sp.Name, diskMiB, start, end)
		}
		pfs := map[string]string{"esp": "esp", "linux": "linux", "linux-swap": "linux-swap"}[sp.Type]
		if pfs == "" {
			pfs = "linux"
		}
		// NOTE: these are semantic types; parted's mkpart wants an actual
		// fs-type as the type-GUID hint (esp=fat32+flag, linux=ext4-ish
		// data, linux-swap is itself a parted token). Real filesystems are
		// made by the format ops; the hint only sets the right GPT type.
		hint := map[string]string{"esp": "fat32", "linux": "ext4",
			"linux-swap": "linux-swap"}[pfs]
		if hint == "" {
			hint = "ext4"
		}
		out = append(out, partRange{Index: sp.Index, Name: sp.Name, Start: start, End: end, PartedF: pfs, Hint: hint})
		start = end + 1
	}
	return out, nil
}

func specsFromAction(act map[string]any) ([]partSpec, error) {
	raw, _ := act["partitions"].([]any)
	if len(raw) == 0 {
		return nil, fmt.Errorf("partition_disk: no partitions[] in action (schema_version too old?)")
	}
	var specs []partSpec
	for _, r := range raw {
		m, _ := r.(map[string]any)
		idx, _ := m["index"].(float64)
		sp := partSpec{Index: int(idx), Name: field(m, "name"),
			Size: field(m, "size"), Type: field(m, "type")}
		if sp.Name == "" || sp.Size == "" {
			return nil, fmt.Errorf("partition_disk: incomplete partition entry")
		}
		specs = append(specs, sp)
	}
	return specs, nil
}

// grubEntry: one boot menu entry per generation; dir is the ESP-relative
// directory holding that generation's kernel+initramfs ("" = ESP root).
type grubEntry struct {
	gen  int
	kver string
	dir  string
}

// renderGrubCfg is pure (selftested): ESP-relative kernel paths (vfat,
// grub reads it natively), root by UUID, serialized terminals so the
// qemu -nographic console shows the boot. Current generation first
// (set default=0), every other surviving generation after it.
func renderGrubCfg(timeout int, uuid string, args []string, entries []grubEntry) string {
	cmdline := fmt.Sprintf("root=UUID=%s ro", uuid)
	if len(args) > 0 {
		cmdline += " " + strings.Join(args, " ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, `set default=0
set timeout=%d
serial --unit=0 --speed=115200
terminal_input serial
terminal_output serial console
`, timeout)
	for _, en := range entries {
		fmt.Fprintf(&b, `menuentry 'Kodos generation %d' {
	linux %s/vmlinuz-%s %s
	initrd %s/initramfs-%s
}
`, en.gen, en.dir, en.kver, cmdline, en.dir, en.kver)
	}
	return b.String()
}

// settingsKey: neutral snake_case -> sshd-style CamelCase keys (pure,
// selftested). Honest limit: per-service key dialects beyond CamelCase
// need backend settings knowledge (same known limit as the schema).
// uuidToken matches {UUID:<device>} placeholders in write_file content
// (fstab: Lua cannot know UUIDs — the executor formats/mounts the devices
// BEFORE write_file runs, so it resolves them here; guests then mount by
// stable UUID instead of install-time device names like /dev/nbd6p1).
var uuidToken = regexp.MustCompile(`\{UUID:([^}]+)\}`)

// expandUUIDTokens is pure (selftested): uuidOf resolves a device to its
// UUID; any failure aborts (a half-substituted fstab would fail at boot).
func expandUUIDTokens(content string, uuidOf func(string) (string, error)) (string, error) {
	var firstErr error
	out := uuidToken.ReplaceAllStringFunc(content, func(tok string) string {
		if firstErr != nil {
			return tok
		}
		uuid, err := uuidOf(uuidToken.FindStringSubmatch(tok)[1])
		if err != nil {
			firstErr = fmt.Errorf("token %s: %w", tok, err)
			return tok
		}
		return "UUID=" + uuid
	})
	return out, firstErr
}

func settingsKey(k string) string {
	var b strings.Builder
	up := true
	for _, r := range k {
		if r == '_' {
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// copyTree copies the host directory src into chroot-mapped dst.
// Dirs: 0755; regular files: 0644; symlinks: verbatim (Readlink→Symlink).
// Idempotent: existing files are overwritten, existing links recreated.
func (e *executor) copyTree(act map[string]any) error {
	src := field(act, "src")
	dst := field(act, "dst")
	name := field(act, "name")
	if src == "" || dst == "" {
		return fmt.Errorf("copy_tree: missing src or dst")
	}
	// map dst (target-rooted) into the actual host path under chroot
	hostDst := filepath.Join(e.s.chroot, dst)
	if filepath.Clean(src) == filepath.Clean(hostDst) {
		fmt.Printf("copy_tree %s: src and dst are the same directory, skipping\n", name)
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		// resolve chroot-rooted managed links first (same rule as writeFileTo):
		// a real installed root ships top-level dirs as symlinks into
		// /kod/generation/... whose targets are meaningful ONLY rooted at the
		// chroot — a raw Join MkdirAlls onto the symlink ("file exists") and
		// file writes would dangle to the host's /kod. Scratch tmpfs roots
		// never carry such links; that masked this until ordering put copy
		// after a populated-root mount (paste round 2, 2026-11-14).
		target := e.s.chrootResolve(filepath.Join(hostDst, rel))
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("replace symlink %s: %w", target, err)
			}
			return os.Symlink(link, target)
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, 0o644)
		}
	})
}

// ---- op handlers ----

func (e *executor) partitionDisk(act map[string]any) error {
	device := field(act, "device")
	if !strings.HasPrefix(device, "/dev/") {
		return fmt.Errorf("partition_disk: %q is not a device path", device)
	}
	mounts, err := liveMounts()
	if err != nil {
		return fmt.Errorf("cannot read /proc/mounts, refusing to partition disk %s: %w", device, err)
	}
	if diskBusy(device, mounts) {
		return fmt.Errorf("refusing to partition mounted disk %s", device)
	}
	if out, err := runOut("sfdisk", "-d", device); err == nil && strings.Contains(out, "label:") {
		// idempotent ONLY when all expected partitions exist; a half-done
		// table is never auto-repaired (never re-partition a possibly-live disk)
		specs, err := specsFromAction(act)
		if err != nil {
			return err
		}
		have := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, device+"p") && strings.Contains(line, ":") {
				have++
			}
		}
		if have >= len(specs) {
			fmt.Printf("%s already partitioned: leaving alone\n", device)
			return nil
		}
		return fmt.Errorf("%s has a partition table but only %d/%d partitions: refusing to re-partition (wipe it, e.g. dd if=/dev/zero of=%s bs=1M count=10, or delete the VM disk)",
			device, have, len(specs), device)
	}
	if err := run("parted", "-s", device, "mklabel", field(act, "table")); err != nil {
		return err
	}
	out, err := runOut("parted", "-sm", device, "unit", "MiB", "print")
	if err != nil {
		return err
	}
	var diskMiB int64
	for _, line := range strings.Split(out, "\n") {
		// machine format (-sm): the disk line is "<device>:<size>MiB:..."
		if rest, ok := strings.CutPrefix(line, device+":"); ok {
			sizeField := strings.Split(rest, ":")[0]
			f, err := strconv.ParseFloat(strings.TrimSuffix(sizeField, "MiB"), 64)
			if err != nil {
				return fmt.Errorf("cannot parse disk size: %q", line)
			}
			diskMiB = int64(f)
		}
	}
	if diskMiB == 0 {
		return fmt.Errorf("disk size not found for %s", device)
	}
	specs, err := specsFromAction(act)
	if err != nil {
		return err
	}
	ranges, err := planPartitions(diskMiB, specs)
	if err != nil {
		return err
	}
	for _, r := range ranges {
		if err := run("parted", "-s", device, "mkpart", r.Name, r.Hint,
			fmt.Sprintf("%dMiB", r.Start), fmt.Sprintf("%dMiB", r.End)); err != nil {
			return err
		}
		if r.PartedF == "esp" {
			if err := run("parted", "-s", device, "set", strconv.Itoa(r.Index), "esp", "on"); err != nil {
				return err
			}
		}
		fmt.Printf("%s: %s %s %dMiB-%dMiB\n", device, r.Name, r.PartedF, r.Start, r.End)
	}
	return nil
}

func (e *executor) formatPartition(act map[string]any) error {
	part := field(act, "partition")
	mounts, err := liveMounts()
	if err != nil {
		return fmt.Errorf("cannot read /proc/mounts, refusing to format %s: %w", part, err)
	}
	if isMounted(part, mounts) {
		return fmt.Errorf("refusing to format mounted target %s", part)
	}
	fs := field(act, "filesystem")
	if out, _ := runOut("blkid", "-o", "value", "-s", "TYPE", part); existingBlocksFormat(out, fs) {
		// never re-mkfs a live partition — but a fs torn by an earlier
		// crashed run would poison later ops with mysterious I/O errors,
		// so repair the fs we are about to reuse
		return repairFS(out, part)
	}
	switch fs {
	case "ext4":
		return run("mkfs.ext4", "-q", part)
	case "vfat":
		return run("mkfs.vfat", "-F", "32", part)
	default:
		return fmt.Errorf("format: unsupported filesystem %q", fs)
	}
}

// existingBlocksFormat: does the filesystem blkid found on the partition
// protect it from re-formatting? A stale SWAP signature (crashed-run
// leftover, stray mkswap) is not user data: the config says this partition
// is a real fs, so format over it (self-heal; real bare-metal round
// 2026-11-16). Anything that is an actual filesystem — incl. foreign types —
// stays protected.
func existingBlocksFormat(existing, want string) bool {
	if existing == "" {
		return false
	}
	if existing == "swap" && (want == "ext4" || want == "vfat") {
		return false
	}
	return true
}

// mkswapDestroysRealFS: the swap-designated partition carries data a
// silent mkswap would erase; refuse and let the operator decide.
func mkswapDestroysRealFS(existing string) bool {
	return existing != "" && existing != "swap"
}

// repairFS: preen-check an existing filesystem before reusing it. fsck
// exit 0=clean 1=repaired 2=repaired,reboot — all fine. >=4 uncorrected:
// hard error, do NOT build a generation on a broken fs. Unknown host:
// if the fsck tool is not installed, proceed as before.
func repairFS(fstype, part string) error {
	var tool string
	switch fstype {
	case "vfat":
		tool = "fsck.vfat"
	case "ext2", "ext3", "ext4":
		tool = "e2fsck"
	default:
		return nil // swap etc: nothing to check
	}
	if _, err := exec.LookPath(tool); err != nil {
		return nil
	}
	// dosfsck -a is timid: it SKIPS the fixes that matter (broken LFN
	// fragments stay, and every later op hitting that dir gets EIO).
	// -y answers yes to every repair.
	args := []string{"-y", part}
	if tool == "e2fsck" {
		args = []string{"-p", part} // preen
	}
	out, err := exec.Command(tool, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		// fsck exits are bitmasks: 1/2/3 (repaired) pass; 4 uncorrected,
		// 8 operational, 16 usage = cannot trust this fs, hard error
		if bad := ee.ExitCode() & (4 | 8 | 16); bad != 0 {
			return fmt.Errorf("format: %s cannot repair %s (exit %d):\n%s\nthe on-disk fs is too damaged to reuse: run vm/run.sh fresh",
				tool, part, ee.ExitCode(), strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		fmt.Printf("format: %s repaired %s (%s)\n", tool, part, strings.ReplaceAll(s, "\n", "; "))
	}
	return nil
}

func (e *executor) createSwap(act map[string]any) error {
	part := field(act, "partition")
	mounts, err := liveMounts()
	if err != nil {
		return fmt.Errorf("cannot read /proc/mounts, refusing to create swap %s: %w", part, err)
	}
	if isMounted(part, mounts) {
		return fmt.Errorf("refusing to format mounted target %s (swap)", part)
	}
	if out, _ := runOut("blkid", "-o", "value", "-s", "TYPE", part); out == "swap" {
		return nil
	} else if mkswapDestroysRealFS(out) {
		return fmt.Errorf("create_swap: %s carries a %s filesystem; refusing to mkswap over data", part, out)
	}
	return run("mkswap", part)
}

func (e *executor) mount(act map[string]any) error {
	part, point := field(act, "partition"), field(act, "point")
	target := filepath.Join(e.s.chroot, point)
	mounts, err := liveMounts()
	if err != nil {
		return fmt.Errorf("cannot read /proc/mounts, refusing to mount %s: %w", target, err)
	}
	if dev, ok := mountedTargetAt(target, mounts); ok {
		fmt.Printf("mount %s: already mounted, skipping\n", target)
		if point == "/" {
			// R2: rootPart must be set even on skip (bootloader UUID path);
			// use the LIVE device from /proc/mounts, not the config's name:
			// stale config device names are not blkid-resolvable.
			if dev != part {
				fmt.Printf("mount /: config names %s but live device is %s — using the live one\n", part, dev)
			}
			e.rootPart = dev
		}
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	// live/installer kernels do not always autoload the fs driver at mount
	// time (busybox mount then fails EINVAL); ask kmod first, best effort.
	if fsType, _ := runOut("blkid", "-o", "value", "-s", "TYPE", part); fsType != "" {
		run("modprobe", fsType)
	}
	mountArgs := []string{}
	// Pseudo filesystems (tmpfs /tmp) have no device for blkid to type;
	// name their type explicitly instead of relying on fstab lookup.
	if fs := field(act, "filesystem"); fs != "" {
		mountArgs = append(mountArgs, "-t", fs)
	}
	mountArgs = append(mountArgs, "-o", field(act, "options"), part, target)
	if err := run("mount", mountArgs...); err != nil {
		return err
	}
	if point == "/" {
		e.rootPart = part
	}
	return nil
}

// ---- chroot infrastructure (root only) ----

func (e *executor) ensureChrootMounts() error {
	if e.chrootMounted {
		return nil
	}
	for _, name := range []string{"proc", "sys", "dev", "run"} {
		mp := filepath.Join(e.s.chroot, name)
		if _, err := os.Readlink(mp); err == nil && !dirExists(mp) {
			os.Remove(mp) // dangling generation link: mountpoints must be real dirs
		}
		if err := os.MkdirAll(mp, 0o755); err != nil {
			return err
		}
	}
	if !isMountpoint(filepath.Join(e.s.chroot, "proc")) {
		if err := run("mount", "-t", "proc", "proc", filepath.Join(e.s.chroot, "proc")); err != nil {
			return err
		}
	}
	for _, src := range []string{"/sys", "/dev"} {
		target := filepath.Join(e.s.chroot, strings.TrimPrefix(src, "/"))
		if !isMountpoint(target) {
			if err := run("mount", "--rbind", src, target); err != nil {
				return err
			}
			run("mount", "--make-rslave", target)
		}
	}
	runTarget := filepath.Join(e.s.chroot, "run")
	if !isMountpoint(runTarget) {
		if err := run("mount", "-t", "tmpfs", "tmpfs", runTarget); err != nil {
			return err
		}
	}
	resolv := filepath.Join(e.s.chroot, "etc/resolv.conf")
	if !lexists(resolv) {
		b, err := os.ReadFile("/etc/resolv.conf")
		if err == nil {
			os.WriteFile(resolv, b, 0o644)
		}
	}
	e.chrootMounted = true
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (e *executor) umountChroot() {
	if e.s.chroot == "/" {
		return // on-machine run: ensureChrootMounts created no new mounts (all four already mountpoints), so unmount nothing
	}
	for _, name := range []string{"run", "dev", "sys", "proc"} {
		mp := filepath.Join(e.s.chroot, name)
		// NEVER propagate unmounts outward: systemd shares /, and an rbind
		// copy of /dev that stays shared will take the HOST's /dev/pts down
		// with umount -R (killed every pty on the machine once).
		run("mount", "--make-rprivate", mp)
		exec.Command("umount", "-R", mp).Run() // best-effort
	}
}

// ensureGenChrootMounts binds the kernel API filesystems into a raw generation
// tree for the duration of the hook sweep. The gen tree may not have all
// target dirs (baselayout creates /dev etc via its own hook), so we SKIP any
// that are not yet real directories. We also skip any that are already
// mountpoints (live-machine apply where the gen may be the previous gen with
// live mounts already in place). On partial failure, already-established
// mounts are torn down before returning (umountGenChroot is idempotent via its
// isMountpoint guard), so the caller's defer is only needed for the success path.
func ensureGenChrootMounts(genDir string) error {
	// proc: mount first, then demote (mirror ensureChrootMounts' order)
	if isRealDir(filepath.Join(genDir, "proc")) && !isMountpoint(filepath.Join(genDir, "proc")) {
		if err := run("mount", "-t", "proc", "proc", filepath.Join(genDir, "proc")); err != nil {
			umountGenChroot(genDir) // roll back any earlier mounts
			return fmt.Errorf("gen-chroot mount proc: %w", err)
		}
		run("mount", "--make-rprivate", filepath.Join(genDir, "proc"))
	}
	// sys + dev: rbind, then make-rslave
	for _, src := range []string{"/sys", "/dev"} {
		target := filepath.Join(genDir, strings.TrimPrefix(src, "/"))
		if !isRealDir(target) || isMountpoint(target) {
			continue
		}
		if err := run("mount", "--rbind", src, target); err != nil {
			umountGenChroot(genDir)
			return fmt.Errorf("gen-chroot rbind %s: %w", src, err)
		}
		run("mount", "--make-rslave", target)
	}
	// run: fresh tmpfs (private by default — no --make-rprivate needed)
	if isRealDir(filepath.Join(genDir, "run")) && !isMountpoint(filepath.Join(genDir, "run")) {
		if err := run("mount", "-t", "tmpfs", "tmpfs", filepath.Join(genDir, "run")); err != nil {
			umountGenChroot(genDir)
			return fmt.Errorf("gen-chroot mount run: %w", err)
		}
	}
	return nil
}

// umountGenChroot tears down the API mounts ensureGenChrootMounts created.
// Called unconditionally (defer) after the hook sweep — same rprivate-first,
// -R-after discipline as umountChroot to avoid propagating unmounts outward.
func umountGenChroot(genDir string) {
	for _, name := range []string{"run", "dev", "sys", "proc"} {
		mp := filepath.Join(genDir, name)
		if !isMountpoint(mp) {
			continue // not mounted by us (dir absent or already-live mount)
		}
		run("mount", "--make-rprivate", mp)
		exec.Command("umount", "-R", mp).Run() // best-effort
	}
}

// chrootSh / chrootExecScript run in an EXPLICIT root dir. When dir is the
// live chroot, the kernel API filesystems are mirrored first via
// ensureChrootMounts. When dir is a raw generation tree, the caller
// (runPostInstalls) has already called ensureGenChrootMounts + deferred
// umountGenChroot: hooks get a proper chroot with /proc,/dev,/sys,/run so
// that boot-critical post-install steps (busybox --install creates /sbin/init
// + all applets; openrc rc-update links; kernel depmod) have the kernel
// interfaces they need.
func (e *executor) chrootSh(dir, script string) error {
	if dir == e.s.chroot {
		if err := e.ensureChrootMounts(); err != nil {
			return err
		}
	}
	cmd := exec.Command("chroot", dir, "/bin/sh", "-l", "-c", script)
	cmd.Dir = dir // the inode becomes "/" after chroot(2)
	// NOT the host's PATH: it lacks /bin,/sbin,/usr/sbin (Arch host) and
	// points at host-only dirs — chrooted scripts must resolve against the
	// target's own layout (busybox applets live in /bin,/sbin).
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin", "HOME=/", "TERM=dumb"}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// chrootShOut runs a chrooted command capturing combined output (for
// idempotence checks like rc-update's "already been added").
func (e *executor) chrootShOut(script string) (string, error) {
	if err := e.ensureChrootMounts(); err != nil {
		return "", err
	}
	cmd := exec.Command("chroot", e.s.chroot, "/bin/sh", "-l", "-c", script)
	cmd.Dir = e.s.chroot
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin", "HOME=/", "TERM=dumb"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// genChrootSh runs a script chrooted into the ACTIVE generation tree.
// The live root is a bag of top-level symlinks (/usr -> current/usr),
// which breaks realpath-following tools: mkinitfs' lddtree staged the
// hardlinked real files under /kod/... and left relative SONAME links
// (/usr/lib/libblkid.so.1 -> libblkid.so.1.1.0) DANGLING inside the
// initramfs -> "Error loading shared library" panic path. Inside the
// generation tree every dir is real and files are hardlinks, so paths
// resolve like a vanilla root. Its /etc entries hardlink into the gen's
// etc_store; the kod bind keeps /kod-rooted paths (current aliases, cpio
// staging) resolvable inside the tree.
func (e *executor) genChrootSh(script string) error {
	// NOT ".../current": that symlink targets /kod/generation/N which is
	// chroot-rooted and would resolve against the HOST from out here.
	gen := filepath.Join(e.s.chroot, "kod/generation", strconv.Itoa(e.meta.Generation))
	if !dirExists(gen) {
		return fmt.Errorf("genChrootSh: no activated generation at %s", gen)
	}
	if err := e.ensureChrootMounts(); err != nil { // live-root mounts to mirror in
		return err
	}
	var binds []string
	bind := func(name string) error {
		src := filepath.Join(e.s.chroot, name)
		dst := filepath.Join(gen, name)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := run("mount", "--rbind", src, dst); err != nil {
			return err
		}
		run("mount", "--make-rprivate", dst) // never propagate umounts outward
		binds = append(binds, dst)
		return nil
	}
	for _, n := range []string{"kod", "proc", "sys", "dev", "run"} {
		if err := bind(n); err != nil {
			for _, b := range binds {
				run("mount", "--make-rprivate", b)
				exec.Command("umount", "-R", b).Run()
			}
			return err
		}
	}
	defer func() {
		for _, b := range binds {
			run("mount", "--make-rprivate", b)
			exec.Command("umount", "-R", b).Run() // best-effort
		}
	}()
	cmd := exec.Command("chroot", gen, "/bin/sh", "-l", "-c", script)
	cmd.Dir = gen
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin", "HOME=/", "TERM=dumb"}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// runPostInstalls executes package .pre-install/.post-install scripts
// chrooted into the generation tree, at CREATE time (main.go). The gen
// tree gets proper API filesystem mounts (/proc,/dev,/sys,/run) for the
// duration of the hook sweep: busybox's post-install calls
// `/bin/busybox --install -s` which creates all applet symlinks including
// /sbin/init, /bin/sh, etc.; openrc's post-install uses /bin/sh; kernel
// hooks use depmod. Without these mounts some hooks silently skip steps,
// leaving an unrunnable generation. Mounts are torn down unconditionally
// after the sweep (rprivate-first, umount -R, same discipline as
// umountChroot). The scripts live in the store (outside the gen tree),
// so each is staged at <genDir>/tmp/.kodos-hook-<pkg>-<kind>.sh (0755),
// executed, then removed.
func (e *executor) runPostInstalls(genDir string, toInstall map[string]string) error {
	names := make([]string, 0, len(toInstall))
	for p := range toInstall {
		names = append(names, p)
	}
	slices.Sort(names)
	if i := slices.Index(names, "busybox"); i > 0 { // provides /bin/sh for the rest
		names = append([]string{"busybox"}, append(names[:i:i], names[i+1:]...)...)
	}
	if err := os.MkdirAll(filepath.Join(genDir, "tmp"), 0o755); err != nil {
		return err
	}
	if err := ensureGenChrootMounts(genDir); err != nil {
		return fmt.Errorf("gen-chroot mounts: %w", err)
	}
	defer umountGenChroot(genDir) // success path: tear down after the full sweep

	before := e.snapshotRunlevels(genDir) // hooks may rc-update add: protect those entries
	for _, p := range names {
		// apk runs .pre-install before a package's files land and
		// .post-install after; Alpine's pre-install hooks only need
		// busybox adduser/addgroup (landed first), so running both in
		// one sweep is fine — and system users like dbus's messagebus
		// are created ONLY there, nothing else in the package metadata
		// declares them.
		for _, kind := range []string{".pre-install", ".post-install"} {
			script := filepath.Join(e.s.store(), p, toInstall[p], kind)
			if !lexists(script) {
				continue
			}
			content, err := os.ReadFile(script)
			if err != nil {
				return err
			}
			name := ".kodos-hook-" + p + "-" + strings.TrimPrefix(kind, ".") + ".sh"
			staged := filepath.Join(genDir, "tmp", name)
			if err := os.WriteFile(staged, content, 0o755); err != nil {
				return err
			}
			fmt.Printf("%s: %s\n", kind[1:], p)
			err = e.chrootExecScript(genDir, "/tmp/"+name)
			if err != nil {
				// shebang-less or non-exec script: plain sh fallback (/bin/sh
				// exists by now if busybox ran)
				if err2 := e.chrootSh(genDir, "/tmp/"+name); err2 != nil {
					os.Remove(staged)
					return fmt.Errorf("%s %s: %w", kind[1:], p, err)
				}
			}
			os.Remove(staged)
		}
	}
	hooked := e.snapshotRunlevels(genDir)
	if e.hookRunlevels == nil {
		e.hookRunlevels = map[string]bool{}
	}
	for k := range hooked {
		if !before[k] {
			e.hookRunlevels[k] = true
		}
	}
	// Post-install hooks (busybox addgroup, openrc, pam...) and machine ops
	// (shadow groupadd/useradd) rewrite /etc files INSIDE the gen tree:
	// rename-over tools detach the seeded store link, leaving a standalone
	// real file whose content would drift out of the generation's store.
	// Copy such mutated files back into the generation's mutable etc_store
	// and re-seed the link, so /etc stays store-backed before activation.
	if err := e.syncMutatedEtc(filepath.Join(genDir, "etc"),
		filepath.Join(genDir, "etc")); err != nil {
		return err
	}
	if err := ensureSystemHomeDirs(genDir); err != nil {
		return fmt.Errorf("system home dirs: %w", err)
	}
	return nil
}

// checkGenRunnable asserts the generation tree has the minimum boot-critical
// entries: /sbin/init and /bin/sh. Missing either means a package post-install
// hook silently failed to create it (busybox's --install -s creates both).
// Hard-error here — before activation can ever expose the incomplete tree.
func checkGenRunnable(genDir string, n int) error {
	for _, p := range []string{"sbin/init", "bin/sh"} {
		if _, err := os.Lstat(filepath.Join(genDir, p)); err != nil {
			return fmt.Errorf("generation %d incomplete: /%s missing after hooks"+
				" (package post-install failure?)", n, p)
		}
	}
	return nil
}

// store. walkEtc is the generation's etc tree to scan; srcEtc is the
// MUTATION-SOURCE side: the LIVE root's etc (hooks used to run there) or —
// create-time contract — walkEtc itself (hooks now run chrooted into the raw
// gen tree, so detached seeded links show up as real files inside it).
func (e *executor) syncMutatedEtc(walkEtc, srcEtc string) error {
	gen := e.s.genDir(e.meta.Generation)
	genSide := walkEtc == srcEtc
	var paths []string
	filepath.WalkDir(walkEtc, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	for _, src := range paths {
		rel, _ := filepath.Rel(walkEtc, src)
		live := filepath.Join(srcEtc, rel)
		st, err := os.Lstat(live)
		if err != nil {
			continue
		}
		if genSide {
			if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
				continue // seeded link intact (writes through it land in the store)
			}
			// A real file at a seeded path = the rename-over detached the
			// store hardlink: cow-copy its content into the store copy and
			// re-hardlink. Hook-created etc files with no store candidate
			// stay unmanaged (activation materializes them).
			cand := ""
			for _, base := range []string{"etc_store", "etc_default"} {
				if c := filepath.Join(gen, base, rel); lexists(c) {
					cand = c
					break
				}
			}
			if cand == "" || sameInode(live, cand) {
				continue
			}
			if !sameContent(cand, live) {
				b, err := os.ReadFile(live)
				if err != nil {
					return fmt.Errorf("etc read %s: %w", rel, err)
				}
				if err := os.WriteFile(cand, b, st.Mode().Perm()); err != nil {
					return fmt.Errorf("etc write store %s: %w", rel, err)
				}
				fmt.Printf("etc re-managed after hooks: %s\n", rel)
			}
			os.Remove(live)
			if err := e.s.hardlink(cand, live); err != nil {
				return fmt.Errorf("etc re-hardlink %s (cand=%s): %w", rel, cand, err)
			}
			continue
		}
		tgt := e.s.storeEntry(src) // the store entry behind the gen link
		if tgt == "" {
			continue // hook-created unmanaged entry
		}
		if ss, err := os.Lstat(tgt); err != nil || ss.Mode()&os.ModeSymlink != 0 {
			continue // verbatim alias link (e.g. ssl1.1/certs): never mutates
		}
		if st.Mode()&os.ModeSymlink != 0 {
			t, _ := os.Readlink(live)
			if !strings.HasPrefix(t, "/kod/") {
				continue // local symlink: not ours to touch
			}
			// legacy symlink shape: migrate to a hardlink; shadow 4.18
			// rejects symlinks under /etc outright.
			os.Remove(live)
			if err := e.s.hardlink(tgt, live); err != nil {
				return err
			}
			fmt.Printf("etc re-managed after hooks: %s\n", rel)
			continue
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if sameInode(live, tgt) {
			continue // still hardlinked: writes landed in the store directly
		}
		if sameContent(tgt, live) {
			os.Remove(live)
			if err := e.s.hardlink(tgt, live); err != nil {
				return fmt.Errorf("etc re-hardlink %s (tgt=%s live=%s): %w", rel, tgt, live, err)
			}
			continue
		}
		b, err := os.ReadFile(live)
		if err != nil {
			return fmt.Errorf("etc read %s: %w", rel, err)
		}
		if err := os.WriteFile(tgt, b, st.Mode().Perm()); err != nil {
			return fmt.Errorf("etc write store %s: %w", rel, err)
		}
		if err := os.Remove(live); err != nil {
			return fmt.Errorf("etc remove %s: %w", rel, err)
		}
		if err := e.s.hardlink(tgt, live); err != nil {
			return fmt.Errorf("etc re-hardlink %s (tgt=%s live=%s): %w", rel, tgt, live, err)
		}
		fmt.Printf("etc re-managed after hooks: %s\n", rel)
	}
	return nil
}

func mustRel(base, target string) string {
	r, err := filepath.Rel(base, target)
	if err != nil {
		return strings.TrimPrefix(target, base+"/")
	}
	return r
}

func (e *executor) chrootExecScript(dir, relPath string) error {
	if dir == e.s.chroot {
		if err := e.ensureChrootMounts(); err != nil {
			return err
		}
	}
	cmd := exec.Command("chroot", dir, relPath)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin", "HOME=/", "TERM=dumb"}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// ---- per-distro file rendering (Alpine/OpenRC facts; the Go mirror of
// backends/alpine.lua) ----

func (e *executor) configureLocale(act map[string]any) error {
	def := ""
	if m, ok := act["default"].(map[string]any); ok {
		def = field(m, "locale")
	}
	if s, ok := act["default"].(string); ok {
		def = s
	}
	if def == "" {
		return fmt.Errorf("configure_locale: no default")
	}
	return e.writeFileTo("/etc/env.d/02locale", fmt.Sprintf("LANG=%q\n", def), 0o644)
}

func (e *executor) configureKeyboard(act map[string]any) error {
	layout := field(act, "layout")
	keymap := field(act, "console_keymap")
	if keymap == "" {
		keymap = layout
	}
	return e.writeFileTo("/etc/conf.d/keymaps",
		fmt.Sprintf("KEYMAP=%q\nSETUNICODE=%q\n", keymap, "yes"), 0o644)
}

func (e *executor) configureTimezone(act map[string]any) error {
	tz := field(act, "timezone")
	if tz == "" {
		return fmt.Errorf("configure_timezone: no timezone")
	}
	if err := e.writeFileTo("/etc/timezone", tz+"\n", 0o644); err != nil {
		return err
	}
	link := filepath.Join(e.s.chroot, "etc/localtime")
	os.Remove(link)
	return os.Symlink("/usr/share/zoneinfo/"+tz, link) // resolves once /usr is activated
}

func (e *executor) loadKernelModules(act map[string]any) error {
	mods := strList(act, "modules")
	// chrootResolve: live /etc/modules may be a chroot-rooted managed link;
	// its target only resolves rooted at the chroot (see writeFileTo).
	p := e.s.chrootResolve(filepath.Join(e.s.chroot, "etc/modules"))
	b, _ := os.ReadFile(p) // idempotent: skip modules already listed
	existing := string(b)
	var add []string
	for _, m := range mods {
		if !strings.Contains(existing, m) {
			add = append(add, m)
		}
	}
	if len(add) == 0 {
		return nil
	}
	return os.WriteFile(p, []byte(strings.TrimRight(existing, "\n")+"\n"+
		strings.Join(add, "\n")+"\n"), 0o644)
}

func (e *executor) configureService(act map[string]any) error {
	file := field(act, "settings_file")
	if file == "" {
		return fmt.Errorf("configure_service %s: no backend settings_file for this service",
			field(act, "name"))
	}
	settings, _ := act["settings"].(map[string]any)
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		var v string
		switch x := settings[k].(type) {
		case bool:
			v = map[bool]string{true: "yes", false: "no"}[x]
		case float64:
			v = strconv.FormatInt(int64(x), 10)
		case string:
			v = x
		default:
			return fmt.Errorf("configure_service %s: unsupported setting value for %q", field(act, "name"), k)
		}
		fmt.Fprintf(&b, "%s %s\n", settingsKey(k), v)
	}
	return e.writeFileTo(file, b.String(), 0o644)
}

// ---- post-activation ops (run inside the chroot) ----

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// groupNameValid: standard POSIX group name shape (also makes the grep
// pattern injection-safe)
func groupNameValid(g string) bool {
	if g == "" {
		return false
	}
	for i, r := range g {
		ok := r == '_' || r == '-' || r == '$' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func (e *executor) createUser(act map[string]any) error {
	login := field(act, "user")
	if login == "" {
		return fmt.Errorf("create_user: no user")
	}
	b, err := os.ReadFile(e.s.chrootResolve(filepath.Join(e.s.chroot, "etc/passwd")))
	// ^ chrootResolve: live etc entries are links into /kod/..., which are
	// dangling from the host side but real inside the target root.
	if err != nil {
		return fmt.Errorf("create_user: cannot read target passwd: %w", err)
	}
	exists := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, login+":") {
			exists = true
		}
	}
	if !exists {
		flags := ""
		if act["create_home"] == true {
			flags = "-m "
		}
		// supplementary groups (docker, kvm...) belong to packages the user
		// may install LATER: create any that don't exist yet. grep on
		// /etc/group, NOT getent: the busybox getent applet is not
		// guaranteed on a fresh Alpine.
		var mkgroups strings.Builder
		for _, g := range append(strList(act, "supplementary_groups"), field(act, "group")) {
			if !groupNameValid(g) {
				return fmt.Errorf("create_user %s: suspicious group name %q", login, g)
			}
			fmt.Fprintf(&mkgroups, "grep -q '^%s:' /etc/group || groupadd %s; ", g, g)
		}
		script := fmt.Sprintf(
			"%suseradd %s-d %s -s %s -g %s -G %s -c %s %s",
			mkgroups.String(),
			flags, shellQuote(field(act, "home_directory")), shellQuote(field(act, "shell")),
			shellQuote(field(act, "group")), shellQuote(strings.Join(strList(act, "supplementary_groups"), ",")),
			shellQuote(field(act, "name")), shellQuote(login))
		if err := e.chrootSh(e.s.chroot, script); err != nil {
			return fmt.Errorf("create_user %s: %w", login, err)
		}
	}
	if hash := e.secrets["users."+login+".hashed_password"]; hash != "" {
		script := fmt.Sprintf("echo %s | chpasswd -e",
			shellQuote(login+":"+hash))
		if err := e.chrootSh(e.s.chroot, script); err != nil {
			return fmt.Errorf("create_user %s: password: %w", login, err)
		}
		fmt.Printf("password set for %s (from secrets)\n", login)
	}
	return nil
}

func (e *executor) enableService(act map[string]any) error {
	unit, runlevel := field(act, "unit"), field(act, "runlevel")
	os.MkdirAll(filepath.Join(e.s.chroot, "etc/runlevels", runlevel), 0o755)
	out, err := e.chrootShOut(fmt.Sprintf("rc-update add %s %s", shellQuote(unit), shellQuote(runlevel)))
	if err != nil && !strings.Contains(out, "already been added") {
		return fmt.Errorf("enable_service %s: %w", field(act, "name"), err)
	}
	if e.declaredRunlevels == nil {
		e.declaredRunlevels = map[string]bool{}
	}
	e.declaredRunlevels[runlevel+"/"+unit] = true
	return nil
}

// snapshotRunlevels indexes <root>/etc/runlevels entries as "runlevel/unit"
// (rootDir = where the hooks/ops write: the raw gen tree at create time, the
// live chroot everywhere else).
func (e *executor) snapshotRunlevels(rootDir string) map[string]bool {
	set := map[string]bool{}
	roots, err := os.ReadDir(filepath.Join(rootDir, "etc/runlevels"))
	if err != nil {
		return set
	}
	for _, rl := range roots {
		if !rl.IsDir() {
			continue
		}
		units, err := os.ReadDir(filepath.Join(rootDir, "etc/runlevels", rl.Name()))
		if err != nil {
			continue
		}
		for _, u := range units {
			set[rl.Name()+"/"+u.Name()] = true
		}
	}
	return set
}

// reconcileRunlevels makes runlevels declarative: after the whole stream
// ran, any live runlevel symlink NOT declared by this stream's
// enable_service actions and NOT added by this run's package hooks is a
// stale leftover of an older plan (the mdev@boot that outlived its
// backend, the bluetooth a flipped config left behind) -> remove it.
// Boundary: the model owns runlevels declaratively; an admin's manual
// rc-update add is removed by the next apply (declare it in config).
func (e *executor) reconcileRunlevels() error {
	if len(e.declaredRunlevels) == 0 {
		return nil // this stream does not manage services: hands off
	}
	rlRoot := filepath.Join(e.s.chroot, "etc/runlevels")
	roots, err := os.ReadDir(rlRoot)
	if err != nil {
		return nil
	}
	for _, rl := range roots {
		if !rl.IsDir() {
			continue
		}
		units, err := os.ReadDir(filepath.Join(rlRoot, rl.Name()))
		if err != nil {
			continue
		}
		for _, u := range units {
			key := rl.Name() + "/" + u.Name()
			if e.declaredRunlevels[key] || e.hookRunlevels[key] {
				continue
			}
			p := filepath.Join(rlRoot, rl.Name(), u.Name())
			st, err := os.Lstat(p)
			if err != nil {
				continue
			}
			isSymlink := st.Mode()&os.ModeSymlink != 0
			if !isSymlink && !st.Mode().IsRegular() {
				continue // dirs/fifos: not ours to sweep
			}
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("reconcile runlevels %s: %w", key, err)
			}
			fmt.Printf("runlevel reconcile: removed stale %s\n", key)
		}
	}
	return nil
}

func (e *executor) enableHomeService(act map[string]any) error {
	// OpenRC has no user services; per IR.md the executor defers user-scope
	// services/flatpaks to first login (user-daemon runner TODO).
	fmt.Printf("note: home service %s for %s deferred to first login (mechanism %s)\n",
		field(act, "name"), field(act, "user"), field(act, "mechanism"))
	return nil
}

func (e *executor) installBootloader(act map[string]any) error {
	if e.rootPart == "" {
		return fmt.Errorf("install_bootloader: root partition unknown (no mount / action?)")
	}
	// Resolve the root UUID BEFORE touching anything: a stale config device
	// name (rootPart not blkid-resolvable) must fail here, not after the
	// ESP bootloader trees are wiped and current has flipped (half-boot
	// state). fstype (best-effort) feeds the modules= kopt below.
	uuid, err := runOut("blkid", "-o", "value", "-s", "UUID", e.rootPart)
	if err != nil {
		return fmt.Errorf("bootloader: UUID of %s: %w", e.rootPart, err)
	}
	// busybox blkid silently IGNORES -o value -s UUID: exit 0, empty output
	// on bogus devices (no error path) or the full "/dev/x: UUID=... TYPE=..."
	// line on real ones. We depend on util-linux's blkid (kodos apk
	// depends=util-linux) — never parse busybox formats; fail closed here.
	if uuid == "" {
		return fmt.Errorf("bootloader: UUID of %s: blkid produced no UUID (util-linux blkid required?)", e.rootPart)
	}
	// busybox blkid on a REAL device prints the whole
	// '/dev/x: UUID="..." TYPE="ext4"' line (-o value -s UUID ignored): a
	// bare UUID never contains space/tab/=/", so this fails closed on the
	// garbage line too — same placement, before any mutation.
	if strings.ContainsAny(uuid, " \t=\"") {
		return fmt.Errorf("bootloader: UUID of %s: not a bare UUID (util-linux blkid required?)", e.rootPart)
	}
	fstype, _ := runOut("blkid", "-o", "value", "-s", "TYPE", e.rootPart)
	gen := e.s.genDir(e.meta.Generation)

	// Alpine makes initramfs via apk TRIGGERS (which we bypass): for each
	// /lib/modules/<abi>, depmod + mkinitfs -o /boot/initramfs<suffix>
	// (suffix from kernel-suffix, matching the vmlinuz-<flavor> name).
	// mkinitfs runs chrooted into the generation tree, so its output
	// lands in <gen>/boot; the ESP copy below runs AFTER the build.
	modsGen := filepath.Join(gen, "lib", "modules")
	if dirs, _ := filepath.Glob(filepath.Join(modsGen, "*")); len(dirs) > 0 {
		for _, md := range dirs {
			abi := filepath.Base(md)
			// chrootResolve: gen-tree files are chroot-rooted links that
			// only resolve rooted at the chroot (raw host reads dangle).
			sufB, err := os.ReadFile(e.s.chrootResolve(filepath.Join(md, "kernel-suffix")))
			if err != nil {
				continue // not a kernel modules dir
			}
			// kernel-suffix content already carries the dash, e.g. "-virt"
			suffix := strings.TrimSpace(string(sufB))
			if !strings.HasPrefix(suffix, "-") {
				suffix = "-" + suffix
			}
			// NO skip-if-exists guard: a file left damaged by an earlier
			// fs/IO incident (truncated by fsck) then silently boots a
			// skeleton initramfs into "No working init" panics. Rebuilding
			// costs seconds; activation is rare.
			if err := e.chrootSh(e.s.chroot, "depmod "+shellQuote(abi)); err != nil {
				return fmt.Errorf("bootloader depmod %s: %w", abi, err)
			}
			if err := e.genChrootSh("mkinitfs -o /boot/initramfs" + suffix + " " + shellQuote(abi)); err != nil {
				return fmt.Errorf("bootloader mkinitfs %s: %w", abi, err)
			}
			// mkinitfs chrooted into the gen tree copies our etc symlinks
			// (-> /kod/generation/N/etc_store/...) plus their store
			// targets; dereference the links into real files, drop the
			// store copies, and require /init (hollow-image guard).
			removed, derefed, err := cleanInitramfs(filepath.Join(gen, "boot", "initramfs"+suffix))
			if err != nil {
				return fmt.Errorf("bootloader initramfs %s: %w", suffix, err)
			}
			fmt.Printf("initramfs built: initramfs%s (kernel %s, %d store entries stripped, %d etc links dereferenced)\n",
				suffix, abi, removed, derefed)
		}
	}

	// Copy the generation's whole /boot content (kernel package files +
	// the just-built initramfs) onto the ESP mount: /boot in the live
	// root is a vfat ESP mountpoint, so generation hardlinks never
	// merge into it; copy dereferenced content instead.
	if bootGen := filepath.Join(gen, "boot"); isRealDir(bootGen) {
		if err := filepath.WalkDir(bootGen, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(bootGen, p)
			src := e.s.chrootResolve(p)
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			// chrootResolve the DESTINATION too: when /boot is not a real
			// mountpoint (scratch roots) it is a chroot-rooted link whose
			// raw host path dangles.
			bootDst := e.s.chrootResolve(filepath.Join(e.s.chroot, "boot"))
			if err := os.MkdirAll(bootDst, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(bootDst, rel), b, 0o644)
		}); err != nil {
			return fmt.Errorf("bootloader boot copy: %w", err)
		}
	}

	// clear stale bootloader trees first: grub-install otherwise tries to
	// backup-rename existing module files, which EIOs on an ESP with any
	// directory damage, and a clean slate makes the rerun cheap anyway
	for _, old := range []string{"boot/grub", "boot/EFI"} {
		if err := os.RemoveAll(filepath.Join(e.s.chroot, old)); err != nil {
			return fmt.Errorf("bootloader: clearing old %s: %w (fs damaged: run vm/run.sh fresh)", old, err)
		}
	}
	if err := e.chrootSh(e.s.chroot, "grub-install --removable --target=x86_64-efi "+
		"--efi-directory=/boot --boot-directory=/boot"); err != nil {
		return err
	}
	// init's "Loading boot drivers" step modprobes modules=<kopt> BEFORE
	// mounting root; nothing in the Alpine initramfs chain guarantees the
	// root filesystem driver (e.g. ext4 is =m on linux-virt) otherwise, and
	// the mount then fails "No such file or directory".
	args := append([]string{}, e.kernelArgs...)
	hasModules := false
	for _, a := range args {
		if strings.HasPrefix(a, "modules=") {
			hasModules = true
		}
	}
	if !hasModules && fstype != "" {
		args = append(args, "modules="+fstype)
	}
	// kver + guard against the GENERATION's boot tree (what was just
	// built), not <chroot>/boot: that is the ESP mount and would let a
	// stale image from an earlier run pass the check silently.
	matches, _ := filepath.Glob(filepath.Join(gen, "boot", "vmlinuz-*"))
	if len(matches) == 0 {
		return fmt.Errorf("bootloader: no %s/boot/vmlinuz-* (kernel package missing?)", gen)
	}
	kver := strings.TrimPrefix(filepath.Base(matches[0]), "vmlinuz-")
	if _, err := os.Stat(filepath.Join(gen, "boot", "initramfs-"+kver)); err != nil {
		return fmt.Errorf("bootloader: no initramfs for kernel %s (mkinitfs failed?)", kver)
	}
	// mkinitfs degrades SILENTLY: its file-collection stage runs through
	// shell pipelines that swallow failures (missing lddtree, empty
	// features files under /etc/mkinitfs), leaving a skeleton image with
	// no busybox//bin/sh: the kernel unpacks an almost-empty rootfs and
	// panics "No working init found" with zero console output. We saw a
	// 3.5MB hollow image pass a size guard, so check CONTENTS: a real
	// image always carries busybox in the cpio stream.
	if err := initramfsSane(filepath.Join(gen, "boot", "initramfs-"+kver)); err != nil {
		return fmt.Errorf("bootloader: initramfs-%s is skeleton (%v): "+
			"mkinitfs degraded: check etc/mkinitfs/mkinitfs.conf features line and features.d/*.files",
			kver, err)
	}
	timeout := 5
	if f, ok := act["timeout"].(float64); ok {
		timeout = int(f)
	}
	// Every surviving generation gets a menu entry: boot that generation's
	// own kernel+initramfs from a per-generation ESP dir — the recovery
	// path when a kernel/mkinitfs step goes sideways in the newest
	// generation. Honest semantics: the mounted root and the activated
	// userspace are still whatever `current` points at; generation
	// SWITCHING remains kodos rollback.
	bootDst := e.s.chrootResolve(filepath.Join(e.s.chroot, "boot"))
	genIdx := e.meta.Generation
	entries := []grubEntry{{genIdx, kver, ""}}
	genDirs, _ := filepath.Glob(filepath.Join(e.s.root(), "generation", "[0-9]*"))
	slices.SortFunc(genDirs, func(a, b string) int {
		x, _ := strconv.Atoi(filepath.Base(a))
		y, _ := strconv.Atoi(filepath.Base(b))
		return y - x
	})
	for _, gd := range genDirs {
		g, err := strconv.Atoi(filepath.Base(gd))
		if err != nil || g == genIdx {
			continue
		}
		gm, _ := filepath.Glob(filepath.Join(gd, "boot", "vmlinuz-*"))
		if len(gm) == 0 {
			continue // no kernel tree in that generation: nothing to boot
		}
		gkv := strings.TrimPrefix(filepath.Base(gm[0]), "vmlinuz-")
		dir := filepath.Join(bootDst, fmt.Sprintf("gen-%d", g))
		if err := filepath.WalkDir(filepath.Join(gd, "boot"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Join(gd, "boot"), p)
			b, err := os.ReadFile(e.s.chrootResolve(p))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
		}); err != nil {
			return fmt.Errorf("bootloader: generation %d boot copy: %w", g, err)
		}
		entries = append(entries, grubEntry{g, gkv, fmt.Sprintf("/gen-%d", g)})
	}
	// prune ESP gen-N trees whose generation was pruned away
	alive := map[string]bool{}
	for _, en := range entries {
		if en.dir != "" {
			alive[en.dir[1:]] = true
		}
	}
	if stale, _ := filepath.Glob(filepath.Join(bootDst, "gen-*")); len(stale) > 0 {
		for _, s := range stale {
			if !alive[filepath.Base(s)] {
				if err := os.RemoveAll(s); err != nil {
					return fmt.Errorf("bootloader: pruning %s: %w", s, err)
				}
			}
		}
	}
	cfg := renderGrubCfg(timeout, uuid, args, entries)
	// ESP is mounted at <chroot>/boot: grub reads its config there
	if err := os.WriteFile(filepath.Join(e.s.chroot, "boot/grub/grub.cfg"),
		[]byte(cfg), 0o444); err != nil {
		return err
	}
	fmt.Printf("bootloader: grub EFI (removable path) + grub.cfg for generation %d, kernel %s (%d menu entries)\n",
		genIdx, kver, len(entries))
	return nil
}

// initramfsSane gunzips (stdlib; no host tools) and greps the cpio stream
// for busybox: mkinitfs' collection stage swallows its own failures and
// otherwise hands back a hollow image silently.
func initramfsSane(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not gzip: %w", err)
	}
	defer zr.Close()
	blob, err := io.ReadAll(zr)
	if err != nil {
		return err
	}
	if !bytes.Contains(blob, []byte("busybox")) {
		return fmt.Errorf("cpio stream has no busybox (%d bytes unpacked)", len(blob))
	}
	return nil
}

// cleanInitramfs rewrites a gzipped newc cpio in place: drops every
// top-level "kod" entry and dereferences the etc symlinks that pointed
// into it. mkinitfs chrooted into the gen tree copies our etc symlinks
// verbatim (e.g. etc/modprobe.d/aliases.conf -> /kod/generation/N/
// etc_store/modprobe.d/...) AND their store targets, so a naive drop
// dangles them (libkmod "Cannot stat directory entry" in the guest).
// While parsing it also requires an "init" entry: the hollow-image
// incident (NEXT.md) shipped a 79-entry cpio that booted to a panic;
// a corrupt/empty cpio fails HERE, before the ESP copy, not at boot.
func cleanInitramfs(path string) (removed, derefed int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return 0, 0, fmt.Errorf("%s is not gzip: %w", path, err)
	}
	data, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("gunzip %s: %w", path, err)
	}
	out, hasInit, removed, derefed, err := stripCpio(data, "kod")
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	if !hasInit {
		return removed, derefed, fmt.Errorf("%s: cpio has no /init entry (hollow image)", path)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(out); err != nil {
		return removed, derefed, err
	}
	if err := zw.Close(); err != nil {
		return removed, derefed, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return removed, derefed, err
	}
	return removed, derefed, os.WriteFile(path, buf.Bytes(), fi.Mode().Perm())
}

// stripCpio rewrites a newc cpio stream: store-path records (drop and
// everything under it) are dropped; symlinks pointing at a dropped file
// are rebuilt as regular files carrying the dropped record's CONTENT, so
// nothing dangles. Unrelated records are copied byte-for-byte (headers
// carry inode/mode/times — only the two fields we must change on a
// rebuilt record get rewritten).
func stripCpio(data []byte, drop string) (out []byte, hasInit bool, removed, derefed int, err error) {
	type rec struct {
		start, end, ds, nsize int
		name, target          string
		mode                  uint32
		fsize                 int
	}
	var recs []rec
	for off := 0; off+110 <= len(data) && string(data[off:off+6]) == "070701"; {
		hdr := data[off : off+110]
		mode, perr := strconv.ParseUint(string(hdr[14:22]), 16, 32)
		if perr != nil {
			return nil, false, 0, 0, fmt.Errorf("bad mode field at %d", off)
		}
		fsize, perr := strconv.ParseUint(string(hdr[54:62]), 16, 32)
		if perr != nil {
			return nil, false, 0, 0, fmt.Errorf("bad filesize field at %d", off)
		}
		nsize, perr := strconv.ParseUint(string(hdr[94:102]), 16, 32)
		if perr != nil || nsize == 0 {
			return nil, false, 0, 0, fmt.Errorf("bad namesize field at %d", off)
		}
		named := off + 110 + int(nsize)
		if named > len(data) || data[named-1] != 0 {
			return nil, false, 0, 0, fmt.Errorf("truncated name at %d", off)
		}
		// cpio writers (mkinitfs included) store "./"-prefixed names;
		// normalize for matching only — copied bytes stay verbatim.
		name := strings.TrimPrefix(string(data[off+110:named-1]), "./")
		if name == "TRAILER!!!" {
			recs = append(recs, rec{start: off, end: len(data), name: name})
			break
		}
		// Writer padding quirks: GNU newc aligns to 2 bytes, busybox
		// (mkinitfs' cpio) to 4; whichever candidate end actually
		// carries the next magic decides both ends and data start.
		// Pads are NUL, so a false hit needs binary data starting
		// "0707.." and would fail loudly, never silently.
		var r rec
		r = rec{start: off, nsize: int(nsize), name: name, mode: uint32(mode), fsize: int(fsize)}
		c2d, c4d := (named+1)&^1, (named+3)&^3
		c2e, c4e := (c2d+int(fsize)+1)&^1, (c4d+int(fsize)+3)&^3
		switch {
		case c2e+6 <= len(data) && string(data[c2e:c2e+6]) == "070701":
			r.end, r.ds = c2e, c2d
		case c4e+6 <= len(data) && string(data[c4e:c4e+6]) == "070701":
			r.end, r.ds = c4e, c4d
		default:
			return nil, false, 0, 0, fmt.Errorf("no header after %q at %d", name, named)
		}
		if r.mode&0o170000 == 0o120000 {
			r.target = strings.TrimPrefix(strings.TrimPrefix(string(data[r.ds:r.ds+r.fsize]), "./"), "/")
		}
		recs = append(recs, r)
		off = r.end
	}
	if len(recs) == 0 || recs[len(recs)-1].name != "TRAILER!!!" {
		return nil, false, 0, 0, fmt.Errorf("cpio stream has no TRAILER record")
	}
	// content of the store records we are about to drop: they are the
	// targets of the etc symlinks copied in front of them
	inDrop := func(name string) bool {
		return name == drop || strings.HasPrefix(name, drop+"/")
	}
	content := map[string][]byte{}
	for _, r := range recs {
		if inDrop(r.name) && r.mode&0o170000 == 0o100000 {
			content[r.name] = data[r.ds : r.ds+r.fsize]
		}
	}
	var buf bytes.Buffer
	for _, r := range recs {
		switch {
		case r.name == "TRAILER!!!":
			buf.Write(data[r.start:]) // trailer + tail padding verbatim
		case inDrop(r.name):
			removed++
		case r.mode&0o170000 == 0o120000 && inDrop(r.target):
			c, ok := content[r.target]
			if !ok {
				return nil, false, removed, derefed,
					fmt.Errorf("%s: symlink target %s missing from cpio", r.name, r.target)
			}
			hdr := append([]byte(nil), data[r.start:r.start+110]...)
			copy(hdr[14:22], fmt.Sprintf("%08X", r.mode&0o7777|0o100000))
			copy(hdr[54:62], fmt.Sprintf("%08X", len(c)))
			hdr = append(hdr, data[r.start+110:r.start+110+r.nsize]...) // name bytes
			for len(hdr)&3 != 0 {
				hdr = append(hdr, 0)
			}
			hdr = append(hdr, c...)
			for len(hdr)&3 != 0 {
				hdr = append(hdr, 0)
			}
			buf.Write(hdr)
			derefed++
		default:
			if r.name == "init" {
				hasInit = true
			}
			buf.Write(data[r.start:r.end])
		}
	}
	out = buf.Bytes()
	if pad := len(out) % 512; pad != 0 { // cpio pads the archive to a block
		out = append(out, make([]byte, 512-pad)...)
	}
	return out, hasInit, removed, derefed, nil
}
