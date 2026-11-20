package main

// Unit tests for `kodos apply` plumbing: resolveConfig (Review Focus 2:
// error text names the exact attempted path) and fetchIndexes (Review
// Focus 1: every index fetch completes — or fails naming the URL — before
// any other side effect).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.lua"), []byte("return {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// (a) DIR with config.lua -> DIR/config.lua, dir = config dir
	entry, cfgDir, err := resolveConfig(dir)
	if err != nil {
		t.Fatalf("dir with config.lua: unexpected error: %v", err)
	}
	if entry != filepath.Join(dir, "config.lua") || cfgDir != dir {
		t.Errorf("dir: got entry=%q dir=%q", entry, cfgDir)
	}

	// (b) DIR without config.lua -> error naming the exact tried path
	dir2 := t.TempDir()
	_, _, err = resolveConfig(dir2)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir2, "config.lua")) {
		t.Fatalf("dir without config.lua: error %v must name %s/config.lua", err, dir2)
	}

	// (c) single .lua file -> itself
	f := filepath.Join(dir, "machine.lua")
	if err := os.WriteFile(f, []byte("return {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, cfgDir, err = resolveConfig(f)
	if err != nil {
		t.Fatalf("single .lua: unexpected error: %v", err)
	}
	if entry != f || cfgDir != dir {
		t.Errorf("file: got entry=%q dir=%q", entry, cfgDir)
	}

	// (d) empty arg -> default /etc/kodos/config.lua; absent -> error names it
	if _, err := os.Stat("/etc/kodos/config.lua"); err == nil {
		t.Skip("/etc/kodos/config.lua exists on this host; cannot test the absent-default case")
	}
	_, _, err = resolveConfig("")
	if err == nil || !strings.Contains(err.Error(), "/etc/kodos/config.lua") {
		t.Fatalf("default path: error %v must name /etc/kodos/config.lua", err)
	}

	// (e) nonexistent path -> error names the path
	_, _, err = resolveConfig(filepath.Join(dir, "nope"))
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "nope")) {
		t.Fatalf("nonexistent: error %v must name the path", err)
	}

	// (f) FILE form must end in .lua
	notlua := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(notlua, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveConfig(notlua)
	if err == nil || !strings.Contains(err.Error(), "must be *.lua") ||
		!strings.Contains(err.Error(), notlua) {
		t.Fatalf("non-.lua file: error %v must be %q naming the path", err, "config file must be *.lua: <path>")
	}
}

func TestParseApplyArgs(t *testing.T) {
	// canonical documented form: flags AFTER the positional arg
	arg, dryRun, root, apkBin, err := parseApplyArgs([]string{"cfg.lua", "-root", "r", "-apk", "a"})
	if err != nil || arg != "cfg.lua" || dryRun || root != "r" || apkBin != "a" {
		t.Fatalf("canonical: got arg=%q dry=%v root=%q apk=%q err=%v", arg, dryRun, root, apkBin, err)
	}
	// flags before the arg work too
	arg, dryRun, _, _, err = parseApplyArgs([]string{"--dry-run", "cfg.lua"})
	if err != nil || arg != "cfg.lua" || !dryRun {
		t.Fatalf("flags-first: got arg=%q dry=%v err=%v", arg, dryRun, err)
	}
	// stray positional: MUST error (fail closed) — silently stopping flag
	// parsing here once meant a mutating apply against the default root /
	_, _, _, _, err = parseApplyArgs([]string{"cfg.lua", "stray", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "stray") {
		t.Fatalf("stray arg: error %v must reject \"stray\"", err)
	}
}

// apkIndexTarGz builds an APKINDEX.tar.gz blob (one member named APKINDEX).
func apkIndexTarGz(t *testing.T, text string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "APKINDEX", Mode: 0o644,
		Size: int64(len(text)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return gz.Bytes()
}

func TestFetchIndexes(t *testing.T) {
	indexText := "P: dummy\nV: 1.0-r0\nC: sha256:abc\n\n"
	blob := apkIndexTarGz(t, indexText)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/good/") {
			w.Write(blob)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	// 200: the APKINDEX member lands in tmp as <branch>.apkindex, verbatim.
	tmp := t.TempDir()
	paths, err := fetchIndexes(srv.URL+"/v3.24", []string{"good"}, nil, tmp)
	if err != nil {
		t.Fatalf("200 fetch: %v", err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "good.apkindex" {
		t.Fatalf("200 fetch: got paths %v", paths)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || string(got) != indexText {
		t.Errorf("extracted content = %q, err %v; want %q", got, err, indexText)
	}

	// 404: error MUST name the exact URL, and NO partial files stay in tmp.
	tmp2 := t.TempDir()
	_, err = fetchIndexes(srv.URL+"/v3.24", []string{"bad"}, nil, tmp2)
	wantURL := srv.URL + "/v3.24/bad/x86_64/APKINDEX.tar.gz"
	if err == nil || !strings.Contains(err.Error(), wantURL) {
		t.Fatalf("404 fetch: error %v must name %s", err, wantURL)
	}
	if ents, _ := os.ReadDir(tmp2); len(ents) != 0 {
		t.Errorf("404 fetch left %d files in tmp dir", len(ents))
	}
}

func TestChooseIndexFetch(t *testing.T) {
	if got, err := chooseIndexFetch("arch", nil); err != nil || got != "arch" {
		t.Fatalf("arch: got %q, %v", got, err)
	}
	if got, err := chooseIndexFetch("apk", []localRepoRef{{name: "local"}}); err != nil || got != "apk" {
		t.Fatalf("apk with local repo: got %q, %v", got, err)
	}

	_, err := chooseIndexFetch("arch", []localRepoRef{{name: "local"}})
	if err == nil || !strings.Contains(err.Error(), "local repositories are not supported for Arch") {
		t.Fatalf("arch with local repo: error %v", err)
	}

	for _, repoType := range []string{"", "deb"} {
		_, err = chooseIndexFetch(repoType, nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported repo type") {
			t.Fatalf("repo type %q: error %v", repoType, err)
		}
	}
}

func TestNextGeneration(t *testing.T) {
	base := t.TempDir()
	gen := filepath.Join(base, "kod", "generation")

	// no generations dir at all -> 0
	if n, err := nextGeneration(gen); err != nil || n != 0 {
		t.Fatalf("missing dir: got %d, %v; want 0, <nil>", n, err)
	}

	// non-contiguous numeric dirs + the current alias symlink -> max+1
	for _, d := range []string{"0", "1", "3"} {
		if err := os.MkdirAll(filepath.Join(gen, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(gen, "3"), filepath.Join(gen, "current")); err != nil {
		t.Fatal(err)
	}
	if n, err := nextGeneration(gen); err != nil || n != 4 {
		t.Fatalf("gens 0,1,3+current: got %d, %v; want 4, <nil>", n, err)
	}
}
