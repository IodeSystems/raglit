package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/raglit/internal/update"
)

// releaseServer serves a signed manifest offering v0.9.0, whose "binary" is a
// shell script that exits with code.
func releaseServer(t *testing.T, code string) (*httptest.Server, update.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	key := update.PublicKey{ID: id, Key: pub}

	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nexit " + code + "\n"
	_ = tw.WriteHeader(&tar.Header{Name: "raglit", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(script))
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(tb.Bytes())

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m := update.Manifest{Schema: update.Schema, Version: "v0.9.0", Published: time.Now().UTC().Format(time.RFC3339),
		Artifacts: map[string]update.Artifact{"raglit": {Version: "v0.9.0", Files: map[string]update.File{
			update.Platform(runtime.GOOS, runtime.GOARCH): {URL: srv.URL + "/raglit.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(tb.Len())},
		}}}}
	body, _ := json.Marshal(m)
	sig := update.Sign(priv, id, body, "timestamp:1\tfile:raglit-manifest.json\thashed")
	mux.HandleFunc("/raglit-manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(body) })
	mux.HandleFunc("/raglit-manifest.json.minisig", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(sig)) })
	mux.HandleFunc("/raglit.tar.gz", func(w http.ResponseWriter, _ *http.Request) { w.Write(tb.Bytes()) })
	return srv, key, priv
}

func stageTest(t *testing.T, code string) (exe string, to string, err error) {
	t.Helper()
	srv, key, _ := releaseServer(t, code)
	dir := t.TempDir()
	exe = filepath.Join(dir, "raglit")
	if err := os.WriteFile(exe, []byte("the running build"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &update.Updater{ManifestURL: srv.URL + "/raglit-manifest.json", Name: "raglit", Keys: []update.PublicKey{key},
		Current: "v0.4.0", Exe: exe + ".new", Home: dir, Platform: update.Platform(runtime.GOOS, runtime.GOARCH),
		Fetch: &update.Fetcher{AllowHTTP: true}}
	to, err = stageRelease(u, exe, []string{"daemon"})
	return exe, to, err
}

// A newer release that starts with the daemon's flags replaces the binary.
func TestStageRelease_ReplacesTheBinaryAfterTheCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the staged build is a shell script")
	}
	exe, to, err := stageTest(t, "0")
	if err != nil || to != "v0.9.0" {
		t.Fatalf("stageRelease = %q, %v; want v0.9.0", to, err)
	}
	b, _ := os.ReadFile(exe)
	if !strings.Contains(string(b), "exit 0") {
		t.Fatalf("the binary was not replaced: %q", b)
	}
}

// A release that fails `daemon --check` is not installed: the running build
// stays the binary and the staged file is removed, so a restart cannot
// crash-loop on it.
func TestStageRelease_RefusesABuildThatFailsTheCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the staged build is a shell script")
	}
	exe, to, err := stageTest(t, "1")
	if err == nil || to != "" {
		t.Fatalf("stageRelease = %q, %v; want an error", to, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "the running build" {
		t.Fatalf("the binary changed: %q", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("the staged build was left behind: %v", err)
	}
}
