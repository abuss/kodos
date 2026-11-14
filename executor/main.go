// kodos — executor spike consuming the IR contract (example/eszkoz/IR.md):
//
//	kodos exec -actions actions.json -metadata generation-N.json -root ROOTFS
//	           [-secrets FILE] [-arch x86_64] [-dry-run]
//	kodos selftest
//
// Spike scope (alpine/apk family): implements the whole offline store/
// generation core — configure_repos, write_file, install_packages (download
// + unpack pinned .apks into the store; .apk is tar.gz), configure_network
// (NM keyfile, secrets resolved from a 0600 file), create_generation,
// activate_generation. Ops needing root/real devices or distro-specific
// settings rendering (partitioning, users, services, bootloader) report
// honestly as not implemented yet; --dry-run plans the full stream.
// Rollback = re-run with an older generation-N.json (activate_generation).
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

type executor struct {
	s        *system
	meta     *metadata
	secrets  map[string]string
	arch     string
	mirror   string // captured from configure_repos (#1 always precedes #7)
	branches []string
	localRepos []struct{ name, url string } // file:// repos from configure_repos

	rootPart      string            // captured from mount / (bootloader needs UUID)
	chrootMounted bool              // pseudo-fs mounted once
	kernelArgs    []string          // stashed by set_kernel_arguments for grub.cfg
	lastGen       map[string]string // pkg->version of the last create_generation
	apk           string            // apk binary for dependency closure resolution
	resolved      map[string]string // pkg->version full closure from apk simulate

	declaredRunlevels map[string]bool // "runlevel/unit" this stream's enable_service set
	hookRunlevels     map[string]bool // "runlevel/unit" package hooks enabled this run
	
	metadataFileBytes []byte // raw -metadata file for config_hash if provided
	actionsFileBytes  []byte // raw actions file for config_hash fallback
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kodos [exec|selftest] ...")
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "exec":
		if err := cmdExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			if os.Getenv("KODOS_TRACEBACK") != "" {
				debug.PrintStack()
			}
			os.Exit(1)
		}
	case "selftest":
		cmdSelftest()
	case "suitetest":
		if err := cmdSuitetest(); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}
	case "lua":
		if err := cmdLua(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: kodos [exec|selftest] ...")
		os.Exit(2)
	}
}

func cmdExec(args []string) error {
	actions := flag.NewFlagSet("exec", flag.ContinueOnError)
	metadata := actions.String("metadata", "", "generation-N.json (version lock)")
	root := actions.String("root", "", "target root filesystem directory")
	secrets := actions.String("secrets", "", "secrets store JSON (dotted key -> value, mode 0600)")
	arch := actions.String("arch", "x86_64", "package arch for download URLs")
	apkBin := actions.String("apk", "apk", "apk-tools binary used to resolve the dependency closure")
	dryRun := actions.Bool("dry-run", false, "validate + print the plan, change nothing")
	actionsFile := actions.String("actions", "", "actions.json (IR stream)")

	if err := actions.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *actionsFile == "" || *metadata == "" || *root == "" {
		return fmt.Errorf("usage: kodos exec -actions A -metadata M -root R [-secrets F] [-arch A] [-dry-run]")
	}

	af, err := loadActions(*actionsFile)
	if err != nil {
		return err
	}
	// Read actions file bytes for config_hash fallback
	afBytes, err := os.ReadFile(*actionsFile)
	if err != nil {
		return err
	}
	meta, err := loadMetadata(*metadata)
	if err != nil {
		return err
	}
	// Read metadata file bytes for config_hash (primary source per brief)
	metaBytes, err := os.ReadFile(*metadata)
	if err != nil {
		return err
	}
	if af.Distro != "alpine" {
		return fmt.Errorf("spike implements the alpine/apk family only (got distro %q)", af.Distro)
	}
	if meta.Distro != af.Distro || meta.Generation < 0 {
		return fmt.Errorf("actions and metadata disagree (distro/generation)")
	}

	e := &executor{s: &system{chroot: *root}, meta: meta, arch: *arch, apk: *apkBin, metadataFileBytes: metaBytes, actionsFileBytes: afBytes}
	if *secrets != "" {
		if err := loadJSON(*secrets, &e.secrets); err != nil {
			return err
		}
	}
	if e.secrets == nil {
		e.secrets = map[string]string{}
	}

	for _, act := range af.Actions {
		op, _ := act["op"].(string)
		if *dryRun {
			fmt.Printf("DRY-RUN %s %s\n", op, summarize(act))
			continue
		}
		fmt.Printf("== %s\n", op)
		if err := e.run(act); err != nil {
			e.umountChroot() // leave no half-mounted pseudo-fs behind
			return err
		}
	}
	if err := e.reconcileRunlevels(); err != nil {
		e.umountChroot()
		return err
	}
	e.umountChroot()
	return nil
}

func cmdSelftest() {
	selftest()
}

func cmdSuitetest() error {
	os.Setenv("KODOS_INPROCESS", "1")
	defer os.Unsetenv("KODOS_INPROCESS")
	suites := []string{
		"test-merge.lua",
		"test-validate.lua",
		"test-equivalence.lua",
		"test-generate.lua",
		"test-lock.lua",
	}
	for _, s := range suites {
		out, err := runLuaScript(s, nil, "")
		if err != nil {
			return fmt.Errorf("suite %s FAIL: %w", s, err)
		}
		os.Stdout.WriteString(out)
	}
	return nil
}

func (e *executor) run(act map[string]any) error {
	op, _ := act["op"].(string)
	switch op {
	case "configure_repos":
		return e.configureRepos(act)
	case "partition_disk":
		return e.partitionDisk(act)
	case "format":
		return e.formatPartition(act)
	case "create_swap":
		return e.createSwap(act)
	case "mount":
		return e.mount(act)
	case "write_file":
		return e.writeFile(act)
	case "install_packages":
		return e.installPackages(act)
	case "configure_locale":
		return e.configureLocale(act)
	case "configure_keyboard":
		return e.configureKeyboard(act)
	case "configure_timezone":
		return e.configureTimezone(act)
	case "load_kernel_modules":
		return e.loadKernelModules(act)
	case "set_kernel_arguments":
		e.kernelArgs = strList(act, "arguments")
		return nil
	case "configure_service":
		return e.configureService(act)
	case "configure_network":
		return e.configureNetwork(act)
	case "create_generation":
		return e.createGeneration(act)
	case "activate_generation":
		if err := e.s.activateGeneration(e.s.genDir(e.meta.Generation)); err != nil {
			return err
		}
		// post-install hooks belong to the activated tree (kernel hook =
		// mkinitfs); running them is part of making the generation real
		return e.runPostInstalls(e.lastGen)
	case "create_user":
		return e.createUser(act)
	case "enable_service":
		return e.enableService(act)
	case "enable_home_service":
		return e.enableHomeService(act)
	case "install_bootloader":
		return e.installBootloader(act)
	case "configure_flatpak_remote", "install_flatpak":
		return fmt.Errorf("op %q: not implemented yet (flatpak lands after the VM boots)", op)
	default:
		return fmt.Errorf("unknown op %q", op)
	}
}

func summarize(act map[string]any) string {
	switch act["op"] {
	case "install_packages":
		return fmt.Sprintf("(%d packages)", len(strList(act, "names")))
	case "configure_repos":
		return act["mirror"].(string)
	case "write_file":
		return act["path"].(string)
	case "create_generation":
		return fmt.Sprintf("(%d packages)", len(strList(act, "packages")))
	}
	return ""
}

// ---- implemented ops ----

func (e *executor) configureRepos(act map[string]any) error {
	e.mirror, _ = act["mirror"].(string)
	e.branches = strList(act, "branches")
	var b strings.Builder
	for _, br := range e.branches {
		b.WriteString(e.mirror + "/" + br + "\n")
	}
	if err := e.writeFileTo("/etc/apk/repositories", b.String(), 0o644); err != nil {
		return err
	}
	// write per-name .list files for local file:// repos
	e.localRepos = nil
	repos, _ := act["repos"].([]any)
	for _, r := range repos {
		m, _ := r.(map[string]any)
		name, url := field(m, "name"), field(m, "url")
		if name == "" || url == "" {
			continue
		}
		e.localRepos = append(e.localRepos, struct{ name, url string }{name, url})
		if err := e.writeFileTo("/etc/apk/repositories.d/"+name+".list", url+"\n", 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (e *executor) writeFile(act map[string]any) error {
	path, _ := act["path"].(string)
	content, _ := act["content"].(string)
	content, err := expandUUIDTokens(content, func(dev string) (string, error) {
		return runOut("blkid", "-o", "value", "-s", "UUID", dev)
	})
	if err != nil {
		return fmt.Errorf("write_file %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if path == "/etc/sudoers" || strings.HasPrefix(path, "/etc/sudoers.d/") {
		mode = 0o440 // sudo silently ignores sudoers files it deems too open
	}
	return e.writeFileTo(path, content, mode)
}

func (e *executor) writeFileTo(path, content string, mode os.FileMode) error {
	// resolve chroot-rooted managed links first: the live etc entry may be a
	// symlink into /kod, and those targets are only meaningful rooted at the
	// chroot — the host runs an arbitrary distro with its own root layout.
	p := e.s.chrootResolve(filepath.Join(e.s.chroot, path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), mode)
}

// installPackages resolves the FULL dependency closure with apk's own
// solver (add --simulate; the config's names are requests, not a closure:
// busybox/musl/baselayout arrive only via depends), then fetches each .apk
// into the store. lockfile.lua now locks the whole closure, so this live
// resolve ENFORCES the lock: pinned version resolving elsewhere = hard lock
// drift error; a dep the live repos added that the lock lacks is loud
// (repo moved after locking — re-snapshot and relock).
func (e *executor) installPackages(act map[string]any) error {
	if e.mirror == "" {
		return fmt.Errorf("install_packages before configure_repos: no repos captured")
	}
	names := strList(act, "names")
	addArgs := []string{}
	for _, n := range names {
		if pin, ok := e.meta.Packages[n]; ok {
			addArgs = append(addArgs, n+"="+pin.Version)
		} else {
			addArgs = append(addArgs, n)
		}
	}
	resolved, err := e.apkSimulate(addArgs)
	if err != nil {
		return err
	}
	var unpinned []string
	for name, ver := range resolved {
		if pin, ok := e.meta.Packages[name]; ok {
			if pin.Version != ver {
				return fmt.Errorf("lock drift: %s resolves to %s but metadata pins %s",
					name, ver, pin.Version)
			}
		} else {
			unpinned = append(unpinned, name+"-"+ver)
		}
		if dst := filepath.Join(e.s.store(), name, ver); !isRealDir(dst) {
			if err := e.fetchUnpack(name, ver); err != nil {
				return err
			}
		}
	}
	slices.Sort(unpinned)
	if len(unpinned) > 0 {
		fmt.Printf("WARNING: %d deps NOT in the locked closure (repo drifted since the lock — re-snapshot indexes and relock): %s\n",
			len(unpinned), strings.Join(unpinned, " "))
	}
	e.resolved = resolved
	fmt.Printf("install_packages: %d requested -> %d resolved (full closure)\n", len(names), len(resolved))
	return nil
}

// apkSimulate: scratch apk db under <chroot>/.kodos-apkdb keeps the solver
// out of the generation/activation model entirely.
func (e *executor) apkSimulate(addArgs []string) (map[string]string, error) {
	db := filepath.Join(e.s.chroot, ".kodos-apkdb")
	os.MkdirAll(filepath.Join(db, "lib/apk/db"), 0o755)
	os.MkdirAll(filepath.Join(db, "etc/apk"), 0o755)
	if !lexists(filepath.Join(db, "lib/apk/db/installed")) {
		os.WriteFile(filepath.Join(db, "lib/apk/db/installed"), nil, 0o644)
	}
	if !lexists(filepath.Join(db, "etc/apk/world")) {
		os.WriteFile(filepath.Join(db, "etc/apk/world"), nil, 0o644)
	}
	args := []string{"--root", db, "--no-cache", "--allow-untrusted"}
	for _, b := range e.branches {
		args = append(args, "-X", e.mirror+"/"+b)
	}
	for _, lr := range e.localRepos {
		args = append(args, "-X", lr.url)
	}
	args = append(args, "add", "--simulate")
	args = append(args, addArgs...)
	out, err := runOut(e.apk, args...)
	if err != nil {
		return nil, fmt.Errorf("apk dependency resolve: %w", err)
	}
	resolved := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, ") Installing "); i >= 0 {
			rest := line[i+len(") Installing "):]
			name, ver, ok := strings.Cut(rest, " (")
			if !ok {
				continue
			}
			resolved[name] = strings.TrimSuffix(ver, ")")
		}
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("apk resolved nothing: %s", out)
	}
	return resolved, nil
}

func (e *executor) fetchUnpack(name, version string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	var body io.ReadCloser
	var lastErr error
	// try local file:// repos first
	for _, lr := range e.localRepos {
		apkPath := strings.TrimPrefix(lr.url, "file://") +
			"/" + e.arch + "/" + name + "-" + version + ".apk"
		f, err := os.Open(apkPath)
		if err == nil {
			body = f
			break
		}
		lastErr = err
	}
	if body == nil {
		for _, br := range e.branches {
			url := fmt.Sprintf("%s/%s/%s/%s-%s.apk", e.mirror, br, e.arch, name, version)
			resp, err := client.Get(url)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode == 200 {
				body = resp.Body
				break
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
	}
	if body == nil {
		return fmt.Errorf("fetch %s-%s: %w", name, version, lastErr)
	}
	defer body.Close()

	dst := filepath.Join(e.s.store(), name, version)
	os.RemoveAll(dst) // re-runs must not inherit half-extracted state
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return unpackApkStream(body, dst, name)
}

// unpackApkStream unpacks a .apk (tar.gz) stream into dst (pure,
// selftested). Modes come from hdr.FileInfo().Mode() IN FULL: .Perm()
// silently drops the setuid/setgid bits, and a sudo/su/passwd that lost
// them breaks guest privilege handling in ways only noticed at boot.
func unpackApkStream(body io.Reader, dst, name string) error {
	gz, err := gzip.NewReader(body) // .apk is a tar.gz
	if err != nil {
		return fmt.Errorf("unpack %s: %w", name, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dst, hdr.Name)
		if !strings.HasPrefix(p, dst+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in %s: %s", name, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p) // existing file would keep its OLD mode through O_TRUNC
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			// chmod AFTER the copy: the kernel strips setuid/setgid on
			// writes (file_remove_privs) depending on fsuid/capabilities —
			// creation mode alone is not enough for sudo/passwd to work
			// in the guest.
			if err := os.Chmod(p, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Symlink(hdr.Linkname, p); err != nil {
				return err
			}
		case tar.TypeLink:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Link(filepath.Join(dst, hdr.Linkname), p); err != nil {
				return err
			}
		default:
			fmt.Printf("skip %s: unsupported tar entry %s (%c)\n", hdr.Name, hdr.Name, hdr.Typeflag)
		}
	}
}

// configureNetwork renders NetworkManager keyfiles (same format on all
// supported families). psk travels as a secret reference; the raw value is
// read from the 0600 secrets store here and written ONLY into the 0600
// keyfile. uuids derive from the profile name: reruns are byte-stable.
var kodosUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// connUUID renders a canonical RFC-4122 v4 UUID derived from the
// connection name: same name => same UUID across installs (keyfile
// profiles are declarative), but spelled the way UUID parsers expect.
func connUUID(name string) string {
	sum := sha256.Sum256([]byte("kodos-conn/" + name))
	sum[6] = sum[6]&0x0f | 0x40 // version 4
	sum[8] = sum[8]&0x3f | 0x80 // variant 10xx
	u := hex.EncodeToString(sum[:16])
	return u[0:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:32]
}

func (e *executor) configureNetwork(act map[string]any) error {
	list, _ := act["connections"].([]any)
	for _, item := range list {
		c, _ := item.(map[string]any)
		name, _ := c["name"].(string)
		ctype, _ := c["type"].(string)
		if name == "" || ctype == "" {
			return fmt.Errorf("configure_network: malformed connection entry")
		}
		var nmType, body string
		switch ctype {
		case "ethernet":
			nmType = "802-3-ethernet"
		case "wifi":
			nmType = "802-11-wireless"
			ssid, _ := c["ssid"].(string)
			pskRef, _ := c["psk"].(map[string]any)
			key, _ := pskRef["secret"].(string)
			psk, ok := e.secrets[key]
			if !ok {
				return fmt.Errorf("configure_network %s: secret %q not in secrets store", name, key)
			}
			body = fmt.Sprintf("\n[wifi]\nssid=%s\nmode=infrastructure\n\n[wifi-security]\nkey-mgmt=wpa-psk\npsk=%s\n", ssid, psk)
		default:
			return fmt.Errorf("configure_network %s: unknown type %q", name, ctype)
		}
		// Deterministic v4 UUID from the connection name (stable profile
		// id across installs). Hand-splicing hex once produced a 2-char
		// 4th segment; NM silently rejects such keyfiles (see selftest).
		uuid := connUUID(name)
		if !kodosUUIDRe.MatchString(uuid) {
			return fmt.Errorf("configure_network %s: internal bug, bad uuid %q", name, uuid)
		}
		content := fmt.Sprintf("[connection]\nid=%s\nuuid=%s\ntype=%s\n\n[ipv4]\nmethod=auto\n\n[ipv6]\nmethod=auto\n%s",
			name, uuid, nmType, body)
		p := filepath.Join(e.s.chroot, "/etc/NetworkManager/system-connections", name+".nmconnection")
		os.Remove(p) // never append onto an existing 0600 file
		// parent at vanilla mode 0755 (MkdirAll would otherwise stamp 0700
		// onto /etc/NetworkManager itself); only system-connections is
		// 0700, because it holds keyfiles with psks.
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s (mode 0600, psk from secrets)\n", strings.TrimPrefix(p, e.s.chroot))
	}
	return nil
}

func (e *executor) createGeneration(act map[string]any) error {
	// Live /etc edits made after the last activation (shadow's
	// groupadd/useradd rename the managed file away) must be captured into
	// the current store before it is seeded forward into the new generation.
	if err := e.syncMutatedEtc(); err != nil {
		return err
	}
	names := strList(act, "packages")
	toInstall := make(map[string]string, len(names))
	if e.resolved != nil {
		// install_packages already closed the set with apk's solver:
		// the generation carries the FULL closure (musl, busybox, ...)
		for n, v := range e.resolved {
			toInstall[n] = v
		}
		for _, n := range names {
			if _, ok := toInstall[n]; !ok {
				return fmt.Errorf("create_generation: %s missing from resolved closure", n)
			}
		}
	} else {
		// no apk run this process (dry-run/selftest): fall back to pins
		for _, n := range names {
			pin, ok := e.meta.Packages[n]
			if !ok {
				return fmt.Errorf("create_generation: %s not pinned in metadata", n)
			}
			toInstall[n] = pin.Version
		}
	}
	idx := e.meta.Generation
	prev := ""
	if cand := e.s.genDir(idx - 1); idx > 0 && isRealDir(cand) {
		prev = cand
	}
	e.lastGen = toInstall
	genDir, err := e.s.createGeneration(toInstall, idx, prev)
	if err != nil {
		return err
	}
	// Write generation.json with metadata + executor stamps
	if err := e.writeGenerationManifest(genDir); err != nil {
		return err
	}
	fmt.Printf("generation %d created (%d packages)\n", idx, len(toInstall))
	return nil
}

// stampGeneration writes generation.json manifest for a generation directory,
// stamping metadata with timestamp, config_hash, and kernel_args.
// Used by both executor's create_generation handler and selftest.
func stampGeneration(genDir string, meta *metadata, kernelArgs []string, metadataFileBytes, actionsFileBytes []byte) error {
	// Compute config_hash: sha256 of metadata file if provided, else actions stream
	var configHash string
	if len(metadataFileBytes) > 0 {
		// Primary: hash the -metadata file (version lock is the contract)
		h := sha256.Sum256(metadataFileBytes)
		configHash = hex.EncodeToString(h[:])
	} else if len(actionsFileBytes) > 0 {
		// Fallback: no -metadata (selftest/synth flows), use actions stream
		h := sha256.Sum256(actionsFileBytes)
		configHash = hex.EncodeToString(h[:])
	}
	
	// Stamp the metadata with manifest fields
	meta.Timestamp = time.Now().UTC().Format(time.RFC3339)
	meta.ConfigHash = configHash
	meta.KernelArgs = kernelArgs
	
	// Marshal metadata struct directly (now has the extra fields)
	manifestJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal generation manifest: %w", err)
	}
	
	manifestPath := filepath.Join(genDir, "generation.json")
	if err := os.WriteFile(manifestPath, manifestJSON, 0o644); err != nil {
		return fmt.Errorf("write generation.json: %w", err)
	}
	return nil
}

func (e *executor) writeGenerationManifest(genDir string) error {
	return stampGeneration(genDir, e.meta, e.kernelArgs, e.metadataFileBytes, e.actionsFileBytes)
}
