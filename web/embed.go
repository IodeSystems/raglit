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
	"io/fs"
)

// dist is the vite build output.
//
// COMMITTED to the tree on purpose: `go install
// github.com/iodesystems/raglit/cmd/raglit@latest` has to work on a machine with
// no node, and //go:embed only sees files present in the module. A
// generated-at-build-time dist would make the released binary serve nothing at
// all, and it would do it silently.
//
// `all:` so nothing is dropped for starting with `_` or `.` — vite does not emit
// such names today, but a build that quietly loses an asset is a page that 404s
// for a file plainly sitting on disk.
//
//go:embed all:dist
var dist embed.FS

// Dist is the built bundle, rooted at the directory holding index.html.
func Dist() (fs.FS, error) { return fs.Sub(dist, "dist") }
