package main

import (
	"context"
	"net/http"
	"sort"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/gwag/v2/gw/gat"

	"github.com/iodesystems/raglit"
)

// Routes for the commands that used to open an index file in-process (USER
// 2026-10-30: every client goes through the daemon). Each wraps the Store
// method the command called, behind the same ACL as every other route: reads
// through readIdx, writes through writeIdx, and work that reaches the
// daemon's host or models through requireAdmin.

func registerCorpusRoutes(api huma.API, g *gat.Gateway, reg *raglit.Registry, lf *llmFlags, home raglit.Home) {
	op := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
	}
	gat.Register(api, g, op("refragment", http.MethodPost, "/api/refragment",
		"Re-ingest documents holding a fragment larger than the embed model accepts (probes the limit unless given)."),
		refragmentOp(reg, lf, home))
}

type refragmentIn struct {
	Index string `query:"index"`
	Body  struct {
		LimitChars int  `json:"limit_chars,omitempty" doc:"the embed limit in characters; 0 = probe the daemon's embed model"`
		DryRun     bool `json:"dry_run,omitempty"`
	}
}

type oversizedDoc struct {
	Path  string `json:"path"`
	Chars int    `json:"chars"`
}

type refragmentOut struct {
	Body struct {
		LimitChars int            `json:"limit_chars"`
		Documents  []oversizedDoc `json:"documents"`
		Queued     int            `json:"queued"`
		Failed     []string       `json:"failed,omitempty"`
	}
}

func refragmentOp(reg *raglit.Registry, lf *llmFlags, home raglit.Home) func(context.Context, *refragmentIn) (*refragmentOut, error) {
	return func(ctx context.Context, in *refragmentIn) (*refragmentOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		limit := in.Body.LimitChars
		if limit <= 0 {
			if lf == nil || *lf.embedModel == "" {
				return nil, huma.Error400BadRequest("no embed model configured on the daemon — pass limit_chars")
			}
			cfg, _, _ := raglit.LoadConfig(home)
			emb := raglit.NewEmbedder(lf.embedClientForProbe(), *lf.embedModel)
			limit = st.EmbedLimitChars(ctx, emb, cfg.EmbedLimitChars)
		}
		if limit <= 0 {
			return nil, huma.Error422UnprocessableEntity("could not determine an embed limit — the endpoint may truncate silently; pass limit_chars")
		}
		over, err := st.OversizedDocs(limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("oversized documents", err)
		}
		out := &refragmentOut{}
		out.Body.LimitChars = limit
		out.Body.Documents = []oversizedDoc{}
		for p, n := range over {
			out.Body.Documents = append(out.Body.Documents, oversizedDoc{Path: p, Chars: n})
		}
		sort.Slice(out.Body.Documents, func(i, j int) bool { return out.Body.Documents[i].Chars > out.Body.Documents[j].Chars })
		if in.Body.DryRun {
			return out, nil
		}
		for _, d := range out.Body.Documents {
			if err := st.MarkForReingest(d.Path); err != nil {
				out.Body.Failed = append(out.Body.Failed, d.Path+": "+err.Error())
				continue
			}
			if _, err := st.Enqueue(d.Path, ""); err != nil {
				out.Body.Failed = append(out.Body.Failed, d.Path+": "+err.Error())
				continue
			}
			out.Body.Queued++
		}
		return out, nil
	}
}
