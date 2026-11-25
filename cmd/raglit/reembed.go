package main

import (
	"context"
	"log"

	"github.com/iodesystems/raglit"
)

// The daemon's re-embed — bringing every index's vectors to the configured
// embed model, one index at a time — is reembedBackfill below: the queue it
// returns owns the order and the progress/priority surface. A failure stops
// that index until the next start, rather than retrying a refusal in a loop.
//
// The model's vector length is learned from one probe embedding, so an index
// whose vectors have another length is rebuilt even when the model id is the
// same. A failed probe leaves the length unknown (0), and only the id is
// compared.
func reembedBackfill(ctx context.Context, reg *raglit.Registry, emb *raglit.Embedder) *raglit.ReembedQueue {
	if emb == nil || emb.Model == "" {
		return nil
	}
	model := emb.Model
	dim := 0
	if v, err := emb.EmbedQuery(ctx, "dimension probe"); err != nil {
		log.Printf("raglit: embed model %s: dimension probe: %v (comparing the model id only)", model, err)
	} else {
		dim = len(v)
	}
	for _, name := range reg.Names() {
		st, err := reg.Get(name)
		if err != nil {
			continue
		}
		if dropped, err := st.SyncEmbedModel(model, dim); err != nil {
			log.Printf("raglit: %s: embed model record: %v", name, err)
			continue
		} else if dropped {
			log.Printf("raglit: %s: vectors were from another embed model or of another length — re-embedding with %s", name, model)
		}
	}
	q := raglit.NewReembedQueue(reg)
	go func() {
		if err := q.Backfill(ctx, 64); err != nil && ctx.Err() == nil {
			log.Printf("raglit: re-embed stopped: %v", err)
		}
	}()
	return q
}

// backfillSourceStats records file size and modified time for documents
// ingested before raglit kept them (srcstat.go). One stat per document, once:
// a filled row is never visited again.
func backfillSourceStats(ctx context.Context, reg *raglit.Registry) {
	for _, name := range reg.Names() {
		if ctx.Err() != nil {
			return
		}
		st, err := reg.Get(name)
		if err != nil {
			continue
		}
		if n, err := st.BackfillSourceStats(); err != nil {
			log.Printf("raglit: %s: file size/date backfill: %v", name, err)
		} else if n > 0 {
			log.Printf("raglit: %s: recorded file size and date for %d document(s)", name, n)
		}
	}
}
