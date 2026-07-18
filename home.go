package raglit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Home is raglit's on-disk layout: everything for one index under one
// directory, so the whole thing is a single portable unit you can copy, back
// up, or delete wholesale.
//
//	<home>/
//	  index.sqlite   the FTS5 index (documents:pages:fragments)
//	  originals/     ingested source files, kept so the index is self-contained
//	  pages/         rasterized/extracted page images for OCR (filled by Slice C)
//
// Default location: $RAGLIT_HOME, else ~/local/raglit. Override per-invocation
// with the CLI --home flag.
type Home string

// DefaultHome resolves the default index home: $RAGLIT_HOME if set, else
// ~/local/raglit. Falls back to ./raglit-home if the user home is undiscoverable.
func DefaultHome() Home {
	if h := os.Getenv("RAGLIT_HOME"); h != "" {
		return Home(h)
	}
	if u, err := os.UserHomeDir(); err == nil {
		return Home(filepath.Join(u, "local", "raglit"))
	}
	return Home("raglit-home")
}

// IndexPath is the SQLite index file inside the home.
func (h Home) IndexPath() string { return filepath.Join(string(h), "index.sqlite") }

// ConfigPath is the model-connection config (base URL, token, model ids).
func (h Home) ConfigPath() string { return filepath.Join(string(h), "config.json") }

// OriginalsDir holds copies of ingested source files.
func (h Home) OriginalsDir() string { return filepath.Join(string(h), "originals") }

// PagesDir holds page images derived from originals (OCR input).
func (h Home) PagesDir() string { return filepath.Join(string(h), "pages") }

// Ensure creates the home layout (home + originals/ + pages/) if absent.
func (h Home) Ensure() error {
	for _, d := range []string{string(h), h.OriginalsDir(), h.PagesDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("raglit: create %s: %w", d, err)
		}
	}
	return nil
}

// OriginalPath is where the original for a given document path is stored — a
// deterministic function of the doc path (hash-prefixed to avoid basename
// collisions across directories), so no DB column is needed to find it back.
func (h Home) OriginalPath(docPath string) string {
	return filepath.Join(h.OriginalsDir(), tag(docPath))
}

// PageDir is the per-document subdirectory of pages/ that holds its page
// images. Deterministic from the doc path, like OriginalPath.
func (h Home) PageDir(docPath string) string {
	return filepath.Join(h.PagesDir(), tag(docPath))
}

// tag builds a collision-safe, deterministic name from a path: an 8-hex prefix
// of its sha256 plus the basename.
func tag(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:4]) + "-" + filepath.Base(path)
}
