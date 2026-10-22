package main

import (
	"context"
	"log"

	"github.com/iodesystems/raglit"
)

// reembedIndexes brings every index's vectors to the configured embed model:
// an index whose vectors came from another model drops them, and every index
// then embeds the fragments that have none — including any ingested while no
// embedder was configured. One index at a time, in the background; the embed
// model's own channel bounds the load. A failure stops that index until the
// next start, rather than retrying a refusal in a loop.
func reembedIndexes(ctx context.Context, reg *raglit.Registry, model string) {
	if model == "" {
		return
	}
	for _, name := range reg.Names() {
		st, err := reg.Get(name)
		if err != nil {
			continue
		}
		if dropped, err := st.SyncEmbedModel(model); err != nil {
			log.Printf("raglit: %s: embed model record: %v", name, err)
			continue
		} else if dropped {
			log.Printf("raglit: %s: vectors were from another embed model — re-embedding with %s", name, model)
		}
		total := 0
		for ctx.Err() == nil {
			n, err := st.BackfillVectors(ctx, 64)
			if err != nil {
				log.Printf("raglit: %s: re-embed stopped after %d: %v", name, total, err)
				break
			}
			if n == 0 {
				break
			}
			total += n
		}
		if total > 0 {
			log.Printf("raglit: %s: embedded %d fragments/figures that had no vector", name, total)
		}
	}
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
