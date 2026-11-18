package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archSvcExec(t *testing.T) (*executor, string) {
	t.Helper()
	root := t.TempDir()
	sysdir := filepath.Join(root, "kod/generation/0/usr/lib/systemd/system")
	os.MkdirAll(sysdir, 0o755)
	os.WriteFile(filepath.Join(sysdir, "sshd.service"), []byte("[Service]\n"), 0o644)
	os.WriteFile(filepath.Join(sysdir, "serial-getty@.service"), []byte("[Service]\n"), 0o644)
	e := &executor{s: &system{chroot: root}, meta: &metadata{Distro: "arch", Generation: 0}}
	return e, root
}

func TestEnableServiceArch(t *testing.T) {
	e, root := archSvcExec(t)
	if err := e.enableService(map[string]any{"name": "ssh", "unit": "sshd", "runlevel": "default"}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "etc/systemd/system/multi-user.target.wants/sshd.service")
	tgt, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("wants symlink: %v", err)
	}
	if tgt != "/usr/lib/systemd/system/sshd.service" {
		t.Errorf("link target %q", tgt)
	}
	if !e.declaredRunlevels["multi-user.target.wants/sshd.service"] {
		t.Errorf("declared key missing: %v", e.declaredRunlevels)
	}

	// Review Focus 1: a unit no package ships must fail closed (systemd
	// would silently skip a dangling wants link at boot).
	err = e.enableService(map[string]any{"name": "nope", "unit": "nope", "runlevel": "default"})
	if err == nil || !strings.Contains(err.Error(), "not shipped") {
		t.Errorf("dangling unit must error, got: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "etc/systemd/system/multi-user.target.wants/nope.service")); err == nil {
		t.Error("failed enable must not leave a link")
	}

	// templated instance + getty target
	if err := e.enableService(map[string]any{"name": "serial-console",
		"unit": "serial-getty@ttyS0", "runlevel": "getty"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(root,
		"etc/systemd/system/getty.target.wants/serial-getty@ttyS0.service")); err != nil {
		t.Errorf("templated wants link: %v", err)
	}

	// OpenRC-only runlevels are unsupported on systemd
	if err := e.enableService(map[string]any{"name": "x", "unit": "sshd", "runlevel": "sysinit"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Errorf("sysinit runlevel must error, got: %v", err)
	}

	// idempotent rerun
	if err := e.enableService(map[string]any{"name": "ssh", "unit": "sshd", "runlevel": "default"}); err != nil {
		t.Errorf("rerun: %v", err)
	}
}

func TestReconcileWants(t *testing.T) {
	e, root := archSvcExec(t)
	if err := e.enableService(map[string]any{"name": "ssh", "unit": "sshd", "runlevel": "boot"}); err != nil {
		t.Fatal(err)
	}
	wants := filepath.Join(root, "etc/systemd/system/multi-user.target.wants")
	os.Symlink("/usr/lib/systemd/system/bluetooth.service", filepath.Join(wants, "bluetooth.service"))
	if err := e.reconcileRunlevels(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(wants, "bluetooth.service")); err == nil {
		t.Error("undeclared wants link must be removed")
	}
	if _, err := os.Lstat(filepath.Join(wants, "sshd.service")); err != nil {
		t.Error("declared wants link must survive")
	}
}
