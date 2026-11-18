package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSystemHomeDirs(t *testing.T) {
	passwd := strings.Join([]string{
		"root:x:0:0:root:/root:/bin/sh",
		"cosmic-greeter:x:101:102:cosmic-greeter:/var/lib/cosmic-greeter:/sbin/nologin",
		"messagebus:x:82:82::/var/lib/dbus:/sbin/nologin",
		"abuss:x:1000:100:Antal Buss:/home/abuss:/bin/bash",
		"bad:x:oops:1::/var/lib/bad:/bin/sh",
		"# comment:x:1:1::/var/lib/comment:/bin/sh",
	}, "\n")
	got := systemHomeDirs(passwd)
	want := map[string][2]int{
		"/var/lib/cosmic-greeter": {101, 102},
		"/var/lib/dbus":           {82, 82},
	}
	if len(got) != len(want) {
		t.Fatalf("system home dirs: got %v, want %v", got, want)
	}
	for _, h := range got {
		ids, ok := want[h.path]
		if !ok || ids[0] != h.uid || ids[1] != h.gid {
			t.Errorf("unexpected home entry %+v (want %v)", h, want)
		}
	}
}

func TestEnsureSystemHomeDirs(t *testing.T) {
	gen := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gen, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid() // chown to self works unprivileged
	passwd := strings.Join([]string{
		"root:x:0:0:root:/root:/bin/sh",
		"cosmic-greeter:x:" + strconv.Itoa(uid) + ":" + strconv.Itoa(gid) +
			":cosmic-greeter:/var/lib/cosmic-greeter:/sbin/nologin",
		"abuss:x:1000:100:Antal Buss:/home/abuss:/bin/bash",
	}, "\n")
	if err := os.WriteFile(filepath.Join(gen, "etc", "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSystemHomeDirs(gen); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(gen, "var", "lib", "cosmic-greeter")
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("greeter home missing after generation assembly: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("greeter home is not a directory: %v", st.Mode())
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok && (s.Uid != uint32(uid) || s.Gid != uint32(gid)) {
		t.Errorf("greeter home owner %d:%d, want %d:%d", s.Uid, s.Gid, uid, gid)
	}
	if _, err := os.Stat(filepath.Join(gen, "home", "abuss")); !os.IsNotExist(err) {
		t.Errorf("config user home under /home must not be created in the generation: %v", err)
	}
}
