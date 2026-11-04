// mkmanifest writes a release's raglit-manifest.json (make release-manifest):
// one raglit entry per archive in -dist (raglit_<os>_<arch>.tar.gz, sha256 and
// size computed here, URL -url/<archive>), and key_next from -key-next when
// that file exists. It signs nothing: make release-manifest runs the minisign
// CLI on its output.
//
// Copied from dun's tools/mkmanifest, without dun's pinned tools.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/raglit/internal/update"
)

func main() {
	version := flag.String("version", "", "the release version, vX.Y.Z")
	dist := flag.String("dist", "", "the directory holding raglit_<os>_<arch>.tar.gz")
	base := flag.String("url", "", "the release's download URL; each archive is <url>/<archive>")
	keyNext := flag.String("key-next", "key_next.json", "a next-key endorsement {key, signature}, when the file exists")
	summary := flag.String("summary", "", "one line on what changed")
	out := flag.String("o", "", "where to write the manifest")
	flag.Parse()
	if err := run(*version, *dist, *base, *keyNext, *summary, *out); err != nil {
		fmt.Fprintln(os.Stderr, "mkmanifest:", err)
		os.Exit(1)
	}
}

func run(version, dist, base, keyNext, summary, out string) error {
	if _, err := update.ParseVersion(version); err != nil {
		return err
	}
	if dist == "" || base == "" || out == "" {
		return errors.New("-dist, -url and -o are required")
	}
	m := update.Manifest{
		Schema:    update.Schema,
		Version:   version,
		Summary:   summary,
		Published: time.Now().UTC().Format(time.RFC3339),
		Artifacts: map[string]update.Artifact{},
	}
	art := update.Artifact{Version: version, Files: map[string]update.File{}}
	archives, _ := filepath.Glob(filepath.Join(dist, "raglit_*_*.tar.gz"))
	if len(archives) == 0 {
		return fmt.Errorf("no raglit_<os>_<arch>.tar.gz in %s", dist)
	}
	for _, a := range archives {
		name := filepath.Base(a)
		plat := strings.TrimSuffix(strings.TrimPrefix(name, "raglit_"), ".tar.gz")
		sum, size, err := hashFile(a)
		if err != nil {
			return err
		}
		art.Files[plat] = update.File{URL: strings.TrimSuffix(base, "/") + "/" + name, SHA256: sum, Size: size}
	}
	m.Artifacts["raglit"] = art
	if kb, err := os.ReadFile(keyNext); err == nil {
		var e update.Endorsement
		if err := json.Unmarshal(kb, &e); err != nil {
			return fmt.Errorf("%s: %w", keyNext, err)
		}
		m.KeyNext = &e
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if _, err := update.ParseManifest(body); err != nil { // what clients will read
		return err
	}
	return os.WriteFile(out, append(body, '\n'), 0o644)
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
