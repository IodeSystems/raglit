package main

import (
	"context"
	"fmt"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/raglit"
)

// Serving rulings over the daemon, so nothing has to read raglit's files.
//
// kgraph needs to know that two documents are copies, or that one version
// supersedes another. It was reading relations.jsonl directly, and that coupling
// broke the moment the storage changed: raglit moved to a database projected
// from an audit trail, kgraph carried on reading a file nobody writes any more,
// and it would have gone on reporting stale rulings with no error anywhere.
//
// So raglit answers questions about its own data and no consumer parses its
// storage. The daemon stays STATELESS about judgements: the caller names the
// project directory, because a judgement store lives beside the documents and
// the caller — kgraph, whose root IS that directory — already knows where that
// is. Recording it daemon-side would be a second place for it to be wrong.
//
// Read-only on purpose. A ruling is a decision, and the path that records one
// goes through the CLI where the audit trail and its ordering are enforced;
// a second writer reachable over HTTP would be a second chance to get that
// ordering wrong.

type relationsIn struct {
	// Project is the directory holding judgements.db — the project root, not the
	// index home.
	Project string `query:"project" required:"true" doc:"project directory holding the judgement store"`
	// Doc, when given, narrows to rulings involving that document path.
	Doc string `query:"doc" doc:"only rulings involving this document path"`
}

type relationOut struct {
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

type relationsOut struct {
	Body struct {
		Relations []relationOut `json:"relations"`
	}
}

func openProjectJudgements(project string) (*raglit.JudgementStore, error) {
	if project == "" {
		return nil, fmt.Errorf("project directory is required")
	}
	return raglit.OpenJudgements(raglit.JudgementsPath(project), raglit.AuditPath(project))
}

func listRelationsOp(_ context.Context, in *relationsIn) (*relationsOut, error) {
	js, err := openProjectJudgements(in.Project)
	if err != nil {
		return nil, huma.Error400BadRequest("open judgements", err)
	}
	defer js.Close()

	var marks []raglit.Mark
	if in.Doc != "" {
		marks, err = js.RelationsFor(in.Doc)
	} else {
		marks, err = js.Relations()
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("read relations", err)
	}
	out := &relationsOut{}
	out.Body.Relations = make([]relationOut, 0, len(marks))
	for _, m := range marks {
		out.Body.Relations = append(out.Body.Relations, relationOut{
			A: m.A, B: m.B, Kind: string(m.Kind), Supersedes: m.Supersedes,
			Note: m.Note, By: m.By, At: m.At,
			Relation: string(m.Relation), Coverage: m.Coverage,
		})
	}
	return out, nil
}

type slicesIn struct {
	Project string `query:"project" required:"true" doc:"project directory holding the judgement store"`
	Parent  string `query:"parent" doc:"only slices of this bundle"`
}

type sliceOut struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	Title  string `json:"title,omitempty"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

type slicesOut struct {
	Body struct {
		Slices []sliceOut `json:"slices"`
	}
}

func listSlicesOp(_ context.Context, in *slicesIn) (*slicesOut, error) {
	js, err := openProjectJudgements(in.Project)
	if err != nil {
		return nil, huma.Error400BadRequest("open judgements", err)
	}
	defer js.Close()

	var sls []raglit.Slice
	if in.Parent != "" {
		sls, err = js.SlicesOf(in.Parent)
	} else {
		sls, err = js.Slices()
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("read slices", err)
	}
	out := &slicesOut{}
	out.Body.Slices = make([]sliceOut, 0, len(sls))
	for _, s := range sls {
		out.Body.Slices = append(out.Body.Slices, sliceOut{
			ID: s.ID, Parent: s.Parent, From: s.From, To: s.To,
			Title: s.Title, Note: s.Note, By: s.By, At: s.At,
		})
	}
	return out, nil
}

// Materializing slices, daemon-side.
//
// The child of a slice is a document in the INDEX, and the daemon owns the
// index. A client cannot build one itself without opening the index file
// directly, which is the second writer the whole daemon arrangement exists to
// prevent — and on a project-local checkout it silently writes to a DIFFERENT
// index from the one search reads, so the child is built and then invisible.
//
// So the client declares (that is a judgement, and it goes through the audit
// trail) and asks the daemon to build. One writer, one index, and a child that
// search can actually find.

type materializeIn struct {
	Project string `query:"project" required:"true" doc:"project directory holding the judgement store"`
	Index   string `query:"index" doc:"index name (default: the default index)"`
	ID      string `query:"id" doc:"only this slice; default rebuilds every slice in the project"`
}

type materializeOut struct {
	Body struct {
		Built  int      `json:"built"`
		Pages  int      `json:"pages"`
		Failed []string `json:"failed,omitempty"`
	}
}

func materializeSlicesOp(reg *raglit.Registry) func(context.Context, *materializeIn) (*materializeOut, error) {
	return func(_ context.Context, in *materializeIn) (*materializeOut, error) {
		js, err := openProjectJudgements(in.Project)
		if err != nil {
			return nil, huma.Error400BadRequest("open judgements", err)
		}
		defer js.Close()

		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error500InternalServerError("open index", err)
		}

		var slices []raglit.Slice
		if in.ID != "" {
			sl, ok, err := js.Slice(in.ID)
			if err != nil {
				return nil, huma.Error500InternalServerError("read slice", err)
			}
			if !ok {
				return nil, huma.Error404NotFound("no slice " + in.ID)
			}
			slices = []raglit.Slice{sl}
		} else if slices, err = js.Slices(); err != nil {
			return nil, huma.Error500InternalServerError("read slices", err)
		}

		out := &materializeOut{}
		for _, sl := range slices {
			n, err := raglit.MaterializeSlice(st, sl)
			if err != nil {
				// One unbuildable slice must not abandon the rest — a parent that
				// has not been ingested yet is an ordinary state, not a failure of
				// the whole operation.
				out.Body.Failed = append(out.Body.Failed, fmt.Sprintf("%s: %v", sl.ID, err))
				continue
			}
			out.Body.Built++
			out.Body.Pages += n
		}
		return out, nil
	}
}
