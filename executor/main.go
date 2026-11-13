// kodos-exec — executor spike consuming the IR contract (example/eszkoz/IR.md):
//
//	kodos-exec -actions actions.json -metadata generation-N.json -root ROOTFS
//	           [-secrets FILE] [-arch x86_64] [-dry-run]
//	kodos-exec selftest
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
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

	rootPart      string            // captured from mount / (bootloader needs UUID)
	chrootMounted bool              // pseudo-fs mounted once
	kernelArgs    []string          // stashed by set_kernel_arguments for grub.cfg
	lastGen       map[string]string // pkg->version of the last create_generation
}

func main() {
	actions := flag.String("actions", "", "actions.json (IR stream)")
	metadata := flag.String("metadata", "", "generation-N.json (version lock)")
	root := flag.String("root", "", "target root filesystem directory")
	secrets := flag.String("secrets", "", "secrets store JSON (dotted key -> value, mode 0600)")
	arch := flag.String("arch", "x86_64", "package arch for download URLs")
	dryRun := flag.Bool("dry-run", false, "validate + print the plan, change nothing")
	flag.Parse()

	if len(flag.Args()) > 0 && flag.Args()[0] == "selftest" {
		selftest()
		return
	}
	if *actions == "" || *metadata == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: kodos-exec -actions A -metadata M -root R [-secrets F] [-arch A] [-dry-run] | selftest")
		os.Exit(2)
	}

	af, err := loadActions(*actions)
	fatal(err)
	meta, err := loadMetadata(*metadata)
	fatal(err)
	if af.Distro != "alpine" {
		fatal(fmt.Errorf("spike implements the alpine/apk family only (got distro %q)", af.Distro))
	}
	if meta.Distro != af.Distro || meta.Generation < 0 {
		fatal(fmt.Errorf("actions and metadata disagree (distro/generation)"))
	}

	e := &executor{s: &system{chroot: *root}, meta: meta, arch: *arch}
	if *secrets != "" {
		fatal(loadJSON(*secrets, &e.secrets))
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
			fatal(err)
		}
	}
	e.umountChroot()
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
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
	return e.writeFileTo("/etc/apk/repositories", b.String(), 0o644)
}

func (e *executor) writeFile(act map[string]any) error {
	path, _ := act["path"].(string)
	content, _ := act["content"].(string)
	return e.writeFileTo(path, content, 0o644)
}

func (e *executor) writeFileTo(path, content string, mode os.FileMode) error {
	p := filepath.Join(e.s.chroot, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), mode)
}

// installPackages fetches each pinned <pkg>-<version>.apk (first branch that
// has it wins) and unpacks it into the store; idempotent: an existing store
// entry with content is kept (build-once-into-store contract).
func (e *executor) installPackages(act map[string]any) error {
	if e.mirror == "" {
		return fmt.Errorf("install_packages before configure_repos: no repos captured")
	}
	for _, name := range strList(act, "names") {
		pin, ok := e.meta.Packages[name]
		if !ok {
			return fmt.Errorf("%s: not pinned in metadata (lock first)", name)
		}
		dst := filepath.Join(e.s.store(), name, pin.Version)
		if isRealDir(dst) {
			continue // already in the store: no re-download, no rebuild
		}
		if err := e.fetchUnpack(name, pin.Version); err != nil {
			return err
		}
	}
	return nil
}

func (e *executor) fetchUnpack(name, version string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	var body io.ReadCloser
	var lastErr error
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
	if body == nil {
		return fmt.Errorf("fetch %s-%s: %w", name, version, lastErr)
	}
	defer body.Close()

	dst := filepath.Join(e.s.store(), name, version)
	os.RemoveAll(dst) // re-runs must not inherit half-extracted state
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	gz, err := gzip.NewReader(body) // .apk is a tar.gz
	if err != nil {
		return fmt.Errorf("unpack %s-%s: %w", name, version, err)
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
			f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
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
		sum := sha256.Sum256([]byte("kodos-conn/" + name))
		u := hex.EncodeToString(sum[:16])
		uuid := fmt.Sprintf("%s-%s-4%s-%x-%s", u[0:8], u[8:12], u[13:16], (0x80|int(sum[16]))&0xbf, u[20:32])
		content := fmt.Sprintf("[connection]\nid=%s\nuuid=%s\ntype=%s\n\n[ipv4]\nmethod=auto\n\n[ipv6]\nmethod=auto\n%s",
			name, uuid, nmType, body)
		p := filepath.Join(e.s.chroot, "/etc/NetworkManager/system-connections", name+".nmconnection")
		os.Remove(p) // never append onto an existing 0600 file
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
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
	names := strList(act, "packages")
	toInstall := make(map[string]string, len(names))
	for _, n := range names {
		pin, ok := e.meta.Packages[n]
		if !ok {
			return fmt.Errorf("create_generation: %s not pinned in metadata", n)
		}
		toInstall[n] = pin.Version
	}
	idx := e.meta.Generation
	prev := ""
	if cand := e.s.genDir(idx - 1); idx > 0 && isRealDir(cand) {
		prev = cand
	}
	e.lastGen = toInstall
	_, err := e.s.createGeneration(toInstall, idx, prev)
	if err == nil {
		fmt.Printf("generation %d created (%d packages)\n", idx, len(toInstall))
	}
	return err
}
