package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

// A release older than the data format would drop session ids on its first save.
func TestOlderReleasesDoNotReadCurrentData(t *testing.T) {
	for tag, want := range map[string]bool{"v0.8.10": false, "v0.7.453": false, "v0.9.0": true, "v0.9.12": true, "v1.0.0": true} {
		if got := readsCurrentData(tag); got != want {
			t.Fatalf("readsCurrentData(%s) = %v, want %v", tag, got, want)
		}
	}
}

type releaseDownloadProbe struct{ calls int }

func (p *releaseDownloadProbe) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls++
	return nil, errors.New("download probe")
}

func TestReleaseUpdateRejectsOlderDataFormatBeforeDownload(t *testing.T) {
	probe := &releaseDownloadProbe{}
	prior := downloadClient
	downloadClient = &http.Client{Transport: probe}
	t.Cleanup(func() { downloadClient = prior })
	for _, tag := range []string{"v0.8.10", "v0.7.453"} {
		res := performReleaseUpdate(context.Background(), tag, io.Discard)
		if res.OK || !strings.Contains(res.Message, "cannot read the current data") {
			t.Fatalf("performReleaseUpdate(%q) = %+v", tag, res)
		}
	}
	if probe.calls != 0 {
		t.Fatalf("older releases reached the download path: %d requests", probe.calls)
	}
	res := performReleaseUpdate(context.Background(), "v0.9.0", io.Discard)
	if probe.calls != 1 || !strings.Contains(res.Message, "download probe") {
		t.Fatalf("a supported release did not reach the download path: %+v, requests=%d", res, probe.calls)
	}
}

// releaseServer serves one release the way GitHub does: the binary and its checksums.txt.
func releaseServer(t *testing.T, binary []byte, checksums func(name string) string) {
	t.Helper()
	name := "klax-v9.9.9-" + runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+name):
			_, _ = w.Write(binary)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			if checksums == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(checksums(name)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	prior, priorRepo := releaseHost, repo
	releaseHost, repo = srv.URL, "owner/klax"
	t.Cleanup(func() { releaseHost, repo = prior, priorRepo })
}

func sumOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestDownloadVerifiesChecksum(t *testing.T) {
	binary := []byte("#!/bin/sh\necho ok\n")
	releaseServer(t, binary, func(name string) string { return sumOf(binary) + "  " + name + "\n" })
	path, err := downloadReleaseTo("v9.9.9", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	got, _ := os.ReadFile(path)
	if string(got) != string(binary) {
		t.Fatalf("downloaded content differs")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestDownloadRefusesUnverifiedBinaries(t *testing.T) {
	binary := []byte("real binary")
	cases := map[string]func(name string) string{
		"no checksums file":   nil,
		"wrong digest":        func(name string) string { return sumOf([]byte("something else")) + "  " + name + "\n" },
		"file not listed":     func(string) string { return sumOf(binary) + "  klax-other\n" },
		"malformed digest":    func(name string) string { return "zzzz  " + name + "\n" },
		"truncated digest":    func(name string) string { return sumOf(binary)[:20] + "  " + name + "\n" },
		"empty checksum list": func(string) string { return "" },
	}
	for name, checksums := range cases {
		t.Run(name, func(t *testing.T) {
			releaseServer(t, binary, checksums)
			path, err := downloadReleaseTo("v9.9.9", io.Discard)
			if err == nil {
				os.Remove(path)
				t.Fatal("an unverified binary must be refused")
			}
			if path != "" {
				t.Errorf("a path was returned alongside the error: %q", path)
			}
		})
	}
}

func TestDownloadLeavesNoTempFileOnMismatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	releaseServer(t, []byte("payload"), func(name string) string { return sumOf([]byte("other")) + "  " + name + "\n" })
	if _, err := downloadReleaseTo("v9.9.9", io.Discard); err == nil {
		t.Fatal("expected a checksum error")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestParseChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	list := sum + "  klax-v1-linux-amd64\n" + strings.ToUpper(sum) + " *klax-v1-darwin-arm64\n"
	if got, err := parseChecksum(list, "klax-v1-linux-amd64"); err != nil || got != sum {
		t.Errorf("text mode: %q %v", got, err)
	}
	if got, err := parseChecksum(list, "klax-v1-darwin-arm64"); err != nil || got != sum {
		t.Errorf("binary mode marker and upper case: %q %v", got, err)
	}
	if _, err := parseChecksum(list, "klax-v1-windows"); err == nil {
		t.Error("an unlisted file must be an error")
	}
}

func TestUpdateRepoDefaultsToTheFork(t *testing.T) {
	if repo != "voknil/klax" && os.Getenv("KLAX_UPDATE_REPO") == "" {
		t.Errorf("repo = %q, want the fork's own releases", repo)
	}
}
