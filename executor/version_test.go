package main

import (
	"strings"
	"testing"
)

// `kodos version` must self-identify on sticks/targets where the binary
// cannot be eyeballed against the repo: revision + build (commit) time.
func TestVersionString(t *testing.T) {
	v := versionString()
	for _, want := range []string{"kodos ", "built from ", "go1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("version %q missing %q", v, want)
		}
	}
}
