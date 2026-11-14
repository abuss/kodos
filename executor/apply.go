package main

// kodos apply: the machine manages ITSELF. The compiled pipeline of
// generate --inplace + lockfile (vm/run.sh's install flow minus partitioning):
// resolve config -> actions stream -> fetch ALL repo indexes live -> lock
// closure -> write actions+lock under <root>/kod/actions -> exec against root.
// Every fetch completes before any write/exec happens (Review Focus 1);
// config errors name the exact path tried (Review Focus 2).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// resolveConfig maps the CLI arg to (absolute entry file, its dir).
// DIR -> DIR/config.lua; a .lua file -> itself; "" -> /etc/kodos/config.lua.
// Error text always names the exact path that was tried.
func resolveConfig(arg string) (string, string, error) {
	if arg == "" {
		arg = "/etc/kodos/config.lua" // installed-machine default
	}
	fi, err := os.Stat(arg)
	if err != nil {
		return "", "", fmt.Errorf("config not found: %s", arg)
	}
	entry := arg
	if fi.IsDir() {
		entry = filepath.Join(arg, "config.lua")
		if _, err := os.Stat(entry); err != nil {
			return "", "", fmt.Errorf("config not found: %s", entry)
		}
	} else if !strings.HasSuffix(entry, ".lua") {
		return "", "", fmt.Errorf("config file must be *.lua: %s", entry)
	}
	// absolute: runLuaScript chdirs to the embedded lua dir, so the entry
	// path handed to generate.lua/lockfile.lua must not be relative
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", "", err
	}
	return abs, filepath.Dir(abs), nil
}

// localRepoRef is one file:// repos entry from the configure_repos action.
type localRepoRef struct{ name, url, src string }

// fetchIndexes fetches every repo index into tmp and returns the paths in
// lockfile.lua's order (https branches first, local repos last — first
// index wins = repo priority). It fails naming the exact URL/path, and
// writes nothing outside tmp: apply calls it before any other side effect.
func fetchIndexes(mirror string, branches []string, localRepos []localRepoRef, tmp string) ([]string, error) {
	var out []string
	client := &http.Client{Timeout: 2 * time.Minute}
	for _, br := range branches {
		url := fmt.Sprintf("%s/%s/x86_64/APKINDEX.tar.gz", mirror, br)
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetch index %s: %w", url, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("fetch index %s: HTTP %d", url, resp.StatusCode)
		}
		blob, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("fetch index %s: %w", url, err)
		}
		dst := filepath.Join(tmp, br+".apkindex")
		if err := extractAPKINDEX(blob, dst); err != nil {
			return nil, fmt.Errorf("fetch index %s: %w", url, err)
		}
		out = append(out, dst)
	}
	for _, lr := range localRepos {
		// src (host-abs, from generator provenance) when declared; else the
		// raw file:// url path (Task 7-style host-absolute test repos).
		base := lr.src
		if base == "" {
			base = strings.TrimPrefix(lr.url, "file://")
		}
		idx := filepath.Join(base, "x86_64", "APKINDEX.tar.gz")
		if _, err := os.Stat(idx); err != nil {
			return nil, fmt.Errorf("local repo %s index: %s: %w", lr.name, idx, err)
		}
		out = append(out, idx) // lockfile.lua consumes .tar.gz paths directly
	}
	return out, nil
}

// extractAPKINDEX pulls the APKINDEX member out of an APKINDEX.tar.gz blob.
func extractAPKINDEX(blob []byte, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("not a gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no APKINDEX member in tar.gz")
		}
		if err != nil {
			return err
		}
		if hdr.Name == "APKINDEX" && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, data, 0o600)
		}
	}
}

// nextGeneration = max numeric generation dir + 1; never reuses numbers.
// Missing/empty gens dir -> 0; non-contiguous ok; the current alias is
// skipped (a symlink: ReadDir Lstats, so its IsDir() is false).
func nextGeneration(genDir string) (int, error) {
	ents, err := os.ReadDir(genDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	max := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(e.Name()); err == nil && n+1 > max {
			max = n + 1
		}
	}
	return max, nil
}

// parseApplyArgs splits the apply CLI: Go's flag stops at the first
// non-flag, but the documented form puts flags AFTER the DIR|FILE arg,
// so that arg is lifted out first. Anything left over is a stray
// positional (or a flag that landed after it): fail closed instead of
// silently dropping it — that hazard meant a mutating apply against /.
func parseApplyArgs(args []string) (arg string, dryRun bool, root, apkBin string, err error) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: kodos apply [DIR|FILE.lua] [--dry-run]") }
	fs.BoolVar(&dryRun, "dry-run", false, "run generate+lock, print the plan, change nothing")
	fs.StringVar(&root, "root", "/", "target root filesystem (test passthrough, mirrors exec)")
	fs.StringVar(&apkBin, "apk", "apk", "apk binary for closure resolution (test passthrough)")
	flagArgs := make([]string, 0, len(args))
	for _, a := range args {
		if arg == "" && !(len(a) > 1 && a[0] == '-') {
			arg = a
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return "", false, "", "", err
	}
	if fs.NArg() != 0 {
		return "", false, "", "", fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return arg, dryRun, root, apkBin, nil
}

func cmdApply(args []string) error {
	arg, dryRun, root, apkBin, err := parseApplyArgs(args)
	if err != nil {
		return err
	}
	return apply(arg, dryRun, root, apkBin)
}

func apply(configArg string, dryRun bool, root, apkBin string) error {
	entry, confDir, err := resolveConfig(configArg)
	if err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// KODOS_APK for lockfile.lua's closure solver: resolve to an absolute
	// path (runLuaScript chdirs; io.open there is relative to the lua dir).
	apkAbs := ""
	if abs, err := filepath.Abs(apkBin); err == nil && lexists(abs) {
		apkAbs = abs
	} else if p, err := exec.LookPath(apkBin); err == nil && filepath.IsAbs(p) {
		apkAbs = p
	}

	// 1. actions stream (config dir on the Lua module path via searchDir)
	actionsOut, err := runLuaScript("generate.lua", []string{"--inplace", entry}, confDir)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	var af actionsFile
	if err := json.Unmarshal([]byte(actionsOut), &af); err != nil {
		return fmt.Errorf("parse generate output: %w", err)
	}

	// 2. the configure_repos action carries mirror/branches/repos (#1 always present)
	var mirror string
	var branches []string
	var localRepos []localRepoRef
	for _, act := range af.Actions {
		if field(act, "op") != "configure_repos" {
			continue
		}
		mirror = field(act, "mirror")
		branches = strList(act, "branches")
		if rs, _ := act["repos"].([]any); rs != nil {
			for _, r := range rs {
				m, _ := r.(map[string]any)
				lr := localRepoRef{name: field(m, "name"), url: field(m, "url")}
				lr.src, _ = m["src"].(string)
				localRepos = append(localRepos, lr)
			}
		}
		break
	}
	if mirror == "" || len(branches) == 0 {
		return fmt.Errorf("no configure_repos op (or no mirror/branches) in the generated stream")
	}

	// 3. ALL index fetching happens here, before any other side effect
	// (Review Focus 1): a failed fetch = error + temp cleanup, nothing else ran.
	tmp, err := os.MkdirTemp("", "kodos-apply-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	indexes, err := fetchIndexes(mirror, branches, localRepos, tmp)
	if err != nil {
		return err
	}

	// 4. generation number: max existing + 1, never reused
	gen, err := nextGeneration(filepath.Join(rootAbs, "kod", "generation"))
	if err != nil {
		return err
	}

	// 5. lock the closure against exactly these index snapshots
	lockArgs := append([]string{strconv.Itoa(gen)}, indexes...)
	lockArgs = append(lockArgs, "--", entry)
	if apkAbs != "" {
		os.Setenv("KODOS_APK", apkAbs)
		defer os.Unsetenv("KODOS_APK")
	}
	lockOut, err := runLuaScript("lockfile.lua", lockArgs, confDir)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}

	if dryRun {
		printPlan(actionsOut, lockOut, gen, rootAbs)
		return nil
	}

	// 6. stage actions+lock under the target root (brief said 0600 dir; a
	// dir needs its x bit, so 0700 dir + 0600 files — deliberate deviation)
	actionsDir := filepath.Join(rootAbs, "kod", "actions")
	if err := os.MkdirAll(actionsDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(actionsDir, 0o700); err != nil { // pre-existing wider dir gets tightened
		return err
	}
	staged := []struct{ label, path, content string }{
		{"stage actions", filepath.Join(actionsDir, fmt.Sprintf("actions-%d.json", gen)), actionsOut},
		{"stage generation", filepath.Join(actionsDir, fmt.Sprintf("generation-%d.json", gen)), lockOut},
	}
	for _, s := range staged {
		if err := os.WriteFile(s.path, []byte(s.content), 0o600); err != nil {
			return fmt.Errorf("%s: %w", s.label, err)
		}
		if err := os.Chmod(s.path, 0o600); err != nil { // re-runs keep old modes otherwise
			return fmt.Errorf("%s: %w", s.label, err)
		}
	}
	fmt.Printf("apply: generation %d staged in %s (actions-%d.json, generation-%d.json, 0600)\n",
		gen, actionsDir, gen, gen)

	// 7. execute in place
	execArgs := []string{"-actions", staged[0].path, "-metadata", staged[1].path,
		"-root", rootAbs, "-apk", apkBin}
	if secrets := filepath.Join(confDir, "secrets.json"); lexists(secrets) {
		execArgs = append(execArgs, "-secrets", secrets)
	}
	if err := cmdExec(execArgs); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	return nil
}

// printPlan summarizes generate+lock output for --dry-run: zero filesystem
// mutation beyond the temp dir (which apply's defer already removed).
func printPlan(actionsOut, lockOut string, gen int, root string) {
	var af actionsFile
	var lock metadata
	if err := json.Unmarshal([]byte(actionsOut), &af); err != nil {
		fmt.Printf("DRY-RUN: actions unparsable: %v\n", err)
		return
	}
	if err := json.Unmarshal([]byte(lockOut), &lock); err != nil {
		fmt.Printf("DRY-RUN: lock unparsable: %v\n", err)
		return
	}
	fmt.Printf("DRY-RUN plan: next generation %d, %d actions, %d packages locked\n",
		gen, len(af.Actions), len(lock.Packages))
	ops := make([]string, len(af.Actions))
	for i, a := range af.Actions {
		ops[i] = field(a, "op")
	}
	show := ops
	if len(show) > 6 {
		show = append(append(append([]string{}, ops[:3]...), "..."), ops[len(ops)-3:]...)
	}
	fmt.Printf("  stream: %s\n", strings.Join(show, " -> "))

	// added/removed vs the current generation's manifest, if readable
	var curNames map[string]bool
	if target, err := os.Readlink(filepath.Join(root, "kod", "generation", "current")); err == nil {
		var curGen int
		if _, scanErr := fmt.Sscanf(filepath.Base(target), "%d", &curGen); scanErr == nil {
			var cur metadata
			p := filepath.Join(root, "kod", "generation", filepath.Base(target), "generation.json")
			if err := json.Unmarshal(mustRead(p), &cur); err == nil {
				curNames = map[string]bool{}
				for name := range cur.Packages {
					curNames[name] = true
				}
			}
		}
	}
	if curNames == nil {
		fmt.Printf("  packages: %d (no readable current generation to diff against)\n", len(lock.Packages))
		return
	}
	var added, removed []string
	for name := range lock.Packages {
		if !curNames[name] {
			added = append(added, name)
		}
	}
	for name := range curNames {
		if _, ok := lock.Packages[name]; !ok {
			removed = append(removed, name)
		}
	}
	fmt.Printf("  vs current gen: +%d/-%d packages\n", len(added), len(removed))
	for _, list := range [][]string{added, removed} {
		if len(list) > 0 && len(list) <= 12 {
			fmt.Printf("    %s\n", strings.Join(list, " "))
		}
	}
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}
