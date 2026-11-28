package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	coreDesc  = "%FILENAME%\nlinux-1.0-1-x86_64.pkg.tar.zst\n%NAME%\nlinux\n%VERSION%\n1.0-1\n%SHA256SUM%\nabc\n\n"
	extraDesc = "%FILENAME%\nsudo-1.0-1-x86_64.pkg.tar.zst\n%NAME%\nsudo\n%VERSION%\n1.0-1\n%SHA256SUM%\ndef\n\n"
)

func archDBTarGz(t *testing.T, members [][2]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{
			Name: m[0], Mode: 0o644, Size: int64(len(m[1])), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return gz.Bytes()
}

func TestFetchArchIndexes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/os/x86_64/core.db.tar.gz":
			w.Write(archDBTarGz(t, [][2]string{{"core/linux-1.0-1/desc", coreDesc}}))
			return
		case "/extra/os/x86_64/extra.db.tar.gz":
			w.Write(archDBTarGz(t, [][2]string{{"extra/sudo-1.0-1/desc", extraDesc}}))
			return
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	paths, err := fetchArchIndexes(srv.URL, []string{"core", "extra"}, "x86_64", tmp)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	wantNames := []string{"core.desc", "extra.desc"}
	if len(paths) != 2 || filepath.Base(paths[0]) != wantNames[0] || filepath.Base(paths[1]) != wantNames[1] {
		t.Fatalf("paths = %v, want core.desc then extra.desc", paths)
	}
	for _, tc := range []struct{ path, want string }{{paths[0], coreDesc}, {paths[1], extraDesc}} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Errorf("read %s: %v", tc.path, err)
			continue
		}
		if !strings.Contains(string(got), tc.want) {
			t.Errorf("%s content = %q, want to contain %q", filepath.Base(tc.path), got, tc.want)
		}
	}
}

func TestFetchArchIndexesFallsBackToPlainDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/core/os/x86_64/core.db" {
			w.Write(archDBTarGz(t, [][2]string{{"core/linux-1.0-1/desc", coreDesc}}))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	paths, err := fetchArchIndexes(srv.URL, []string{"core"}, "x86_64", tmp)
	if err != nil {
		t.Fatalf("fallback fetch: %v", err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "core.desc" {
		t.Fatalf("paths = %v, want core.desc", paths)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(got), coreDesc) {
		t.Errorf("fallback content = %q, err = %v", got, err)
	}
}

func TestFetchArchIndexesMissingDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	_, err := fetchArchIndexes(srv.URL, []string{"core"}, "x86_64", tmp)
	wantURL := srv.URL + "/core/os/x86_64/core.db.tar.gz"
	if err == nil || !strings.Contains(err.Error(), "core") || !strings.Contains(err.Error(), wantURL) {
		t.Fatalf("404 error %v must name branch core and %s", err, wantURL)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("404 fetch left %d files in tmp dir", len(ents))
	}
}

func TestFetchArchIndexesMalformedDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archDBTarGz(t, [][2]string{{"core/README", "not a desc"}}))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	_, err := fetchArchIndexes(srv.URL, []string{"core"}, "x86_64", tmp)
	wantURL := srv.URL + "/core/os/x86_64/core.db.tar.gz"
	if err == nil || !strings.Contains(err.Error(), "desc") || !strings.Contains(err.Error(), wantURL) {
		t.Fatalf("malformed DB error %v must name desc and %s", err, wantURL)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("malformed DB fetch left %d files in tmp dir", len(ents))
	}
}

func TestFetchArchIndexesTLSCertHint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archDBTarGz(t, [][2]string{{"core/linux-1.0-1/desc", coreDesc}}))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	_, err := fetchArchIndexes(srv.URL, []string{"core"}, "x86_64", tmp)
	if err == nil || !strings.Contains(err.Error(), "ca-certificates") {
		t.Fatalf("TLS error %v must mention ca-certificates", err)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("TLS fetch left %d files in tmp dir", len(ents))
	}
}

// local (file://) arch repos: mkarchrepo.sh layout <mirror>/<branch>/os/...
func TestFetchArchIndexesFileURL(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "kodos/os/x86_64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blob := archDBTarGz(t, [][2]string{{"kodos/desc", coreDesc}})
	if err := os.WriteFile(filepath.Join(dir, "kodos.db.tar.gz"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := fetchArchIndexes("file://"+root, []string{"kodos"}, "x86_64", t.TempDir())
	if err != nil {
		t.Fatalf("file:// arch repo: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 desc path, got %v", paths)
	}
}
