package main

// ops.go — the ops the spike previously refused: storage (partition/format/
// swap/mount), chroot infrastructure (pseudo-fs mounts, chroot sh,
// .post-install scripts), per-distro file rendering (locale/keyboard/
// timezone/modules/service settings), users, services, bootloader.
// All of these need root on a real target; the pure logic (partition
// planning, grub.cfg rendering, settings keys) is unit-checked in selftest.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// mountedAt / diskBusy read /proc/mounts: the executor must never carve up
// a disk the HOST is using (the config carries the real machine's device
// names; a wrong layer or typo must not nuke the running system).
func mountedTargetAt(target string) (string, bool) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && unescapeMount(f[1]) == target {
			return unescapeMount(f[0]), true
		}
	}
	return "", false
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func diskBusy(device string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		src := unescapeMount(f[0])
		// the disk itself or any partition of it (nvme0n1p2, nbd0p1, sda1)
		if src == device || (strings.HasPrefix(src, device) &&
			(len(src) > len(device) && (src[len(device)] >= '0' && src[len(device)] <= '9' ||
				src[len(device)] == 'p'))) {
			return true
		}
	}
	return false
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

// renderGrubCfg is pure (selftested): ESP-relative kernel paths (vfat,
// grub reads it natively), root by UUID, serialized terminals so the
// qemu -nographic console shows the boot.
func renderGrubCfg(timeout int, uuid, kver string, args []string, gen int) string {
	cmdline := fmt.Sprintf("root=UUID=%s ro", uuid)
	if len(args) > 0 {
		cmdline += " " + strings.Join(args, " ")
	}
	return fmt.Sprintf(`set default=0
set timeout=%d
serial --unit=0 --speed=115200
terminal_input serial
terminal_output serial console
menuentry 'Kodos generation %d' {
	search.fs_uuid %s root
	set root=($root)
	linux /vmlinuz-%s %s
	initrd /initramfs-%s
}
`, timeout, gen, uuid, kver, cmdline, kver)
}

// settingsKey: neutral snake_case -> sshd-style CamelCase keys (pure,
// selftested). Honest limit: per-service key dialects beyond CamelCase
// need backend settings knowledge (same known limit as the schema).
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

// ---- op handlers ----

func (e *executor) partitionDisk(act map[string]any) error {
	device := field(act, "device")
	if !strings.HasPrefix(device, "/dev/") {
		return fmt.Errorf("partition_disk: %q is not a device path", device)
	}
	if diskBusy(device) {
		return fmt.Errorf("partition_disk: %s (or a partition of it) is MOUNTED on this host: refusing", device)
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
	if out, _ := runOut("blkid", "-o", "value", "-s", "TYPE", part); out != "" {
		return nil // already formatted: never re-mkfs a live partition
	}
	switch fs := field(act, "filesystem"); fs {
	case "ext4":
		return run("mkfs.ext4", "-q", part)
	case "vfat":
		return run("mkfs.vfat", "-F", "32", part)
	default:
		return fmt.Errorf("format: unsupported filesystem %q", fs)
	}
}

func (e *executor) createSwap(act map[string]any) error {
	part := field(act, "partition")
	if out, _ := runOut("blkid", "-o", "value", "-s", "TYPE", part); out == "swap" {
		return nil
	}
	return run("mkswap", part)
}

func (e *executor) mount(act map[string]any) error {
	part, point := field(act, "partition"), field(act, "point")
	target := filepath.Join(e.s.chroot, point)
	if src, ok := mountedTargetAt(target); ok {
		fmt.Printf("%s already mounted at %s (%s)\n", src, target, src)
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := run("mount", "-o", field(act, "options"), part, target); err != nil {
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
	for _, name := range []string{"run", "dev", "sys", "proc"} {
		exec.Command("umount", "-R", filepath.Join(e.s.chroot, name)).Run() // best-effort
	}
}

func (e *executor) chrootSh(script string) error {
	if err := e.ensureChrootMounts(); err != nil {
		return err
	}
	cmd := exec.Command("chroot", e.s.chroot, "/bin/sh", "-l", "-c", script)
	cmd.Dir = e.s.chroot // the inode becomes "/" after chroot(2)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// runPostInstalls executes package .post-install scripts INSIDE the
// activated generation (Alpine's kernel hook = mkinitfs, dbus adds users,
// ...). Deterministic: store order by package name.
func (e *executor) runPostInstalls(toInstall map[string]string) error {
	names := make([]string, 0, len(toInstall))
	for p := range toInstall {
		names = append(names, p)
	}
	slices.Sort(names)
	for _, p := range names {
		script := filepath.Join(e.s.store(), p, toInstall[p], ".post-install")
		if !lexists(script) {
			continue
		}
		rel, _ := filepath.Rel(e.s.chroot, script)
		fmt.Printf("post-install: %s\n", p)
		if err := e.chrootSh("/" + rel); err != nil {
			return fmt.Errorf("post-install %s: %w", p, err)
		}
	}
	return nil
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
	p := filepath.Join(e.s.chroot, "etc/modules")
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
		return fmt.Errorf("configure_service: no settings_file")
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
	b, err := os.ReadFile(filepath.Join(e.s.chroot, "etc/passwd"))
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
		if err := e.chrootSh(script); err != nil {
			return fmt.Errorf("create_user %s: %w", login, err)
		}
	}
	if hash := e.secrets["users."+login+".hashed_password"]; hash != "" {
		script := fmt.Sprintf("echo %s | chpasswd -e",
			shellQuote(login+":"+hash))
		if err := e.chrootSh(script); err != nil {
			return fmt.Errorf("create_user %s: password: %w", login, err)
		}
		fmt.Printf("password set for %s (from secrets)\n", login)
	}
	return nil
}

func (e *executor) enableService(act map[string]any) error {
	unit, runlevel := field(act, "unit"), field(act, "runlevel")
	os.MkdirAll(filepath.Join(e.s.chroot, "etc/runlevels", runlevel), 0o755)
	if err := e.chrootSh(fmt.Sprintf("rc-update add %s %s", shellQuote(unit), shellQuote(runlevel))); err != nil {
		return fmt.Errorf("enable_service %s: %w", field(act, "name"), err)
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
	if err := e.chrootSh("grub-install --removable --target=x86_64-efi " +
		"--efi-directory=/boot --boot-directory=/boot"); err != nil {
		return err
	}
	uuid, err := runOut("blkid", "-o", "value", "-s", "UUID", e.rootPart)
	if err != nil {
		return fmt.Errorf("bootloader: UUID of %s: %w", e.rootPart, err)
	}
	matches, _ := filepath.Glob(filepath.Join(e.s.chroot, "boot", "vmlinuz-*"))
	if len(matches) == 0 {
		return fmt.Errorf("bootloader: no /boot/vmlinuz-* (kernel post-install did not run?)")
	}
	kver := strings.TrimPrefix(filepath.Base(matches[0]), "vmlinuz-")
	if _, err := os.Stat(filepath.Join(e.s.chroot, "boot", "initramfs-"+kver)); err != nil {
		return fmt.Errorf("bootloader: no initramfs for kernel %s (mkinitfs failed?)", kver)
	}
	timeout := 5
	if f, ok := act["timeout"].(float64); ok {
		timeout = int(f)
	}
	gen := e.meta.Generation
	cfg := renderGrubCfg(timeout, uuid, kver, e.kernelArgs, gen)
	// ESP is mounted at <chroot>/boot: grub reads its config there
	if err := os.WriteFile(filepath.Join(e.s.chroot, "boot/grub/grub.cfg"),
		[]byte(cfg), 0o444); err != nil {
		return err
	}
	fmt.Printf("bootloader: grub EFI (removable path) + grub.cfg for generation %d, kernel %s\n", gen, kver)
	return nil
}
