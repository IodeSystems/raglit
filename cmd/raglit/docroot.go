package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iodesystems/raglit"
)

// Root-relative document paths.
//
// The index stores ABSOLUTE paths, and the UI showed them: /home/<user>/life/...
// in front of every document, in the list, in search hits, in the document
// header and in the URL bar. A person reading a corpus wants
// "records/2010-westview-survey.pdf"; the home directory of the machine the
// daemon runs on is nobody's business on a page that gets screen-shared.
//
// So every document has a DISPLAY path, relative to the index root, and every
// endpoint that takes a document path accepts that display path back. The
// absolute path stays the identity in the store — nothing about storage
// changes; this is a spelling at the API boundary.
//
// Documents in raglit's OWN storage (extracted attachments, uploads) are not in
// the corpus tree and are not under the root. They are spelled
// "@<home-relative path>" — "@attachments/f0ce0ae7-x.eml/p01-03-order.docx" —
// so the two kinds cannot collide and the spelling says which one it is.

// storagePrefix marks a display path that is relative to the index's own
// storage rather than to the corpus root.
const storagePrefix = "@"

// docView is one index's paths as the UI spells them.
type docView struct {
	st    *raglit.Store
	Root  string
	paths []string
}

// indexView reads an index's document paths once and derives its root.
//
// The root is the deepest directory containing every CORPUS document — the
// documents in raglit's own storage are left out of it. Counting them is what
// widened one corpus's root to the user's home directory: 585 documents under
// ~/life/projects/<case>/ and 72 extracted attachments under
// ~/.raglit/indexes/<case>/ share only /home/<user>. Everything keyed on the
// root moved with it — the review mount walked the whole home directory on every
// listing (six minutes on the 657-document copy) and every path the UI showed
// began with /home/<user>/.
func indexView(st *raglit.Store) (docView, error) {
	paths, err := st.DocumentPaths()
	if err != nil {
		return docView{}, err
	}
	v := docView{st: st, paths: paths}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			continue
		}
		if _, ok := st.StorageRel(p); ok {
			continue
		}
		dir := filepath.Dir(p)
		if v.Root == "" {
			v.Root = dir
			continue
		}
		v.Root = commonDir(v.Root, dir)
	}
	return v, nil
}

// indexRoot is the deepest directory containing every corpus document in an
// index — see indexView.
//
// Derived rather than configured, because nothing declares it: an index is a set
// of ingested paths and the tree they share is a fact about them. Deepest common
// ancestor, so the root is as tight as the corpus allows — an index whose
// documents all live under one project bounds a review to that project, and only
// an index spanning unrelated trees widens to their common parent.
func indexRoot(st *raglit.Store) (string, error) {
	v, err := indexView(st)
	return v.Root, err
}

// rel is a document's display path. A path that is neither under the root nor
// in raglit's storage (a URL, a relative path from an old ingest) is returned
// as it is: there is nothing to shorten it against.
func (v docView) rel(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	if r, ok := v.st.StorageRel(p); ok {
		return storagePrefix + filepath.ToSlash(r)
	}
	if v.Root != "" && isUnder(p, v.Root) {
		if r, err := filepath.Rel(v.Root, p); err == nil && r != "." {
			return filepath.ToSlash(r)
		}
	}
	return p
}

// asset is a document's handle in the attest mount, which is rooted at the
// index root: the display path for a corpus document, "" for one in raglit's
// storage, which is outside the review mount's boundary.
func (v docView) asset(p string) string {
	r := v.rel(p)
	if r == p && filepath.IsAbs(p) || strings.HasPrefix(r, storagePrefix) {
		return ""
	}
	return r
}

// resolve maps a display path back to the indexed path. ok is false when no
// document of this index has that display path; an absolute path is returned
// unchanged, because that is already the identity.
//
// A LOOKUP, not a join with the root. Joining would accept a path to a file the
// index does not hold and hand it to an operation as though it did; it would
// also get storage paths wrong for a home that was copied, whose rows still name
// the original. Matching against the index's own paths can only ever produce a
// path the index already has.
func (v docView) resolve(p string) (string, bool) {
	if filepath.IsAbs(p) {
		return p, true
	}
	if slices.Contains(v.paths, p) {
		return p, true
	}
	for _, abs := range v.paths {
		if v.rel(abs) == p {
			return abs, true
		}
	}
	return "", false
}

// escapes reports whether a relative path climbs: "..", "a/../../b". Refused
// outright rather than cleaned — a display path never contains one, so a
// request that does is not a spelling of any document.
func escapes(p string) bool {
	if filepath.IsAbs(p) {
		return false
	}
	return slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

// docPathQueryRoutes are the operations whose `path` query parameter names ONE
// document of the index in `index`. An allowlist, deliberately: /search takes a
// path PREFIX, /api/ocr a file on the daemon host, /api/upload a name to store
// under — rewriting any of those would change what they mean.
var docPathQueryRoutes = map[string]bool{
	"/api/doc": true, "/api/doc-detail": true, "/api/get-document": true,
	"/api/email": true, "/api/page-layout": true, "/api/page-image": true,
	"/api/fields": true, "/api/notes": true,
	"/api/identify": true, "/api/identify/queue": true,
	"/api/reread": true, "/api/restore": true, "/api/withdraw": true, "/api/forget": true,
}

// docPathBodyRoutes take the document as a top-level `path` in a JSON body.
var docPathBodyRoutes = map[string]bool{"/api/reocr": true}

// maxRewriteBody bounds the body read for a rewrite. /api/reocr's body is a
// path and a page number; anything larger is passed through untouched.
const maxRewriteBody = 64 << 10

// docPathMiddleware turns a display path in a request back into the indexed
// path before the operation sees it.
//
// One place rather than a call in each of fifteen operations, because the
// failure of missing one is silent: the operation looks the relative path up,
// finds nothing, and answers "no such document" for a document that is right
// there. A path that resolves to nothing is passed through as it came — several
// of these accept a filename substring and must keep doing so — and a path that
// climbs with ".." is refused.
func docPathMiddleware(reg *raglit.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if docPathQueryRoutes[r.URL.Path] {
				if p := q.Get("path"); p != "" && !filepath.IsAbs(p) {
					if escapes(p) {
						http.Error(w, "document path must not contain ..", http.StatusBadRequest)
						return
					}
					if abs, ok := resolveDocPath(r, reg, q.Get("index"), p); ok {
						q.Set("path", abs)
						r.URL.RawQuery = q.Encode()
					}
				}
			}
			if docPathBodyRoutes[r.URL.Path] && r.Body != nil && r.ContentLength <= maxRewriteBody {
				b, err := io.ReadAll(io.LimitReader(r.Body, maxRewriteBody+1))
				_ = r.Body.Close()
				if err == nil && len(b) <= maxRewriteBody {
					b = rewriteBodyPath(r, reg, q.Get("index"), b)
				}
				if escapesBody(b) {
					http.Error(w, "document path must not contain ..", http.StatusBadRequest)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(b))
				r.ContentLength = int64(len(b))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func resolveDocPath(r *http.Request, reg *raglit.Registry, index, p string) (string, bool) {
	st, err := reg.Read(r.Context(), index)
	if err != nil {
		return "", false // the operation itself reports the index error
	}
	v, err := indexView(st)
	if err != nil {
		return "", false
	}
	return v.resolve(p)
}

// rewriteBodyPath resolves a top-level "path" in a JSON object body, leaving
// every other byte of a body it cannot parse alone.
func rewriteBodyPath(r *http.Request, reg *raglit.Registry, index string, b []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	var p string
	if raw, ok := m["path"]; !ok || json.Unmarshal(raw, &p) != nil || p == "" || filepath.IsAbs(p) || escapes(p) {
		return b
	}
	abs, ok := resolveDocPath(r, reg, index, p)
	if !ok {
		return b
	}
	enc, _ := json.Marshal(abs)
	m["path"] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return b
	}
	return out
}

func escapesBody(b []byte) bool {
	var m struct {
		Path string `json:"path"`
	}
	return json.Unmarshal(b, &m) == nil && escapes(m.Path)
}
