package main

// homedirs.go — package hooks create system users' homes only on first
// install: Alpine's `adduser -S -D -h /var/lib/<svc>` no-ops (2>/dev/null)
// when the user already exists in the seeded /etc/passwd of a later
// generation. Since /var is generation-scoped, the home silently disappears
// on `kodos apply` and greetd's chdir into /var/lib/cosmic-greeter fails with
// "unable to set working directory". Re-assert the passwd contract on every
// generation after the hook sweep.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type systemHome struct {
	path string
	uid  int
	gid  int
}

// systemHomeDirs extracts /var/lib/<svc> homes from a passwd file. Other
// homes (/root, /home/<user>, /var/empty...) are deliberately not ours to
// create: they belong to the live root or are intentionally non-existent.
func systemHomeDirs(passwd string) []systemHome {
	var homes []systemHome
	for _, line := range strings.Split(passwd, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 6 {
			continue
		}
		home := f[5]
		if !strings.HasPrefix(home, "/var/lib/") || strings.Contains(home, "..") {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		homes = append(homes, systemHome{path: home, uid: uid, gid: gid})
	}
	return homes
}

// ensureSystemHomeDirs recreates missing system-user homes in a freshly
// assembled generation tree. Called after package hooks have populated
// <genDir>/etc/passwd; existing directories keep their hook-set mode.
func ensureSystemHomeDirs(genDir string) error {
	b, err := os.ReadFile(filepath.Join(genDir, "etc", "passwd"))
	if os.IsNotExist(err) { // synthetic hook-sweep fixtures need no users
		return nil
	}
	if err != nil {
		return err
	}
	for _, h := range systemHomeDirs(string(b)) {
		dir := filepath.Join(genDir, filepath.FromSlash(strings.TrimPrefix(h.path, "/")))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.Chown(dir, h.uid, h.gid); err != nil {
			return err
		}
	}
	return nil
}
