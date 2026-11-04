package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Installing a download: it goes to a temp file beside its target (so the
// last step is a rename on one filesystem), its size and sha256 are checked
// against the manifest, the one binary is copied out of the tar.gz into a
// second temp file — the archive's names are only compared, never used as a
// path — made executable, synced, and renamed over the target. Nothing runs
// the file before the checks pass, and a failure leaves the target as it was.

// MaxBinary caps the binary read out of an archive.
const MaxBinary = 512 << 20

// Fetcher downloads. AllowHTTP is for tests (httptest serves plain HTTP).
type Fetcher struct {
	Client    *http.Client
	AllowHTTP bool
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// get opens u for reading, after checking its scheme.
func (f *Fetcher) get(ctx context.Context, u string) (io.ReadCloser, error) {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && !(f.AllowHTTP && p.Scheme == "http")) {
		return nil, &VerifyError{fmt.Sprintf("refusing a download that is not https: %q", u)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return resp.Body, nil
}

// Small reads a small document (a manifest, a signature), at most max bytes.
func (f *Fetcher) Small(ctx context.Context, u string, max int64) ([]byte, error) {
	body, err := f.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, &VerifyError{fmt.Sprintf("%s is larger than %d bytes", u, max)}
	}
	return b, nil
}

// Install downloads file, checks it, and puts the archive's binary named
// entry at dst (mode 0755). It returns dst.
func (f *Fetcher) Install(ctx context.Context, file File, entry, dst string) (string, error) {
	if err := file.check(); err != nil {
		return "", &VerifyError{err.Error()}
	}
	dir := filepath.Dir(dst)
	archive, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".download-*")
	if err != nil {
		return "", fmt.Errorf("cannot write beside %s: %w", dst, err)
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()
	body, err := f.get(ctx, file.URL)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, h), io.LimitReader(body, file.Size+1))
	body.Close()
	if err != nil {
		return "", fmt.Errorf("download %s: %w", file.URL, err)
	}
	if n != file.Size {
		return "", &VerifyError{fmt.Sprintf("%s: %d bytes, the manifest says %d", file.URL, n, file.Size)}
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, file.SHA256) {
		return "", &VerifyError{fmt.Sprintf("%s: sha256 %s, the manifest says %s", file.URL, got, file.SHA256)}
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := extract(archive, entry, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// extract copies the regular file named entry (at the archive's top, or in
// one directory) out of the tar.gz r, to dst.
func extract(r io.Reader, entry, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return &VerifyError{"not a tar.gz: " + err.Error()}
	}
	tr := tar.NewReader(gz)
	var out *os.File
	defer func() {
		if out != nil {
			out.Close()
			os.Remove(out.Name())
		}
	}()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return &VerifyError{"a broken archive: " + err.Error()}
		}
		name := hdr.Name
		if path.IsAbs(name) || strings.Contains(name, "\\") || hasDotDot(name) {
			return &VerifyError{fmt.Sprintf("the archive names a path outside itself: %q", name)}
		}
		clean := path.Clean(name)
		if path.Base(clean) != entry || strings.Count(clean, "/") > 1 {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return &VerifyError{fmt.Sprintf("the archive's %s is not a regular file", name)}
		}
		if out != nil {
			return &VerifyError{"the archive holds " + entry + " twice"}
		}
		if hdr.Size <= 0 || hdr.Size > MaxBinary {
			return &VerifyError{fmt.Sprintf("the archive's %s is %d bytes", name, hdr.Size)}
		}
		out, err = os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".new-*")
		if err != nil {
			return fmt.Errorf("cannot write beside %s: %w", dst, err)
		}
		if n, err := io.Copy(out, io.LimitReader(tr, hdr.Size)); err != nil || n != hdr.Size {
			return &VerifyError{fmt.Sprintf("the archive's %s is cut short", name)}
		}
	}
	if out == nil {
		return &VerifyError{"the archive has no " + entry}
	}
	if err := out.Chmod(0o755); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), dst); err != nil {
		return fmt.Errorf("put %s in place: %w", dst, err)
	}
	out = nil
	if d, err := os.Open(filepath.Dir(dst)); err == nil { // the rename survives a crash
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func hasDotDot(name string) bool {
	for _, p := range strings.Split(name, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}
