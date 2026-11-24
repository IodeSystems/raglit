package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/raglit"
)

type docRelationsIn struct {
	Index string `query:"index" required:"true" doc:"index name"`
	Path  string `query:"path" required:"true" doc:"document display path"`
}

type docRelationOut struct {
	A          string  `json:"a"`
	B          string  `json:"b"`
	Kind       string  `json:"kind"`
	Supersedes string  `json:"supersedes,omitempty"`
	Note       string  `json:"note,omitempty"`
	By         string  `json:"by,omitempty"`
	At         string  `json:"at,omitempty"`
	Relation   string  `json:"relation,omitempty"`
	Coverage   float64 `json:"coverage,omitempty"`
}

type docSliceOut struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	Title  string `json:"title,omitempty"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

type docRelationsOut struct {
	Body struct {
		Relations []docRelationOut `json:"relations"`
		Slices    []docSliceOut    `json:"slices"`
	}
}

// docRelationsHandler serves relations and slices for a selected document.
// It resolves the project directory from the index name, the same way the
// doc-detail handler does, so the client only needs the index name and the
// document's display path.
//
// Why a new endpoint when /api/relations and /api/slices already answer both
// halves: those two take the PROJECT DIRECTORY — a path on the daemon host —
// because they are the stateless CLI-facing surface, and a browser cannot name
// that path from an index name and a display path. /api/doc-detail solves the
// same join server-side for the UI, and this endpoint is the same join for
// relations and slices: one request, no host knowledge in the pane.
// TestDocRelations_ResolvesServerSide is the record of that decision.
func docRelationsHandler(reg *raglit.Registry) func(context.Context, *docRelationsIn) (*docRelationsOut, error) {
	return func(ctx context.Context, in *docRelationsIn) (*docRelationsOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		v, err := indexView(st)
		if err != nil {
			return nil, huma.Error500InternalServerError("index view", err)
		}
		abs := in.Path
		if !filepath.IsAbs(abs) {
			resolved, ok := v.resolve(abs)
			if !ok {
				return nil, huma.Error400BadRequest("resolve path", errors.New("document not in index"))
			}
			abs = resolved
		}
		js, err := raglit.OpenJudgements(raglit.AuditPath(v.Root))
		if err != nil {
			return nil, huma.Error500InternalServerError("open judgements", err)
		}
		defer js.Close()

		marks, err := js.RelationsFor(abs)
		if err != nil {
			return nil, huma.Error500InternalServerError("read relations", err)
		}
		out := &docRelationsOut{}
		out.Body.Relations = make([]docRelationOut, 0, len(marks))
		for _, m := range marks {
			out.Body.Relations = append(out.Body.Relations, docRelationOut{
				A: m.A, B: m.B, Kind: string(m.Kind), Supersedes: m.Supersedes,
				Note: m.Note, By: m.By, At: m.At,
				Relation: string(m.Relation), Coverage: m.Coverage,
			})
		}

		sls, err := js.SlicesOf(abs)
		if err != nil {
			return nil, huma.Error500InternalServerError("read slices", err)
		}
		out.Body.Slices = make([]docSliceOut, 0, len(sls))
		for _, s := range sls {
			out.Body.Slices = append(out.Body.Slices, docSliceOut{
				ID: s.ID, Parent: s.Parent, From: s.From, To: s.To,
				Title: s.Title, Note: s.Note, By: s.By, At: s.At,
			})
		}
		return out, nil
	}
}
