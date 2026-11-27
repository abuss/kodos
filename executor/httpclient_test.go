package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// archive.archlinux.org handshakes flake (odin, 2026-11-27): one transport
// failure must be retried once, and an HTTP status (404 pin drift) must NOT.
func TestGetRetryOnce(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 1 {
			c, _, err := w.(http.Hijacker).Hijack() // dead connection
			if err == nil {
				c.Close()
			}
			return
		}
		io.WriteString(w, "pkg")
	}))
	defer srv.Close()
	resp, err := getRetry(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("transport flake must survive one retry: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "pkg" || hits != 2 {
		t.Fatalf("want attempt-2 success, got status %d body %q hits %d", resp.StatusCode, b, hits)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.NotFound(w, nil)
	}))
	defer srv2.Close()
	if resp, err := getRetry(srv2.Client(), srv2.URL); err != nil || resp.StatusCode != 404 {
		t.Fatalf("404 must pass through unwrapped: %v %v", resp.StatusCode, err)
	}
}
