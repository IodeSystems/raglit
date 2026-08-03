package main

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/iodesystems/raglit/web"
)

// apiPrefixes are the paths the DAEMON owns. Everything else under / is a route
// belonging to the page.
//
// This list is the whole reason the old page kept its routes in the hash. Its
// comment said so: path routing "would need the daemon to catch-all every
// unknown URL — which would swallow the attest mount and every API route with
// one typo." That is the failure this guards. A mistyped /api/documnets must
// come back 404, not 200 with a page of HTML — a client that asked for JSON and
// got a document reports a parse error, and whoever chases it goes looking in
// the JSON encoder rather than at their own URL.
//
// So the catch-all is a DENY list, not a bare wildcard: anything under these
// prefixes is the API's to answer or to refuse, and only paths outside them
// become the SPA. Kept in step with API_PREFIXES in web/vite.config.ts — a path
// the dev proxy forwards but this list omits is a route that works in `npm run
// dev` and 404s in the binary.
var apiPrefixes = []string{
	"/api/",
	"/status",
	"/indexes",
	"/search",
	"/search-figures",
	"/ingest",
	"/openapi.json",
	"/openapi.yaml",
	"/docs",
	"/schemas/",
	"/attest/",
}

// isAPIPath reports whether the daemon, rather than the page, owns this path.
func isAPIPath(p string) bool {
	for _, pre := range apiPrefixes {
		if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// spaHandler serves the built bundle, falling back to index.html so a deep link
// works on a hard reload.
//
// The fallback is what makes /i/some-index/d/%2Fhome%2Fx.pdf/pages a real URL
// rather than one that only survives client-side navigation. Without it the
// second thing anybody does with a link they were sent — paste it into a fresh
// tab — is the thing that fails.
func spaHandler() (http.Handler, error) {
	sub, err := web.Dist()
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		// A real asset is served as itself; anything else is a route.
		if p := strings.TrimPrefix(r.URL.Path, "/"); p != "" {
			if f, err := sub.Open(p); err == nil {
				_ = f.Close()
				// Hashed filenames, so these are safe to cache hard. The
				// document that names them is not — see below.
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// No caching, for the reason the page it replaces gave: the document is
		// embedded in the binary, so a copy held by a browser is a page from a
		// PREVIOUS BUILD — which presents as a fix that did not take, and sends
		// somebody debugging server code that is already correct.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		_, _ = w.Write(index)
	}), nil
}
