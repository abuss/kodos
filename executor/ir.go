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
	Repo     string `json:"repo,omitempty"` // ALPM section (core/extra); apk omits it
}

// checkDistro: this build implements the alpine/apk and arch/pacman-family
// store models (milestone 1: arch bootstrap). Everything else fails closed.
func checkDistro(af *actionsFile, meta *metadata) error {
	switch af.Distro {
	case "alpine", "arch":
	default:
		return fmt.Errorf("this build implements alpine/apk and arch store models only (got distro %q)", af.Distro)
	}
	if meta.Distro != af.Distro || meta.Generation < 0 {
		return fmt.Errorf("actions and metadata disagree (distro/generation)")
	}
	return nil
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
// at the same rank, ranks never go backwards). copy_tree + configure_repos
// sit AFTER the mounts: they write into the target root, so a bootstrap
// stream that emitted them earlier would have their output covered by the
// root mount. #19 activation precedes #20-23 because those run inside the
// activated chroot.
var opRank = map[string]int{
	"partition_disk": 1, "format": 2, "create_swap": 3, "mount": 4,
	"copy_tree": 5, "configure_repos": 6, "write_file": 7,
	"install_packages":         8,
	"configure_flatpak_remote": 9, "install_flatpak": 10,
	"configure_locale": 11, "configure_keyboard": 12, "configure_timezone": 13,
	"load_kernel_modules": 14, "set_kernel_arguments": 15,
	"configure_service": 16, "configure_network": 17,
	"create_generation": 18, "activate_generation": 19,
	"create_user": 20, "enable_service": 21, "enable_home_service": 22,
	"install_bootloader": 23,
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
