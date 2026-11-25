package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureReposArch(t *testing.T) {
	root := t.TempDir()
	e := &executor{s: &system{chroot: root}, meta: &metadata{Distro: "arch"}}
	act := map[string]any{"type": "arch", "mirror": "https://geo.mirror.pkgbuild.com",
		"branches": []any{"core", "extra"}}
	if err := e.configureRepos(act); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/pacman.conf")); err != nil ||
		!strings.Contains(string(b), "[core]") {
		t.Errorf("etc/pacman.conf missing: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/pacman.d/mirrorlist")); err != nil ||
		!strings.Contains(string(b), "Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch") {
		t.Errorf("etc/pacman.d/mirrorlist missing/wrong: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc/apk")); err == nil {
		t.Error("arch must not write /etc/apk")
	}
	// alpine regression: untouched
	root2 := t.TempDir()
	e2 := &executor{s: &system{chroot: root2}, meta: &metadata{Distro: "alpine"}}
	act2 := map[string]any{"type": "apk", "mirror": "https://dl-cdn.alpinelinux.org/alpine/v3.24",
		"branches": []any{"main"}}
	if err := e2.configureRepos(act2); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root2, "etc/apk/repositories")); err != nil ||
		!strings.Contains(string(b), "v3.24/main") {
		t.Errorf("alpine repositories regression: %v %s", err, b)
	}
}

func TestRenderMirrorList(t *testing.T) {
	got := renderMirrorList("https://geo.mirror.pkgbuild.com")
	want := "## kodos-managed: repos.base.mirror\n" +
		"Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch\n"
	if got != want {
		t.Errorf("mirrorlist:\n got %q\nwant %q", got, want)
	}
}

func TestRenderPacmanConf(t *testing.T) {
	got := renderPacmanConf([]string{"core", "extra"})
	for _, want := range []string{
		"[options]",
		"SigLevel = Required DatabaseOptional",
		"Architecture = auto",
		"ParallelDownloads = 5",
		"[core]\nInclude = /etc/pacman.d/mirrorlist",
		"[extra]\nInclude = /etc/pacman.d/mirrorlist",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pacman.conf missing %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "[core]"), strings.Index(got, "[extra]"); i < 0 || j < 0 || i > j {
		t.Errorf("repo order core-then-extra broken:\n%s", got)
	}
}
