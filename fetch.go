package raglit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Fetched is the raw content behind an ingest URL, plus enough to route it.
type Fetched struct {
	Data        []byte
	Title       string // basename, for the document title
	IsPDF       bool   // route to OCR vs plain-text fragmenting
	ContentType string // HTTP Content-Type, for format routing (empty for local files)
}

// maxFetchBytes caps a single fetch so a runaway URL can't exhaust memory.
const maxFetchBytes = 64 << 20 // 64 MiB

// Fetch resolves an ingest URL to bytes. Supported schemes:
//   - file://<path>   a local file
//   - http(s)://...   a remote GET
//   - (no scheme)     treated as a local filesystem path
//
// PDF is detected by a .pdf extension or an application/pdf content type.
func Fetch(ctx context.Context, rawURL string) (Fetched, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Fetched{}, fmt.Errorf("raglit: bad url %q: %w", rawURL, err)
	}
	switch u.Scheme {
	case "http", "https":
		return fetchHTTP(ctx, rawURL)
	case "file":
		return fetchFile(fileURLPath(u))
	case "":
		return fetchFile(rawURL) // bare local path
	default:
		return Fetched{}, fmt.Errorf("raglit: unsupported url scheme %q (use file:// or http(s)://)", u.Scheme)
	}
}

// fileURLPath extracts a filesystem path from a file:// URL, tolerating both
// file:///abs/path and file://host/path (host ignored) and file://rel.
func fileURLPath(u *url.URL) string {
	if u.Path != "" {
		return u.Path
	}
	return u.Opaque // file:relative
}

func fetchFile(path string) (Fetched, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{
		Data:  data,
		Title: filepath.Base(path),
		IsPDF: strings.EqualFold(filepath.Ext(path), ".pdf"),
	}, nil
}

func fetchHTTP(ctx context.Context, rawURL string) (Fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Fetched{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Fetched{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Fetched{}, fmt.Errorf("raglit: fetch %s: status %d", rawURL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return Fetched{}, err
	}
	ct := resp.Header.Get("Content-Type")
	u, _ := url.Parse(rawURL)
	title := filepath.Base(u.Path)
	if title == "" || title == "." || title == "/" {
		title = rawURL
	}
	return Fetched{
		Data:        data,
		Title:       title,
		IsPDF:       strings.Contains(ct, "application/pdf") || strings.EqualFold(filepath.Ext(u.Path), ".pdf"),
		ContentType: ct,
	}, nil
}
