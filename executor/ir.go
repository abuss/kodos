package main

// IR loading + contract checks (IR.md): actions.json is
// {actions:[{op,...}...], distro, schema_version}; generation metadata is
// {generation, packages:{name:{version,checksum}}, ...}. The executor trusts
// nothing it reads: unknown ops, wrong schema_version, or out-of-order ops
// are hard errors before a single syscall happens.

import (
	"encoding/json"
	"fmt"
	"os"
)

type pkgPin struct {
	Version  string `json:"version"`
	Checksum string `json:"checksum"`
}

type metadata struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    int               `json:"generation"`
	Distro        string            `json:"distro"`
	Packages      map[string]pkgPin `json:"packages"`
	Timestamp     string            `json:"timestamp,omitempty"`
	ConfigHash    string            `json:"config_hash,omitempty"`
	KernelArgs    []string          `json:"kernel_args"`
}

type actionsFile struct {
	SchemaVersion int              `json:"schema_version"`
	Distro        string           `json:"distro"`
	Actions       []map[string]any `json:"actions"`
}

// canonical emission order per IR.md (op -> minimum rank; ops may repeat
// at the same rank, ranks never go backwards). #18 activation precedes
// #19-22 because those run inside the activated chroot.
var opRank = map[string]int{
	"copy_tree": 0,
	"configure_repos": 1, "partition_disk": 2, "format": 3, "create_swap": 4,
	"mount": 5, "write_file": 6, "install_packages": 7,
	"configure_flatpak_remote": 8, "install_flatpak": 9,
	"configure_locale": 10, "configure_keyboard": 11, "configure_timezone": 12,
	"load_kernel_modules": 13, "set_kernel_arguments": 14,
	"configure_service": 15, "configure_network": 16,
	"create_generation": 17, "activate_generation": 18,
	"create_user": 19, "enable_service": 20, "enable_home_service": 21,
	"install_bootloader": 22,
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func loadActions(path string) (*actionsFile, error) {
	var a actionsFile
	if err := loadJSON(path, &a); err != nil {
		return nil, err
	}
	if a.SchemaVersion != 1 {
		return nil, fmt.Errorf("%s: unsupported schema_version %d", path, a.SchemaVersion)
	}
	prev := 0
	for i, act := range a.Actions {
		op, _ := act["op"].(string)
		rank, ok := opRank[op]
		if !ok {
			return nil, fmt.Errorf("%s: action %d: unknown op %q", path, i, op)
		}
		if rank < prev {
			return nil, fmt.Errorf("%s: action %d: op %q out of order (rank %d after %d)",
				path, i, op, rank, prev)
		}
		prev = rank
	}
	return &a, nil
}

func loadMetadata(path string) (*metadata, error) {
	var m metadata
	if err := loadJSON(path, &m); err != nil {
		return nil, err
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("%s: unsupported schema_version %d", path, m.SchemaVersion)
	}
	return &m, nil
}

func field(act map[string]any, key string) string {
	s, _ := act[key].(string)
	return s
}

func strList(act map[string]any, key string) []string {
	var out []string
	if v, ok := act[key].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
