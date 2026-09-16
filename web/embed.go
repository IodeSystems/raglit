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

// dist holds the vite build output. The REAL bundle is generated and NOT
// committed (2026-08-19); what IS committed is a two-file placeholder —
// index.html plus assets/app.js — so that `go install …/cmd/raglit@latest`
// serves a page on a machine with no node, and the SPA tests in
// cmd/raglit/webui_test.go RUN rather than skip on every clean checkout.
//
// The full bundle used to be committed for exactly that reason; adopting MUI
// multiplied its size until the trade stopped being worth it, so only the
// placeholder stays in the tree. `make web` wipes dist (emptyOutDir) and
// rebuilds the real thing over it — web/.gitignore keeps the two placeholder
// files un-ignored across that wipe, and the hashed assets replace them on the
// next commit.
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
// With a clean checkout dist holds only the committed placeholder, which IS an
// index.html — so Built() is true and the tests run everywhere. This branch is
// for a tree where someone deleted dist/index.html: serve a page that SAYS the
// UI was not built rather than a blank one or a 404. The one thing a reader
// needs here is the build command.
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

// Built reports whether an index.html is embedded — the committed placeholder
// counts, which is deliberate: it is what keeps the SPA tests running on every
// clean checkout instead of skipping them.
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
   interface. The committed placeholder in <code>web/dist</code> is missing from
   this tree, so there is no bundle to serve.</p>
<pre>make web     # build the bundle
make install # rebuild raglit with it embedded</pre>
<p class="muted">Everything the UI reads is available over the API and the CLI in
   the meantime — <code>raglit status</code>, <code>raglit search</code>,
   <code>raglit problems</code>.</p>
`
