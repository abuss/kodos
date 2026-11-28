package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func acts(distro string) *actionsFile {
	return &actionsFile{SchemaVersion: 1, Distro: distro,
		Actions: []map[string]any{{"op": "install_packages"}}}
}

func TestCheckDistro(t *testing.T) {
	if err := checkDistro(acts("arch"), &metadata{Distro: "arch"}); err != nil {
		t.Errorf("arch must be accepted, got: %v", err)
	}
	err := checkDistro(acts("deb"), &metadata{Distro: "deb"})
	if err == nil || !strings.Contains(err.Error(), "deb") {
		t.Errorf("deb must be rejected naming the distro, got: %v", err)
	}
	err = checkDistro(acts("arch"), &metadata{Distro: "alpine"})
	if err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("actions/meta distro mismatch must error, got: %v", err)
	}
	if err := checkDistro(acts("alpine"), &metadata{Distro: "alpine"}); err != nil {
		t.Errorf("alpine regression: %v", err)
	}
}

// install_aur_packages must load (ranked beside install_packages) and run
// gracefully on an executor without metadata (arch-only guard + no builder
// user -> warn-and-continue).
func TestAURStubOp(t *testing.T) {
	af := actionsFile{SchemaVersion: 1, Distro: "arch", Actions: []map[string]any{
		{"op": "install_packages"},
		{"op": "install_aur_packages", "helper": "yay", "names": []any{"visual-studio-code-bin"}},
		{"op": "configure_locale"},
	}}
	path := t.TempDir() + "/actions.json"
	b, _ := json.Marshal(af)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadActions(path)
	if err != nil {
		t.Fatalf("install_aur_packages must be a known op, got: %v", err)
	}
	if len(got.Actions) != 3 {
		t.Fatalf("actions = %d, want 3", len(got.Actions))
	}
	var e executor
	if err := e.run(af.Actions[1]); err != nil {
		t.Errorf("stub must warn-and-continue, got error: %v", err)
	}
}

func TestPkgPinRepo(t *testing.T) {
	var p pkgPin
	if err := json.Unmarshal([]byte(`{"version":"6.1-1","checksum":"ab","repo":"core","arch":"any"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Repo != "core" {
		t.Errorf("Repo = %q, want core", p.Repo)
	}
	if p.Arch != "any" {
		t.Errorf("Arch = %q, want any", p.Arch)
	}
	b, _ := json.Marshal(pkgPin{Version: "1-1", Checksum: "ab"})
	if strings.Contains(string(b), "repo") || strings.Contains(string(b), "arch") {
		t.Errorf("empty Repo/Arch must be omitted, got %s", b)
	}
}
