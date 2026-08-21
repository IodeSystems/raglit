// Package web carries the built review SPA.
//
// A Go package inside the npm project rather than an embed from cmd/raglit,
// because //go:embed cannot reach outside the directory of the package that
// declares it — "../../web/dist" is not a legal embed pattern. Putting the embed
// here keeps the whole frontend, source and build output and all, in one
// directory that the Go build imports like any other package.
package web

import (
	"embed"
	"errors"
	"io/fs"
	"testing/fstest"
)

// dist is the vite build output. It is NOT committed (2026-08-19).
//
// It used to be, so that `go install …/cmd/raglit@latest` would serve a UI on a
// machine with no node. That traded a working install for a generated bundle in
// the tree, rewritten on every UI change; adopting MUI multiplies its size, so
// the trade stopped being worth it. `dist/.gitkeep` IS committed — it is what
// keeps this embed pattern matching something in a clean checkout, since a
// //go:embed of an empty or missing directory does not compile.
//
// Deliberately NOT behind a build tag. `rebuildRaglit` (cmd/raglit/selfbuild.go)
// re-invokes `go build` with no tags when the tree changes, so a tagged embed
// would drop the UI out of the binary every time it rebuilt itself — silently,
// which is the failure mode this codebase keeps paying to avoid. Tagless means
// the worst case is an honest page instead of a missing one.
//
// `all:` so nothing is dropped for starting with `_` or `.` — vite does not emit
// such names today, but a build that quietly loses an asset is a page that 404s
// for a file plainly sitting on disk.
//
//go:embed all:dist
var dist embed.FS

// ErrNotBuilt reports a binary built without the UI.
var ErrNotBuilt = errors.New("raglit: the review UI was not built into this binary (run `make web`)")

// Dist is the built bundle, rooted at the directory holding index.html.
//
// When dist holds no index.html the binary was built without running the UI
// build, and this serves a page that SAYS so. A blank page or a 404 would read
// as a broken daemon; the one thing a reader needs here is the build command.
func Dist() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return notBuiltFS(), nil
	}
	return sub, nil
}

// Built reports whether a real bundle is embedded, so a caller that wants to
// warn once at startup can, rather than waiting for somebody to open a browser.
func Built() bool {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

func notBuiltFS() fs.FS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(notBuiltPage)}}
}

const notBuiltPage = `<!doctype html>
<meta charset="utf-8">
<title>raglit — UI not built</title>
<style>
  body { font: 15px/1.6 system-ui, sans-serif; max-width: 46em; margin: 12vh auto; padding: 0 1.5em;
         color: #1a1d21; background: #f6f7f9; }
  @media (prefers-color-scheme: dark) { body { color: #e6e8eb; background: #0f1115; } }
  code, pre { font-family: ui-monospace, monospace; }
  pre { padding: .8em 1em; border-radius: 8px; background: rgba(127,127,127,.14); overflow-x: auto; }
  .muted { opacity: .7; }
</style>
<h1>The review UI is not in this binary</h1>
<p>The daemon is running and its API is serving normally — this is only the web
   interface. <code>web/dist</code> is generated and is no longer committed, so a
   binary built without the frontend build has no bundle to serve.</p>
<pre>make web     # build the bundle
make install # rebuild raglit with it embedded</pre>
<p class="muted">Everything the UI reads is available over the API and the CLI in
   the meantime — <code>raglit status</code>, <code>raglit search</code>,
   <code>raglit problems</code>.</p>
`
