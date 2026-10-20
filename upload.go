package raglit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Uploaded documents.
//
// Ingest takes a PATH on the daemon host, which is right for a local daemon and
// wrong for a shared one: there it is a file read as the daemon's user, so with
// auth on it is the admin's. A remote client sends the bytes instead. They are
// written under the index's own home — <home>/uploads/<name> — and ingested from
// there like any file, so every later stage (pool, OCR, fragments, readings)
// treats an upload exactly as a file it found.
//
// The name is the client's identity for the document. Uploading the same name
// again replaces the file and re-ingests it; forgetting the path removes it from
// the index.

// UploadsDir is where uploaded documents live in a home.
func (h Home) UploadsDir() string { return filepath.Join(string(h), "uploads") }

// SaveUpload writes data as the upload called name and returns its absolute
// path. name is relative and slash-separated ("notes/2026/a.md"); anything that
// would land outside the uploads directory is refused.
func (s *Store) SaveUpload(name string, data []byte) (string, error) {
	if !s.withHome {
		return "", fmt.Errorf("raglit: upload: this index has no home to store files in")
	}
	rel, err := cleanUploadName(name)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(s.home.UploadsDir())
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	tmp := full + ".upload-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return full, nil
}

func cleanUploadName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("raglit: upload: name is required")
	}
	if strings.ContainsRune(n, 0) || strings.Contains(n, `\`) {
		return "", fmt.Errorf("raglit: upload: name %q has a forbidden character", name)
	}
	if strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("raglit: upload: name %q must be relative", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("raglit: upload: name %q has an empty, . or .. segment", name)
		}
	}
	if strings.HasSuffix(n, ".upload-tmp") {
		return "", fmt.Errorf("raglit: upload: name %q uses a reserved suffix", name)
	}
	return filepath.FromSlash(n), nil
}

// UploadURL is the queue/document identity of an upload saved at path.
func UploadURL(path string) string { return "file://" + filepath.ToSlash(path) }
