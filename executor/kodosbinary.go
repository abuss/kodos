package main

import (
	"os"
	"path/filepath"
)

// carryUnmanagedKodos copies the active /usr/bin/kodos into a freshly
// assembled generation when no package provides it. Arch has no local kodos
// repo / kodos.apk: arch-run-install.sh installs the static binary as an
// unmanaged file in generation 0, while create_generation rebuilds generation
// N+1 purely from package-store contents. Without this the new generation
// boots without kodos and self-management dies. Alpine's kodos.apk already
// ships the file, so the existence guard leaves package-provided binaries
// (and their store-backed hardlinks) untouched.
func (e *executor) carryUnmanagedKodos(genDir string) error {
	dst := filepath.Join(genDir, "usr", "bin", "kodos")
	if lexists(dst) {
		return nil
	}
	// /usr is a chroot-rooted symlink to the active generation; resolve it
	// the way the kernel would so this also works under -root <fixture>.
	src := e.s.chrootResolve(filepath.Join(e.s.chroot, "usr", "bin", "kodos"))
	b, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
