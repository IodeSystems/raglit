package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

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
	gat.Register(api, g, op("references", http.MethodGet, "/api/references",
		"What in the index cites a document: the references a withdrawal would leave dangling."),
		referencesOp(reg))
	gat.Register(api, g, op("getIndexHint", http.MethodGet, "/api/index-hint",
		"The index's hint: what a person tells the models about this corpus."), getIndexHintOp(reg))
	gat.Register(api, g, op("putIndexHint", http.MethodPut, "/api/index-hint",
		"Record (or, empty, clear) the index's hint."), putIndexHintOp(reg))
	gat.Register(api, g, op("putDocType", http.MethodPut, "/api/doc-types",
		"Register a document type; reports extractions it made stale."), putDocTypeOp(reg))
	gat.Register(api, g, op("deleteDocType", http.MethodDelete, "/api/doc-types",
		"Remove a document type; extractions made under it are kept."), deleteDocTypeOp(reg))
	gat.Register(api, g, op("proposeDocType", http.MethodPost, "/api/doc-types/propose",
		"Propose a document type from example documents with the daemon's identity model; save registers it."),
		proposeDocTypeOp(reg))
	gat.Register(api, g, op("sameBytes", http.MethodGet, "/api/same-bytes",
		"The other documents of the index holding a document's exact bytes."), sameBytesOp(reg))
	gat.Register(api, g, op("sameHash", http.MethodGet, "/api/same-hash",
		"The documents of the index whose bytes have this sha256."), sameHashOp(reg))
	gat.Register(api, g, op("identities", http.MethodGet, "/api/identities",
		"What each document of the index is: its caption, summary, kind and tags (no model calls)."), identitiesOp(reg))
	gat.Register(api, g, op("identifyTargets", http.MethodGet, "/api/identify/targets",
		"The documents an identify sweep would queue: those with no caption (mode tags: no tags; force: all)."),
		identifyTargetsOp(reg))
	gat.Register(api, g, op("matchDocuments", http.MethodGet, "/api/match",
		"The indexed documents a reference (a path, a filename or a substring of one) names."), matchDocumentsOp(reg))
	gat.Register(api, g, op("putFields", http.MethodPut, "/api/fields",
		"Record a document's fields as a person's ruling; never regenerated over."), putFieldsOp(reg))
	gat.Register(api, g, op("fieldsTargets", http.MethodGet, "/api/fields/targets",
		"The documents a fields sweep would queue: those owed an extraction (force: every typed document)."),
		fieldsTargetsOp(reg))
	gat.Register(api, g, op("fieldsStatus", http.MethodGet, "/api/fields/status",
		"Extraction coverage per document type, and every extraction that is not current, with the reason."),
		fieldsStatusOp(reg))
	gat.Register(api, g, op("readings", http.MethodGet, "/api/readings",
		"Every recorded reading of a page (page 0: of every page of the document that has a superseded one)."),
		readingsOp(reg))
	gat.Register(api, g, op("getAbout", http.MethodGet, "/api/about",
		"What the index is: counted tags and kinds, and the generated paragraph if one was written."), getAboutOp(reg))
	gat.Register(api, g, op("writeAbout", http.MethodPost, "/api/about",
		"Ask the daemon's identity model to (re)write the index's paragraph."), writeAboutOp(reg))
	gat.Register(api, g, op("tagAudit", http.MethodGet, "/api/tags",
		"The index's whole tag vocabulary, with tags sharing a word proposed as near-duplicates."), tagAuditOp(reg))
	gat.Register(api, g, op("mergeTags", http.MethodPost, "/api/tags/merge",
		"Collapse tags a person named, in order: \"old,other=>new\"."), mergeTagsOp(reg))
	gat.Register(api, g, op("truePages", http.MethodGet, "/api/true-pages",
		"A document's pages as read, in order, with the engine and any per-page failure (exact path)."),
		truePagesOp(reg))
	gat.Register(api, g, op("sketchStatus", http.MethodGet, "/api/similar/status",
		"Page-sketch coverage under a recipe: what is sketched, unsketched, or sketched under another recipe."),
		sketchStatusOp(reg))
	gat.Register(api, g, op("similarProbe", http.MethodPost, "/api/similar",
		"Compare one probe against the index at page grain: an indexed document by path, or pages the caller read."),
		similarProbeOp(reg))
	gat.Register(api, g, op("similarPairs", http.MethodGet, "/api/similar/pairs",
		"Every overlapping pair of sketched documents, each counted once."), similarPairsOp(reg))
	gat.Register(api, g, op("identicalGroups", http.MethodGet, "/api/identical",
		"Documents held more than once, byte for byte."), identicalGroupsOp(reg))
	gat.Register(api, g, op("migrateAttachments", http.MethodPost, "/api/migrate-attachments",
		"Move mail attachments extracted beside their archive into the index's own storage (apply; else a dry run). Admin: it moves files on the daemon's host."),
		migrateAttachmentsOp(reg))
	gat.Register(api, g, op("importReadings", http.MethodPost, "/api/import-readings",
		"Record verified transcripts as attested readings of the recordings they match (apply; else a dry run)."),
		importReadingsOp(reg))
}

type applyIn struct {
	Index string `query:"index"`
	Body  struct {
		Apply bool   `json:"apply,omitempty"`
		By    string `json:"by,omitempty"`
	}
}

type migrateAttachmentsOut struct {
	Body struct {
		Moves []raglit.AttachmentMove `json:"moves"`
	}
}

func migrateAttachmentsOp(reg *raglit.Registry) func(context.Context, *applyIn) (*migrateAttachmentsOut, error) {
	return func(ctx context.Context, in *applyIn) (*migrateAttachmentsOut, error) {
		if err := requireAdmin(ctx, reg); err != nil {
			return nil, err
		}
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		moves, err := st.MigrateExtractedAttachments(!in.Body.Apply)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		out := &migrateAttachmentsOut{}
		out.Body.Moves = moves
		if out.Body.Moves == nil {
			out.Body.Moves = []raglit.AttachmentMove{}
		}
		return out, nil
	}
}

type importReadingsOut struct {
	Body struct {
		Matches []raglit.TranscriptMatch `json:"matches"`
	}
}

func importReadingsOp(reg *raglit.Registry) func(context.Context, *applyIn) (*importReadingsOut, error) {
	return func(ctx context.Context, in *applyIn) (*importReadingsOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		ms, err := st.ImportVerifiedTranscripts(!in.Body.Apply, in.Body.By)
		if err != nil {
			return nil, huma.Error500InternalServerError("import readings", err)
		}
		out := &importReadingsOut{}
		out.Body.Matches = ms
		if out.Body.Matches == nil {
			out.Body.Matches = []raglit.TranscriptMatch{}
		}
		return out, nil
	}
}

type sketchStatusIn struct {
	Index string `query:"index"`
	Width int    `query:"width"`
	Mod   int    `query:"mod"`
}

type sketchStatusOut struct {
	Body raglit.SketchStatus
}

func sketchStatusOp(reg *raglit.Registry) func(context.Context, *sketchStatusIn) (*sketchStatusOut, error) {
	return func(ctx context.Context, in *sketchStatusIn) (*sketchStatusOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		w, m := in.Width, in.Mod
		if w <= 0 {
			w = raglit.FoldWidth
		}
		if m <= 0 {
			m = raglit.SampleMod
		}
		ss, err := st.SketchStatusFor(w, m)
		if err != nil {
			return nil, huma.Error500InternalServerError("sketch status", err)
		}
		return &sketchStatusOut{Body: ss}, nil
	}
}

type similarProbeIn struct {
	Index string `query:"index"`
	Body  struct {
		Probe string `json:"probe" doc:"an indexed document path, or the name of what the pages were read from"`
		// Pages, when set, are the probe's text as the caller read it (a
		// transcription sidecar, a local extraction). Empty: Probe must be an
		// indexed document, compared with the index's own text.
		Pages  []raglit.PageText  `json:"pages,omitempty"`
		Source string             `json:"source,omitempty"`
		SHA256 string             `json:"sha256,omitempty" doc:"the probe file's bytes, for the exact-copy answer"`
		Opts   raglit.SimilarOpts `json:"opts"`
	}
}

type similarReportOut struct {
	Body raglit.SimilarReport
}

func similarProbeOp(reg *raglit.Registry) func(context.Context, *similarProbeIn) (*similarReportOut, error) {
	return func(ctx context.Context, in *similarProbeIn) (*similarReportOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		b := in.Body
		var exact map[string]string
		if b.SHA256 != "" {
			if exact, err = st.SameBytesHash(b.SHA256); err != nil {
				return nil, huma.Error500InternalServerError("same bytes", err)
			}
		}
		var rep raglit.SimilarReport
		if len(b.Pages) == 0 {
			rep, err = st.SimilarIndexed(b.Probe, b.Opts)
		} else {
			rep, err = st.SimilarTo(b.Probe, raglit.FoldPages(b.Pages), b.Opts)
			rep.Source = b.Source
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if exact != nil {
			rep.Exact = exact
		}
		return &similarReportOut{Body: rep}, nil
	}
}

// docPair is one overlapping pair of indexed documents, counted once: every
// comparison is symmetric, so A→B and B→A are the same finding.
type docPair struct {
	A string          `json:"a"`
	B string          `json:"b"`
	M raglit.DocMatch `json:"match"`
}

type similarPairsOut struct {
	Body struct {
		Documents []string  `json:"documents"`
		Pairs     []docPair `json:"pairs"`
		Skipped   []string  `json:"skipped,omitempty"`
	}
}

func similarPairsOp(reg *raglit.Registry) func(context.Context, *sketchStatusIn) (*similarPairsOut, error) {
	return func(ctx context.Context, in *sketchStatusIn) (*similarPairsOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		opts := raglit.SimilarOpts{Width: in.Width, Mod: in.Mod}
		if opts.Width <= 0 {
			opts.Width = raglit.FoldWidth
		}
		if opts.Mod <= 0 {
			opts.Mod = raglit.SampleMod
		}
		docs, err := st.SketchedPaths(opts.Width, opts.Mod)
		if err != nil {
			return nil, huma.Error500InternalServerError("sketched paths", err)
		}
		out := &similarPairsOut{}
		out.Body.Documents, out.Body.Pairs = docs, []docPair{}
		seen := map[string]bool{}
		for _, p := range docs {
			rep, err := st.SimilarIndexed(p, opts)
			if err != nil {
				out.Body.Skipped = append(out.Body.Skipped, p+": "+err.Error())
				continue
			}
			for _, m := range rep.Matches {
				key, rev := p+"\x00"+m.Path, m.Path+"\x00"+p
				if seen[key] || seen[rev] {
					continue
				}
				seen[key] = true
				out.Body.Pairs = append(out.Body.Pairs, docPair{A: p, B: m.Path, M: m})
			}
		}
		return out, nil
	}
}

type identicalOut struct {
	Body struct {
		Groups [][]string `json:"groups"`
	}
}

func identicalGroupsOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*identicalOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*identicalOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		groups, err := st.IdenticalGroups()
		if err != nil {
			return nil, huma.Error500InternalServerError("identical", err)
		}
		out := &identicalOut{}
		out.Body.Groups = groups
		if out.Body.Groups == nil {
			out.Body.Groups = [][]string{}
		}
		return out, nil
	}
}

type truePagesOut struct {
	Body struct {
		Pages []raglit.PageText `json:"pages"`
	}
}

func truePagesOp(reg *raglit.Registry) func(context.Context, *pathIn) (*truePagesOut, error) {
	return func(ctx context.Context, in *pathIn) (*truePagesOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		pages, err := st.TruePages(in.Path)
		if err != nil {
			return nil, huma.Error404NotFound(err.Error())
		}
		out := &truePagesOut{}
		out.Body.Pages = pages
		if out.Body.Pages == nil {
			out.Body.Pages = []raglit.PageText{}
		}
		return out, nil
	}
}

type digestOut struct {
	Body raglit.IndexDigest
}

func getAboutOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*digestOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*digestOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		d, err := st.IndexDigest()
		if err != nil {
			return nil, huma.Error500InternalServerError("digest", err)
		}
		return &digestOut{Body: d}, nil
	}
}

func writeAboutOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*digestOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*digestOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if _, err := st.WriteIndexAbout(ctx); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		d, err := st.IndexDigest()
		if err != nil {
			return nil, huma.Error500InternalServerError("digest", err)
		}
		return &digestOut{Body: d}, nil
	}
}

type tagAuditOut struct {
	Body struct {
		Digest raglit.IndexDigest  `json:"digest"`
		Near   map[string][]string `json:"near"`
	}
}

func tagAuditOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*tagAuditOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*tagAuditOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		d, err := st.IndexDigestFor("", 0)
		if err != nil {
			return nil, huma.Error500InternalServerError("digest", err)
		}
		near, err := st.TagNeighbours()
		if err != nil {
			return nil, huma.Error500InternalServerError("tag neighbours", err)
		}
		out := &tagAuditOut{}
		out.Body.Digest, out.Body.Near = d, near
		return out, nil
	}
}

type mergeTagsIn struct {
	Index string `query:"index"`
	Body  struct {
		Merges []string `json:"merges" doc:"each \"old,other=>new\""`
	}
}

type mergeTagsOut struct {
	Body struct {
		Merges []raglit.TagMergeResult `json:"merges"`
	}
}

func mergeTagsOp(reg *raglit.Registry) func(context.Context, *mergeTagsIn) (*mergeTagsOut, error) {
	return func(ctx context.Context, in *mergeTagsIn) (*mergeTagsOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		out := &mergeTagsOut{}
		out.Body.Merges = []raglit.TagMergeResult{}
		for _, spec := range in.Body.Merges {
			m, err := raglit.ParseTagMerge(spec)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			res, err := st.MergeTags(ctx, m)
			if err != nil {
				return nil, huma.Error400BadRequest(fmt.Sprintf("merge %s: %v", m, err))
			}
			out.Body.Merges = append(out.Body.Merges, res)
		}
		return out, nil
	}
}

type readingsIn struct {
	Index string `query:"index"`
	Path  string `query:"path" required:"true"`
	Page  int    `query:"page"`
}

type readingsOut struct {
	Body struct {
		Readings []raglit.PageReading `json:"readings"`
	}
}

func readingsOp(reg *raglit.Registry) func(context.Context, *readingsIn) (*readingsOut, error) {
	return func(ctx context.Context, in *readingsIn) (*readingsOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		out := &readingsOut{}
		out.Body.Readings = []raglit.PageReading{}
		if in.Page > 0 {
			rs, err := st.PageReadings(ctx, in.Path, in.Page)
			if err != nil {
				return nil, huma.Error500InternalServerError("readings", err)
			}
			out.Body.Readings = append(out.Body.Readings, rs...)
			return out, nil
		}
		// Every page of the document that has more than the reading it was born
		// with. Pages nobody touched would bury the ones that were.
		sup, err := st.SupersededPages(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("superseded pages", err)
		}
		seen := map[int]bool{}
		for _, r := range sup {
			if r.Doc == in.Path && !seen[r.Page] {
				seen[r.Page] = true
				rs, err := st.PageReadings(ctx, in.Path, r.Page)
				if err != nil {
					return nil, huma.Error500InternalServerError("readings", err)
				}
				out.Body.Readings = append(out.Body.Readings, rs...)
			}
		}
		return out, nil
	}
}

type putFieldsIn struct {
	Index string `query:"index"`
	Body  struct {
		Path   string          `json:"path" doc:"document path, or a reference matching exactly one"`
		Fields json.RawMessage `json:"fields"`
		By     string          `json:"by,omitempty"`
	}
}

type putFieldsOut struct {
	Body fieldsBody
}

func putFieldsOp(reg *raglit.Registry) func(context.Context, *putFieldsIn) (*putFieldsOut, error) {
	return func(ctx context.Context, in *putFieldsIn) (*putFieldsOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		path, err := resolveOneDoc(st, in.Body.Path)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		got, err := st.RecordFields(ctx, path, raglit.DocFields{Fields: in.Body.Fields}, in.Body.By)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &putFieldsOut{}
		out.Body = fieldsBody{Index: in.Index, Path: path, Type: got.Type, Fields: got.Fields,
			Source: got.Source, Model: got.Model, At: got.At, TextHash: got.TextHash, TypeHash: got.TypeHash}
		return out, nil
	}
}

type fieldsTargetsIn struct {
	Index string `query:"index"`
	Force bool   `query:"force"`
}

func fieldsTargetsOp(reg *raglit.Registry) func(context.Context, *fieldsTargetsIn) (*pathsOut, error) {
	return func(ctx context.Context, in *fieldsTargetsIn) (*pathsOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		var paths []string
		if in.Force {
			ids, ierr := st.Identities()
			err = ierr
			for _, id := range ids {
				if strings.TrimSpace(id.DocType) != "" {
					paths = append(paths, id.Path)
				}
			}
		} else {
			// Owed, not merely absent: an extraction written under a schema that
			// has since been edited answers questions nobody is asking any more.
			paths, err = st.ExtractableMissing()
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("fields targets", err)
		}
		out := &pathsOut{}
		out.Body.Paths = paths
		if out.Body.Paths == nil {
			out.Body.Paths = []string{}
		}
		return out, nil
	}
}

type fieldsStatusOut struct {
	Body struct {
		Coverage []raglit.FieldsCoverage `json:"coverage"`
		Stale    []raglit.StaleFields    `json:"stale"`
	}
}

func fieldsStatusOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*fieldsStatusOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*fieldsStatusOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		cov, err := st.FieldsCoverage()
		if err != nil {
			return nil, huma.Error500InternalServerError("coverage", err)
		}
		stale, err := st.FieldsStaleness()
		if err != nil {
			return nil, huma.Error500InternalServerError("staleness", err)
		}
		out := &fieldsStatusOut{}
		out.Body.Coverage, out.Body.Stale = cov, stale
		if out.Body.Coverage == nil {
			out.Body.Coverage = []raglit.FieldsCoverage{}
		}
		if out.Body.Stale == nil {
			out.Body.Stale = []raglit.StaleFields{}
		}
		return out, nil
	}
}

type identitiesOut struct {
	Body struct {
		Identities []raglit.IdentityStatus `json:"identities"`
	}
}

func identitiesOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*identitiesOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*identitiesOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		rows, err := st.Identities()
		if err != nil {
			return nil, huma.Error500InternalServerError("identities", err)
		}
		out := &identitiesOut{}
		out.Body.Identities = rows
		if out.Body.Identities == nil {
			out.Body.Identities = []raglit.IdentityStatus{}
		}
		return out, nil
	}
}

type identifyTargetsIn struct {
	Index string `query:"index"`
	Force bool   `query:"force" doc:"every document"`
	Mode  string `query:"mode" doc:"identity (default) | tags"`
}

type pathsOut struct {
	Body struct {
		Paths []string `json:"paths"`
	}
}

func identifyTargetsOp(reg *raglit.Registry) func(context.Context, *identifyTargetsIn) (*pathsOut, error) {
	return func(ctx context.Context, in *identifyTargetsIn) (*pathsOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		var paths []string
		switch {
		case in.Force:
			docs, derr := st.Documents()
			err = derr
			for _, d := range docs {
				paths = append(paths, d.Path)
			}
		case in.Mode == raglit.IdentityAskTags:
			paths, err = st.DocumentsMissingTags()
		default:
			paths, err = st.DocumentsMissingIdentity()
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("identify targets", err)
		}
		out := &pathsOut{}
		out.Body.Paths = paths
		if out.Body.Paths == nil {
			out.Body.Paths = []string{}
		}
		return out, nil
	}
}

type matchIn struct {
	Index string `query:"index"`
	Ref   string `query:"ref" required:"true"`
}

func matchDocumentsOp(reg *raglit.Registry) func(context.Context, *matchIn) (*pathsOut, error) {
	return func(ctx context.Context, in *matchIn) (*pathsOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		ms, err := st.MatchDocuments(in.Ref)
		if err != nil {
			return nil, huma.Error500InternalServerError("match", err)
		}
		out := &pathsOut{}
		out.Body.Paths = []string{}
		for _, m := range ms {
			out.Body.Paths = append(out.Body.Paths, m.Path)
		}
		return out, nil
	}
}

type sameHashIn struct {
	Index  string `query:"index"`
	SHA256 string `query:"sha256" required:"true"`
}

func sameHashOp(reg *raglit.Registry) func(context.Context, *sameHashIn) (*sameBytesOut, error) {
	return func(ctx context.Context, in *sameHashIn) (*sameBytesOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		same, err := st.SameBytesHash(in.SHA256)
		if err != nil {
			return nil, huma.Error500InternalServerError("same bytes", err)
		}
		out := &sameBytesOut{}
		out.Body.Documents = same
		if out.Body.Documents == nil {
			out.Body.Documents = map[string]string{}
		}
		return out, nil
	}
}

type sameBytesOut struct {
	Body struct {
		Documents map[string]string `json:"documents" doc:"path → title"`
	}
}

func sameBytesOp(reg *raglit.Registry) func(context.Context, *pathIn) (*sameBytesOut, error) {
	return func(ctx context.Context, in *pathIn) (*sameBytesOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		same, err := st.SameBytesAs(in.Path)
		if err != nil {
			return nil, huma.Error500InternalServerError("same bytes", err)
		}
		out := &sameBytesOut{}
		out.Body.Documents = same
		if out.Body.Documents == nil {
			out.Body.Documents = map[string]string{}
		}
		return out, nil
	}
}

type hintOut struct {
	Body struct {
		Hint string `json:"hint"`
	}
}

func getIndexHintOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*hintOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*hintOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		out := &hintOut{}
		out.Body.Hint = st.IndexHint()
		return out, nil
	}
}

type indexOnlyIn struct {
	Index string `query:"index"`
}

type putHintIn struct {
	Index string `query:"index"`
	Body  struct {
		Hint string `json:"hint"`
	}
}

func putIndexHintOp(reg *raglit.Registry) func(context.Context, *putHintIn) (*hintOut, error) {
	return func(ctx context.Context, in *putHintIn) (*hintOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if err := st.SetIndexHint(in.Body.Hint, time.Now().UnixNano()); err != nil {
			return nil, huma.Error500InternalServerError("hint", err)
		}
		out := &hintOut{}
		out.Body.Hint = st.IndexHint()
		return out, nil
	}
}

type putDocTypeIn struct {
	Index string `query:"index"`
	Body  raglit.DocType
}

type docTypeOut struct {
	Body struct {
		Type raglit.DocType `json:"type"`
		// Stale counts extractions that answer the type's PREVIOUS schema.
		Stale int `json:"stale"`
	}
}

func putDocTypeOp(reg *raglit.Registry) func(context.Context, *putDocTypeIn) (*docTypeOut, error) {
	return func(ctx context.Context, in *putDocTypeIn) (*docTypeOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if err := st.SetDocType(in.Body); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return docTypeResult(st, in.Body)
	}
}

// docTypeResult is a registered type as stored, with what its registration
// made stale.
func docTypeResult(st *raglit.Store, t raglit.DocType) (*docTypeOut, error) {
	name := raglit.NormalizeTypeName(t.Name)
	out := &docTypeOut{}
	if stored, err := st.DocTypeByName(name); err == nil {
		out.Body.Type = stored
	} else {
		out.Body.Type = t
	}
	if stale, err := st.FieldsStaleness(); err == nil {
		for _, s := range stale {
			if raglit.NormalizeTypeName(s.Type) == name && s.Reason == raglit.FieldsSchemaMoved {
				out.Body.Stale++
			}
		}
	}
	return out, nil
}

type nameIn struct {
	Index string `query:"index"`
	Name  string `query:"name" required:"true"`
}

func deleteDocTypeOp(reg *raglit.Registry) func(context.Context, *nameIn) (*okOut, error) {
	return func(ctx context.Context, in *nameIn) (*okOut, error) {
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		if err := st.DeleteDocType(in.Name); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}

type proposeDocTypeIn struct {
	Index string `query:"index"`
	Body  struct {
		Name string   `json:"name"`
		Docs []string `json:"docs" doc:"example documents: paths, or any reference that matches exactly one"`
		Save bool     `json:"save,omitempty"`
	}
}

func proposeDocTypeOp(reg *raglit.Registry) func(context.Context, *proposeDocTypeIn) (*docTypeOut, error) {
	return func(ctx context.Context, in *proposeDocTypeIn) (*docTypeOut, error) {
		var st *raglit.Store
		var err error
		if in.Body.Save {
			st, err = writeIdx(ctx, reg, in.Index)
		} else {
			st, err = readIdx(ctx, reg, in.Index)
		}
		if err != nil {
			return nil, err
		}
		if len(in.Body.Docs) == 0 || strings.TrimSpace(in.Body.Name) == "" {
			return nil, huma.Error400BadRequest("name the type and one or more example documents")
		}
		var gold []string
		for _, ref := range in.Body.Docs {
			p, err := resolveOneDoc(st, ref)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			gold = append(gold, p)
		}
		t, err := st.ProposeDocType(ctx, in.Body.Name, gold)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if !in.Body.Save {
			out := &docTypeOut{}
			out.Body.Type = t
			return out, nil
		}
		if err := st.SetDocType(t); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return docTypeResult(st, t)
	}
}

type pathIn struct {
	Index string `query:"index"`
	Path  string `query:"path" required:"true"`
}

type referencesOut struct {
	Body struct {
		References []raglit.Reference `json:"references"`
	}
}

func referencesOp(reg *raglit.Registry) func(context.Context, *pathIn) (*referencesOut, error) {
	return func(ctx context.Context, in *pathIn) (*referencesOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		refs, err := st.ReferencesTo(ctx, in.Path)
		if err != nil {
			return nil, huma.Error500InternalServerError("references", err)
		}
		out := &referencesOut{}
		out.Body.References = refs
		if out.Body.References == nil {
			out.Body.References = []raglit.Reference{}
		}
		return out, nil
	}
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
