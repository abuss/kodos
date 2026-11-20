package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// httpsClient must trust a CA bundle named by SSL_CERT_FILE even when the
// default static-Go system pool would miss it (the Arch-target failure this
// exists for). httptest's self-signed cert stands in for "a root the system
// pool does not already contain".
func TestHTTPSClientTrustsSSLCertFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// (a) No explicit bundle -> the self-signed cert is untrusted (proves the
	// success below comes from our RootCAs, not the ambient pool).
	os.Unsetenv("SSL_CERT_FILE")
	if resp, err := httpsClient(10 * time.Second).Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected self-signed TLS to FAIL with no SSL_CERT_FILE")
	}

	// (b) SSL_CERT_FILE -> the server's own cert becomes a trusted root.
	certPath := filepath.Join(t.TempDir(), "bundle.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(block), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", certPath)

	resp, err := httpsClient(10 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("with SSL_CERT_FILE set, fetch failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// rootCAs must never return nil (AppendCertsFromPEM target), even with no
// SSL_CERT_FILE and whatever ambient system pool exists.
func TestRootCAsNeverNil(t *testing.T) {
	os.Unsetenv("SSL_CERT_FILE")
	if pool := rootCAs(); pool == nil {
		t.Fatal("rootCAs() returned nil")
	}
}
