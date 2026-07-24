package raglit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// ProjectHomeName is the per-project home directory `raglit init` creates in the
// working directory. Commands discover it by walking up from the cwd, so raglit
// is project-local like git: one .raglit/ per project, found from any subdir.
const ProjectHomeName = ".raglit"

// DefaultRoot is the DAEMON's storage root — $RAGLIT_ROOT, else ~/.raglit — under
// which each index is scoped at indexes/<name>/ (its own Home: index.sqlite +
// originals/ + pages/). This is the multi-index server layout (OpenScopedRegistry),
// distinct from a project Home (a single embedded index). The daemon's own config
// (endpoint + models) lives at <root>/config.json.
func DefaultRoot() string {
	if r := os.Getenv("RAGLIT_ROOT"); r != "" {
		return r
	}
	if u, err := os.UserHomeDir(); err == nil {
		return filepath.Join(u, ".raglit")
	}
	return ".raglit-root"
}

// DiscoverHome resolves the home for a command given no explicit --home: the
// nearest ancestor .raglit/ (walking up from the cwd), else DefaultHome()
// ($RAGLIT_HOME, else ~/local/raglit). `raglit init` instead writes ./.raglit
// unconditionally, so a fresh project always gets its own local home.
func DiscoverHome() Home {
	if h, ok := findProjectHome(); ok {
		return h
	}
	return DefaultHome()
}

// findProjectHome walks up from the working directory looking for a directory
// named ProjectHomeName, stopping at the filesystem root.
func findProjectHome() (Home, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		cand := filepath.Join(dir, ProjectHomeName)
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return Home(cand), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// IndexPath is the home's primary (default) SQLite index file.
func (h Home) IndexPath() string { return filepath.Join(string(h), "index.sqlite") }

// indexPath is the sqlite path for a named index within the home: "default"
// (or "") → index.sqlite; any other name → index-<name>.sqlite.
func (h Home) indexPath(name string) string {
	if n := normalizeIndexName(name); n != "default" {
		return filepath.Join(string(h), "index-"+n+".sqlite")
	}
	return h.IndexPath()
}

// normalizeIndexName lowercases and strips a name to [a-z0-9_-], so a name from
// an MCP tool argument can't traverse the filesystem. Empty → "default".
func normalizeIndexName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

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
