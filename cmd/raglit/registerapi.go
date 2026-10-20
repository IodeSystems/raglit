package main

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Register-or-layer over HTTP. See raglit/project.go.

type registerProjectIn struct {
	Body struct {
		ProjectID string `json:"project_id" required:"true" doc:"opaque project id (dun: the repo's root commit)"`
		Label     string `json:"label,omitempty" doc:"human label (dun: normalized origin)"`
	}
}

type registerProjectOut struct{ Body raglit.Registration }

func registerProjectOp(reg *raglit.Registry) func(context.Context, *registerProjectIn) (*registerProjectOut, error) {
	return func(ctx context.Context, in *registerProjectIn) (*registerProjectOut, error) {
		r, err := reg.RegisterProject(ctx, in.Body.ProjectID, in.Body.Label)
		if err != nil {
			if errors.Is(err, raglit.ErrNoIndex) || errors.Is(err, raglit.ErrForbidden) {
				return nil, idxErr(err)
			}
			return nil, huma.Error400BadRequest(err.Error())
		}
		return &registerProjectOut{Body: r}, nil
	}
}

type lookupProjectIn struct {
	ID string `query:"id" required:"true" doc:"project id"`
}

type lookupProjectOut struct {
	Body struct {
		ProjectID string `json:"project_id"`
		Base      string `json:"base"`
		// Layers the caller can read: their own, and the public ones.
		Layers []string `json:"layers"`
	}
}

func lookupProjectOp(reg *raglit.Registry) func(context.Context, *lookupProjectIn) (*lookupProjectOut, error) {
	return func(ctx context.Context, in *lookupProjectIn) (*lookupProjectOut, error) {
		base := reg.ProjectBase(in.ID)
		if base == "" || !reg.CanRead(ctx, base) {
			return nil, huma.Error404NotFound("no project registered under that id")
		}
		out := &lookupProjectOut{}
		out.Body.ProjectID, out.Body.Base = in.ID, base
		out.Body.Layers = reg.Readable(ctx, reg.LayersOf(base))
		return out, nil
	}
}

type setVisibilityIn struct {
	Body struct {
		Index      string `json:"index" required:"true"`
		Visibility string `json:"visibility" required:"true" enum:"private,public"`
	}
}

type setVisibilityOut struct{ Body raglit.ACL }

// setVisibilityOp is dun's public:true — the owner opens an index (or a layer)
// for reading. Owner only.
func setVisibilityOp(reg *raglit.Registry) func(context.Context, *setVisibilityIn) (*setVisibilityOut, error) {
	return func(ctx context.Context, in *setVisibilityIn) (*setVisibilityOut, error) {
		if err := reg.SetVisibility(ctx, in.Body.Index, raglit.Visibility(in.Body.Visibility)); err != nil {
			if errors.Is(err, raglit.ErrNoIndex) || errors.Is(err, raglit.ErrForbidden) {
				return nil, idxErr(err)
			}
			return nil, huma.Error400BadRequest(err.Error())
		}
		return &setVisibilityOut{Body: reg.ACLOf(in.Body.Index)}, nil
	}
}

type transferOwnerIn struct {
	Body struct {
		Index string `json:"index" required:"true"`
		Owner string `json:"owner" required:"true" doc:"the new owner's principal, <iss>#<sub>"`
	}
}

// transferOwnerOp hands an index to another principal: how a maintainer
// service takes over a project base. Current owner or admin.
func transferOwnerOp(reg *raglit.Registry) func(context.Context, *transferOwnerIn) (*setVisibilityOut, error) {
	return func(ctx context.Context, in *transferOwnerIn) (*setVisibilityOut, error) {
		if err := reg.TransferOwnership(ctx, in.Body.Index, in.Body.Owner); err != nil {
			if errors.Is(err, raglit.ErrNoIndex) || errors.Is(err, raglit.ErrForbidden) {
				return nil, idxErr(err)
			}
			return nil, huma.Error400BadRequest(err.Error())
		}
		return &setVisibilityOut{Body: reg.ACLOf(in.Body.Index)}, nil
	}
}

type savingsIn struct {
	ID string `query:"id" doc:"project id"`
	NS string `query:"ns" doc:"or the project's namespace (what register returned as namespace)"`
}

type savingsOut struct{ Body raglit.ProjectSavings }

// savingsOp reports what a maintained base would save a shared project.
func savingsOp(reg *raglit.Registry) func(context.Context, *savingsIn) (*savingsOut, error) {
	return func(ctx context.Context, in *savingsIn) (*savingsOut, error) {
		id := in.ID
		if id == "" && in.NS != "" {
			id = reg.ProjectIDOf(in.NS + "__base")
		}
		if id == "" {
			return nil, huma.Error404NotFound("no project registered under that id or namespace")
		}
		rep, err := reg.ProjectSavings(ctx, id)
		if err != nil {
			if errors.Is(err, raglit.ErrNoIndex) {
				return nil, huma.Error404NotFound("no project registered under that id or namespace")
			}
			return nil, huma.Error500InternalServerError("savings", err)
		}
		return &savingsOut{Body: rep}, nil
	}
}
