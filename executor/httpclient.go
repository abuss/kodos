package main

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net/http"
	"os"
	"time"
)

// A kodos-installed Arch target has no on-disk CA bundle (see rootCAs below),
// so a bundle compiled into the binary is the only guaranteed trust anchor.
//go:embed ca-bundle.pem
var bundledCAs []byte

// A static (CGO_ENABLED=0) kodos binary does not reliably find every distro's
// CA store: on Arch the bundle lives at /etc/ca-certificates/extracted/
// tls-ca-bundle.pem and the /etc/ssl/certs entries are symlinks the pure-Go
// loader skips, so x509.SystemCertPool() comes up empty and every HTTPS fetch
// dies with "certificate signed by unknown authority". We therefore build our
// own pool: the system pool plus well-known bundle paths, so apply works on the
// Arch target without an operator workaround. SSL_CERT_FILE overrides.
func rootCAs() *x509.CertPool {
	pool, _ := x509.SystemCertPool()
	if pool == nil {
		pool = x509.NewCertPool()
	}
	// Always load the embedded Mozilla roots first. Disk paths below are a
	// best-effort supplement (custom/operator bundles), never the sole source:
	// on Arch no on-disk bundle exists because update-ca-trust is a libalpm
	// hook kodos does not run, leaving every path below a dangling symlink.
	pool.AppendCertsFromPEM(bundledCAs)
	for _, p := range []string{
		os.Getenv("SSL_CERT_FILE"),
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/ca-certificates/extracted/tls-ca-bundle.pem",
	} {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			pool.AppendCertsFromPEM(b)
		}
	}
	return pool
}

// httpsClient is one HTTPS client with the cross-distro trust pool and a total
// request timeout. Used by every Arch-path fetch (sync DB + package).
func httpsClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: rootCAs()}
	return &http.Client{Timeout: timeout, Transport: tr}
}
