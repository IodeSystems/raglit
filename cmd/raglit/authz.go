package main

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Index access for request handlers. Every handler opens its index through
// these rather than reg.Get, so the ACL (raglit/acl.go) is applied at the one
// place a request reaches an index. With auth off they are reg.Get.

// readIdx opens an index the caller may read.
func readIdx(ctx context.Context, reg *raglit.Registry, name string) (*raglit.Store, error) {
	st, err := reg.Read(ctx, name)
	return st, idxErr(err)
}

// writeIdx opens an index the caller owns, creating it (owned by the caller)
// when the name is new.
func writeIdx(ctx context.Context, reg *raglit.Registry, name string) (*raglit.Store, error) {
	st, err := reg.Write(ctx, name)
	return st, idxErr(err)
}

func idxErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, raglit.ErrNoIndex):
		return huma.Error404NotFound("no such index")
	case errors.Is(err, raglit.ErrForbidden):
		return huma.Error403Forbidden(err.Error())
	}
	return huma.Error500InternalServerError("open index", err)
}

// requireAdmin guards operations that reach the daemon HOST rather than an
// index: reading a path on its filesystem (ingest by path, OCR of a path,
// watched project homes, project audit stores) or acting on daemon-wide state
// (the shared pool). On a shared daemon, "ingest /etc/shadow into my private
// index" is a file read as the daemon's user, so these are the admin's.
func requireAdmin(ctx context.Context, reg *raglit.Registry) error {
	if !reg.ACLEnabled() || raglit.PrincipalFrom(ctx) == reg.Admin() {
		return nil
	}
	return huma.Error403Forbidden("this operation reads the daemon host's filesystem or daemon-wide state: admin only")
}

// indexHolds reports whether path is a document of st (or of its parent, for a
// branch), as indexed — with or without the file:// scheme.
func indexHolds(st *raglit.Store, path string) bool {
	ms, err := st.MatchDocuments(path)
	if err != nil {
		return false
	}
	bare := strings.TrimPrefix(path, "file://")
	for _, m := range ms {
		if p := strings.TrimPrefix(m.Path, "file://"); p == bare {
			return true
		}
	}
	return false
}
