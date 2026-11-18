package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func filesExec(t *testing.T, distro string) (*executor, string) {
	t.Helper()
	root := t.TempDir()
	e := &executor{s: &system{chroot: root, Distro: distro},
		meta: &metadata{Distro: distro, Generation: 0}}
	return e, root
}

func TestConfigureLocaleArch(t *testing.T) {
	e, root := filesExec(t, "arch")
	if err := e.configureLocale(map[string]any{"default": "en_US.UTF-8"}); err != nil {
		t.Fatal(err)
	}
	lg, err := os.ReadFile(filepath.Join(root, "etc/locale.gen"))
	if err != nil || !strings.Contains(string(lg), "en_US.UTF-8 UTF-8") {
		t.Errorf("etc/locale.gen: %v %q", err, lg)
	}
	lc, err := os.ReadFile(filepath.Join(root, "etc/locale.conf"))
	if err != nil || string(lc) != "LANG=\"en_US.UTF-8\"\n" {
		t.Errorf("etc/locale.conf: %v %q", err, lc)
	}
	if lexists(filepath.Join(root, "etc/env.d/02locale")) {
		t.Error("arch must not write /etc/env.d/02locale")
	}
	if !e.needLocaleGen {
		t.Error("needLocaleGen must be set for the activate hook")
	}
	// alpine regression
	e2, root2 := filesExec(t, "alpine")
	if err := e2.configureLocale(map[string]any{"default": "en_US.UTF-8"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(root2, "etc/env.d/02locale")); err != nil {
		t.Errorf("alpine locale regression: %v", err)
	}
	if e2.needLocaleGen {
		t.Error("alpine must not arm locale-gen")
	}
}

func TestConfigureKeyboardArch(t *testing.T) {
	e, root := filesExec(t, "arch")
	if err := e.configureKeyboard(map[string]any{"layout": "us", "console_keymap": "dvorak"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "etc/vconsole.conf"))
	if err != nil || string(b) != "KEYMAP=dvorak\n" {
		t.Errorf("vconsole.conf: %v %q", err, b)
	}
	if lexists(filepath.Join(root, "etc/conf.d/keymaps")) {
		t.Error("arch must not write /etc/conf.d/keymaps")
	}
}

func TestConfigureTimezoneArch(t *testing.T) {
	e, root := filesExec(t, "arch")
	if err := e.configureTimezone(map[string]any{"timezone": "America/Edmonton"}); err != nil {
		t.Fatal(err)
	}
	tgt, err := os.Readlink(filepath.Join(root, "etc/localtime"))
	if err != nil || tgt != "/usr/share/zoneinfo/America/Edmonton" {
		t.Errorf("localtime: %v %q", err, tgt)
	}
	if lexists(filepath.Join(root, "etc/timezone")) {
		t.Error("arch must not write /etc/timezone")
	}
	// alpine regression: file still written
	e2, root2 := filesExec(t, "alpine")
	if err := e2.configureTimezone(map[string]any{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root2, "etc/timezone")); err != nil || string(b) != "UTC\n" {
		t.Errorf("alpine /etc/timezone regression: %v", err)
	}
}

func TestLoadKernelModulesArch(t *testing.T) {
	e, root := filesExec(t, "arch")
	act := map[string]any{"modules": []any{"virtio_pci", "xhci_pci"}}
	if err := e.loadKernelModules(act); err != nil {
		t.Fatal(err)
	}
	if err := e.loadKernelModules(act); err != nil { // idempotent
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "etc/modules-load.d/kodos.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "virtio_pci\nxhci_pci\n" {
		t.Errorf("modules-load.d content %q", b)
	}
}

func TestEtcConflictNames(t *testing.T) {
	for distro, wantSuffix := range map[string]string{"arch": ".pacnew-1", "alpine": ".apknew-1"} {
		s := &system{chroot: t.TempDir(), Distro: distro}
		gen := filepath.Join(s.chroot, "kod/generation/0")
		os.MkdirAll(filepath.Join(gen, "etc_default"), 0o755)
		newDefault := filepath.Join(gen, "etc_default/conf")
		os.WriteFile(newDefault, []byte("new default\n"), 0o644)
		etcDir := filepath.Join(s.chroot, "etc")
		os.MkdirAll(etcDir, 0o755)
		live := filepath.Join(etcDir, "conf")
		os.WriteFile(live, []byte("local edit\n"), 0o644)
		if err := s.installEtc(filepath.Join(gen, "etc/conf"), live, "conf"); err != nil {
			t.Fatal(err)
		}
		if !lexists(filepath.Join(etcDir, "conf"+wantSuffix)) {
			names, _ := os.ReadDir(etcDir)
			t.Errorf("%s: want conf%s, etc has %v", distro, wantSuffix, names)
		}
		if b, _ := os.ReadFile(live); string(b) != "local edit\n" {
			t.Errorf("%s: local edit lost", distro)
		}
	}
}
