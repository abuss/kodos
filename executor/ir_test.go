package main

import (
	"encoding/json"
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

func TestPkgPinRepo(t *testing.T) {
	var p pkgPin
	if err := json.Unmarshal([]byte(`{"version":"6.1-1","checksum":"ab","repo":"core"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Repo != "core" {
		t.Errorf("Repo = %q, want core", p.Repo)
	}
	b, _ := json.Marshal(pkgPin{Version: "1-1", Checksum: "ab"})
	if strings.Contains(string(b), "repo") {
		t.Errorf("empty Repo must be omitted, got %s", b)
	}
}
