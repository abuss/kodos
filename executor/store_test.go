package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Bootstrap gap (NanoVM field round 2026-11-16): the action stream's
// write_file ops land in the LIVE root's /etc before any generation
// exists, but seedEtc only recognized "local edits" via a previous
// generation's etc_store. Result: package-shipped conffiles were seeded
// verbatim, silently dropping e.g. the /etc/mkinitfs/mkinitfs.conf
// feature trim — gen0's chrooted mkinitfs then saw Alpine's stock `kms`
// feature and built a 180MB initramfs minimal UEFI firmwares refuse.
func TestSeedEtcBootstrapKeepsStreamEdits(t *testing.T) {
	root := t.TempDir()
	pkgEtc := filepath.Join(root, "kod/store/base/1.0/etc/mkinitfs")
	if err := os.MkdirAll(pkgEtc, 0o755); err != nil {
		t.Fatal(err)
	}
	pristine := "features=\"ata base kms\"\n"
	edited := "features=\"ata base\"\n"
	if err := os.WriteFile(filepath.Join(pkgEtc, "mkinitfs.conf"), []byte(pristine), 0o644); err != nil {
		t.Fatal(err)
	}
	liveEtc := filepath.Join(root, "etc/mkinitfs")
	if err := os.MkdirAll(liveEtc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveEtc, "mkinitfs.conf"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &system{chroot: root}
	gen := filepath.Join(root, "kod/generation/0")
	if err := s.seedEtc(gen, map[string]string{"base": "1.0"}, ""); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"etc_store/mkinitfs/mkinitfs.conf", "etc/mkinitfs/mkinitfs.conf"} {
		b, err := os.ReadFile(filepath.Join(gen, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if string(b) != edited {
			t.Errorf("%s = %q, want the stream edit %q", p, string(b), edited)
		}
	}
	b, err := os.ReadFile(filepath.Join(gen, "etc_default/mkinitfs/mkinitfs.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != pristine {
		t.Errorf("etc_default = %q, want pristine %q", string(b), pristine)
	}
}
