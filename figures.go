package raglit

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Figures explained into fragments.
//
// A scanned/diagram page needs a VLM to become text at all; while the model is
// there, the OCR prompt (figurePrompt) also DESCRIBES each figure inline, marked
// as `[FIGURE: ...]`. That description lands in the page text, flows into
// fragments unchanged, and indexes in FTS + text vectors through the existing
// path — a described diagram is searchable as text, no new infrastructure.
//
// A media row (schema.sql) then anchors each figure to the fragment holding its
// description and points at its image (a crop where one exists, else the whole
// page image raglit already saved), so a search hit arrives with its figures
// attached. The figure does NOT become its own fragment — a lone caption is
// almost always under the size floor, the exact starvation the floor prevents.

// figurePromptVersion bumps when figureInstruction changes, so frag_recipe marks
// documents re-describable after a prompt change (see fragRecipe).
const figurePromptVersion = 1

// FigurePromptVersion is the current figure-description prompt version, folded
// into recipe hashes (pool + frag_recipe) so a prompt change reprocesses the
// affected documents.
func FigurePromptVersion() int { return figurePromptVersion }

// figureInstruction is appended to the OCR prompt so the VLM inlines a compact
// description of every figure/diagram/chart it sees, in reading order, marked so
// it can be lifted back out into a media row. Kept terse: the description is for
// retrieval, not reproduction.
const figureInstruction = "\n\nFor every figure, diagram, chart, table image, or photo, insert an inline " +
	"description in reading order, on its own line, in the form " +
	"[FIGURE: <what it shows — kind, key elements, any labels/values>]. Do this in " +
	"addition to transcribing the surrounding text."

// figureRE lifts the description text out of a `[FIGURE: ...]` marker (non-greedy
// to the first closing bracket).
var figureRE = regexp.MustCompile(`\[FIGURE:\s*([^\]]*)\]`)

// parseFigureMarkers returns the description of each [FIGURE: ...] marker in text,
// in order. The markers stay in the fragment text (indexed as prose); this only
// reads them out to build media rows.
func parseFigureMarkers(text string) []string {
	ms := figureRE.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// ImageEmbedder embeds a figure IMAGE into a vector (a CLIP-style tower). It is
// the seam for real image embeddings; raglit ships none by default (its Embedder
// is text-only), so figures fall back to embedding their description. An image
// vector lives in a DIFFERENT space from the text query, so it is stored but not
// used by the text-query figure search.
type ImageEmbedder interface {
	EmbedImage(ctx context.Context, mime string, data []byte) ([]float32, error)
}

// embedMedia fills each figure's embedding, in place, before the atomic swap:
// the IMAGE via the image embedder when one is configured and the crop is on
// disk, else the DESCRIPTION via the text embedder (same space as fragments, so
// text queries can match). Best-effort — a figure that can't be embedded is still
// stored (searchable inline as text), it just won't appear in figure search.
func (s *Store) embedMedia(ctx context.Context, media []stagedMedia) {
	if len(media) == 0 {
		return
	}
	// Image embeddings first (when available); collect the rest for a batched
	// description embedding.
	var descIdx []int
	for i := range media {
		m := &media[i]
		if s.imageEmbedder != nil && m.imagePath != "" {
			if data, err := os.ReadFile(m.imagePath); err == nil {
				if v, err := s.imageEmbedder.EmbedImage(ctx, mimeForExt(filepathExt(m.imagePath)), data); err == nil && len(v) > 0 {
					m.vec, m.space = v, "image"
					continue
				}
			}
		}
		if s.embedder != nil && strings.TrimSpace(m.description) != "" {
			descIdx = append(descIdx, i)
		}
	}
	if len(descIdx) == 0 {
		return
	}
	texts := make([]string, len(descIdx))
	for j, i := range descIdx {
		texts[j] = media[i].description
	}
	vecs, err := s.embedder.EmbedDocs(ctx, texts)
	if err != nil {
		return // best-effort
	}
	for j, i := range descIdx {
		if j < len(vecs) && len(vecs[j]) > 0 {
			media[i].vec, media[i].space = vecs[j], "text"
		}
	}
}

// FigureHit is one figure matched by SearchFigures: the figure plus where it
// lives, ranked by similarity to the query.
type FigureHit struct {
	MediaID     int64   `json:"media_id"`
	Path        string  `json:"path"`
	Title       string  `json:"title"`
	Page        int     `json:"page"`
	Description string  `json:"description"`
	ImagePath   string  `json:"image_path"`
	FragmentID  int64   `json:"fragment_id"`
	Score       float64 `json:"score"`
}

// SearchFigures ranks figures by cosine similarity of the query to each figure's
// embedding, best first. Only figures embedded in the TEXT space (their
// description) are comparable to a text query, so image-space embeddings are
// skipped here (they await an image-query path). Requires a text embedder.
func (s *Store) SearchFigures(ctx context.Context, query string, limit int) ([]FigureHit, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("raglit: SearchFigures needs an embedder (SetEmbedder)")
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT m.id, d.path, d.title, m.page, m.description, m.image_path,
		        COALESCE(m.fragment_id, 0), mv.vec
		 FROM media_vectors mv
		 JOIN media m ON m.id = mv.media_id
		 JOIN documents d ON d.id = m.doc_id
		 WHERE mv.space = 'text'`)
	if err != nil {
		return nil, fmt.Errorf("raglit: searchfigures: %w", err)
	}
	defer rows.Close()
	var hits []FigureHit
	for rows.Next() {
		var h FigureHit
		var blob []byte
		if err := rows.Scan(&h.MediaID, &h.Path, &h.Title, &h.Page, &h.Description, &h.ImagePath, &h.FragmentID, &blob); err != nil {
			return nil, err
		}
		h.Score = float64(dot(qv, decodeVec(blob)))
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// filepathExt returns the lowercased extension (without the dot) of a path, for
// mimeForExt — avoids importing path/filepath just for Ext here.
func filepathExt(p string) string {
	for i := len(p) - 1; i >= 0 && p[i] != '/'; i-- {
		if p[i] == '.' {
			return p[i+1:]
		}
	}
	return ""
}

// extractMedia turns the [FIGURE: ...] markers in the finalized fragments into
// media rows, anchored to the fragment (by index) that holds each marker. The
// image is the fragment's page image (the whole-page fallback: a figure whose
// crop is the page still beats no media object); precise per-figure crops are a
// later tier (see plan/fragmenters.md §3b). Ord is document-global.
func extractMedia(frags []stagedFrag, provenance []stagedPage) []stagedMedia {
	pageImg := map[int]string{}
	for _, p := range provenance {
		if p.imgPath != "" {
			pageImg[p.page] = p.imgPath
		}
	}
	var out []stagedMedia
	ord := 0
	for i, f := range frags {
		for _, desc := range parseFigureMarkers(f.text) {
			out = append(out, stagedMedia{
				fragIdx: i, page: f.page, ord: ord,
				kind: "figure", imagePath: pageImg[f.page], description: desc,
			})
			ord++
		}
	}
	return out
}
