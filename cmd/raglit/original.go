package main

import (
	"mime"
	"net/http"
	"path/filepath"

	"github.com/iodesystems/raglit"
)

// The original bytes of a document that lives in raglit's own storage — an
// extracted attachment or an upload.
//
// Corpus documents are downloaded through the review mount (attest's source
// route), which is bounded by the corpus root. Storage documents sit outside
// that root on purpose (docpaths.go), so they get this route instead, bounded
// by the index's own home: the path must be a document OF the index, and its
// bytes must be under the index's home. Nothing else on the host is reachable.
func originalHandler(reg *raglit.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := reg.Read(r.Context(), r.URL.Query().Get("index"))
		if err != nil {
			http.Error(w, "no such index", http.StatusNotFound)
			return
		}
		// docPathMiddleware has already turned a display path into the indexed one.
		path := r.URL.Query().Get("path")
		if !indexHolds(st, path) {
			http.Error(w, "no such document in this index", http.StatusNotFound)
			return
		}
		full, ok := st.StoragePath(path)
		if !ok {
			http.Error(w, "this document is not in raglit's storage; download it from the review workbench", http.StatusNotFound)
			return
		}
		name := filepath.Base(full)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, full)
	}
}
