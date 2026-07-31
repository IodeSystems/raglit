package attest

import (
	_ "embed"
	"fmt"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strings"
)

// The review page, and the one route the JSON API cannot carry.
//
// Both are plain http.Handlers rather than huma operations, and for different
// reasons. The page is HTML, which has no business in an OpenAPI document. The
// asset route serves whole recordings, and huma's []byte body reads a response
// into memory and answers no Range request — which for audio review is not a
// performance note but a functional one: a reviewer scrubbing a two-hour
// hearing needs the browser to fetch the part it is playing.
//
// So a host mounts three things: Register for the operations, and these two
// wherever its own router puts them. raglit does exactly this — humachi for the
// JSON, plain routes for the UI and the page images.

//go:embed ui.html
var uiPage string

// UI serves the review page.
//
// apiBase and assetBase are where the host mounted the other two, injected into
// the page rather than assumed. A page that hardcodes /api works until the first
// host mounts attest under a prefix, and then fails in the browser with no
// server-side trace.
func (s *Service) UI(apiBase, assetBase string) http.Handler {
	page := strings.NewReplacer(
		"__ATTEST_API__", strings.TrimSuffix(apiBase, "/"),
		"__ATTEST_ASSET__", strings.TrimSuffix(assetBase, "/"),
	).Replace(uiPage)
	body := []byte(page)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// No caching. The page is embedded in the binary, so a stale copy in a
		// tab outlives the upgrade that fixed whatever sent the reviewer looking.
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
}

// AssetBytes serves the source asset itself — `?asset=<root-relative path>` —
// with Range support, so a page can seek in a long recording.
//
// This is the SOURCE, not evidence. It is what the reviewer navigates; the
// per-unit artifact a verdict actually rests on comes from the evidence
// operation, which knows what a claim was read from and whether a re-render
// still matches. Serving the whole file here and calling it evidence would put
// the reviewer back in front of the wrong artifact, which is the failure this
// whole framework exists to prevent.
func (s *Service) AssetBytes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset := r.URL.Query().Get("asset")
		full, err := s.authorize(r.Context(), asset, PermRead)
		if err != nil {
			// The permission layer speaks huma errors; out here the status is
			// all that survives, and a 404 is the right answer for both cases
			// this can produce.
			http.NotFound(w, r)
			return
		}
		if ct := mime.TypeByExtension(filepath.Ext(full)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		http.ServeFile(w, r, full)
	})
}

// ListenOn binds a fixed port when one is given, and one chosen by the OS
// otherwise, returning the URL to print.
//
// A fixed port is what lets an open review page survive a restart: the page
// keeps posting to the port it loaded from, so coming back on a NEW port
// silently breaks every save while the tab still looks fine.
func ListenOn(host, port string, tls bool) (net.Listener, string, error) {
	if port == "" {
		port = "0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, "", err
	}
	_, bound, _ := net.SplitHostPort(ln.Addr().String())
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return ln, fmt.Sprintf("%s://%s:%s", scheme, host, bound), nil
}

// Serve runs the review surface, over TLS when a certificate pair is given.
//
// Worth having as soon as the surface leaves the loopback interface, and for
// reasons that are not abstract: it serves the recording itself, and it accepts
// rulings signed with a person's name. Over plain HTTP on a shared network both
// are readable and the second is forgeable.
//
// There is also a mundane reason. A browser on a page served over plain HTTP
// may decline to decode media at all, depending on how it is configured — so
// the reviewer gets a player that never loads and no error to explain it.
func Serve(ln net.Listener, h http.Handler, certFile, keyFile string) error {
	if certFile == "" && keyFile == "" {
		return http.Serve(ln, h)
	}
	if certFile == "" || keyFile == "" {
		return fmt.Errorf("attest: TLS needs both a certificate and a key")
	}
	return http.ServeTLS(ln, h, certFile, keyFile)
}
