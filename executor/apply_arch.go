package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// apply_arch.go — Arch self-management index fetching. Alpine apply fetches
// APKINDEX tarballs; Arch apply fetches ALPM sync DBs and hands lockfile.lua
// the extracted desc text it already understands.

func archDBURLs(mirror, branch, dirArch string) []string {
	return []string{
		fmt.Sprintf("%s/%s/os/%s/%s.db.tar.gz", mirror, branch, dirArch, branch),
		fmt.Sprintf("%s/%s/os/%s/%s.db", mirror, branch, dirArch, branch),
	}
}

func extractArchDesc(blob []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("not a gzip Arch DB tarball: %w", err)
	}
	defer gz.Close()

	var out bytes.Buffer
	found := false
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Arch DB tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, "/desc") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		out.Write(data)
		found = true
	}
	if !found || out.Len() == 0 {
		return nil, fmt.Errorf("no desc member in Arch DB tarball")
	}
	return out.Bytes(), nil
}

func fetchArchIndexes(mirror string, branches []string, dirArch string, tmp string) ([]string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	descs := make([][]byte, 0, len(branches))
	for _, branch := range branches {
		urls := archDBURLs(mirror, branch, dirArch)
		tried := make([]string, 0, len(urls))
		var lastErr error
		var desc []byte

		for _, url := range urls {
			tried = append(tried, url)
			resp, err := client.Get(url)
			if err != nil {
				lastErr = archFetchError(branch, url, err)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				lastErr = archFetchError(branch, url, fmt.Errorf("HTTP %d", resp.StatusCode))
				continue
			}
			blob, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, archFetchError(branch, url, fmt.Errorf("download: %w", err))
			}
			desc, err = extractArchDesc(blob)
			if err != nil {
				return nil, archFetchError(branch, url, err)
			}
			break
		}

		if desc == nil {
			if lastErr == nil {
				lastErr = fmt.Errorf("fetch arch index %s: no URLs tried", branch)
			}
			return nil, fmt.Errorf("%w (tried: %s)", lastErr, strings.Join(tried, ", "))
		}
		descs = append(descs, desc)
	}

	var paths []string
	for i, branch := range branches {
		dst := filepath.Join(tmp, branch+".desc")
		if err := os.WriteFile(dst, descs[i], 0o600); err != nil {
			for _, p := range paths {
				os.Remove(p)
			}
			return nil, fmt.Errorf("write %s: %w", dst, err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}

func archFetchError(branch, url string, err error) error {
	if err == nil {
		return nil
	}
	hint := ""
	if strings.Contains(err.Error(), "certificate signed by unknown authority") {
		hint = " (target likely missing ca-certificates)"
	}
	return fmt.Errorf("fetch arch index %s %s: %w%s", branch, url, err, hint)
}
