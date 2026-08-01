package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/attest"
)

// The review workbench, in the daemon, for every index.
//
// It ran as `raglit attest`: one process per document, on an ad-hoc port,
// rooted at whatever directory the named file happened to sit in. That is fine
// for looking at one sheet and useless for validating a corpus — the daemon
// already knows every index and every document, and knew nothing about regions
// or verdicts, while the review server knew regions and exactly one directory.
//
// attest was built to be mounted: "A host with its own router, its own auth and
// its own middleware calls Register once and gets the review API inside its API
// — same OpenAPI document, same auth, one deployment." Nobody had called it.
// This is that call.
//
// One Service PER INDEX rather than one for the daemon. Service.Root is the
// security boundary — "a request cannot reach outside it" — so a single mount
// spanning every corpus would make one root out of unrelated trees, and a bug in
// asset resolution would cross between a legal corpus and a source checkout. Per
// index, the blast radius is the index.

// mountAttest registers the review API and UI for each index that has one.
//
// Errors on an individual index are returned rather than swallowed: a mount that
// silently skips an index looks identical to an index with nothing to review,
// and the whole point of this surface is to say how much has been checked.
func mountAttest(router chi.Router, api huma.API, reg *raglit.Registry) error {
	for _, name := range reg.Names() {
		st, err := reg.Get(name)
		if err != nil {
			return fmt.Errorf("attest mount %s: %w", name, err)
		}
		root, err := indexRoot(st)
		if err != nil {
			return fmt.Errorf("attest mount %s: %w", name, err)
		}
		if root == "" {
			// An empty index has no tree to bound a review to. Skipped rather
			// than rooted at "/" — a root nobody chose is the one that lets a
			// path bug reach the whole filesystem.
			continue
		}
		svc := &attest.Service{Root: root, Ident: attest.Guest{}, Ev: multiEvidence{root: root}}
		prefix := "/api/attest/" + name
		if err := svc.Register(api, prefix); err != nil {
			return fmt.Errorf("attest mount %s: %w", name, err)
		}
		// The UI is a plain handler and takes its bases as arguments, so the
		// index lives in the PATH rather than in a query parameter. Every fetch
		// the page makes is relative to the base it was handed, which is what
		// lets one embedded page serve every index unchanged.
		router.Method(http.MethodGet, "/attest/"+name, svc.UI(prefix, prefix+"/source"))
		router.Method(http.MethodGet, prefix+"/source", svc.AssetBytes())
	}
	return nil
}

// indexRoot is the deepest directory containing every document in an index.
//
// Derived rather than configured, because nothing declares it: an index is a set
// of ingested paths and the tree they share is a fact about them. Deepest common
// ancestor, so the root is as tight as the corpus allows — an index whose
// documents all live under one project bounds a review to that project, and only
// an index spanning unrelated trees widens to their common parent.
func indexRoot(st *raglit.Store) (string, error) {
	docs, err := st.Documents()
	if err != nil {
		return "", err
	}
	root := ""
	for _, d := range docs {
		if !filepath.IsAbs(d.Path) {
			continue
		}
		dir := filepath.Dir(d.Path)
		if root == "" {
			root = dir
			continue
		}
		root = commonDir(root, dir)
	}
	return root, nil
}

// commonDir returns the deepest directory that is a prefix of both.
func commonDir(a, b string) string {
	as, bs := strings.Split(filepath.Clean(a), string(filepath.Separator)), strings.Split(filepath.Clean(b), string(filepath.Separator))
	n := min(len(as), len(bs))
	i := 0
	for i < n && as[i] == bs[i] {
		i++
	}
	if i == 0 {
		return string(filepath.Separator)
	}
	return strings.Join(as[:i], string(filepath.Separator))
}

// multiEvidence routes a unit to the renderer for whatever the asset IS.
//
// One Service per index serves audio, text and sheets together, so the renderer
// cannot be chosen when the mount is built — it is a property of each asset. The
// dispatch is on the asset path, which is what the reading already recorded.
type multiEvidence struct{ root string }

func (m multiEvidence) Render(ctx context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	ev := evidenceFor(a.ID, m.root)
	if ev == nil {
		// Audio on a box with no ffmpeg. attest turns this into the 501 that
		// says the mount cannot render, which is the honest answer.
		return attest.Artifact{}, fmt.Errorf("raglit: this mount cannot render %s", a.ID)
	}
	return ev.Render(ctx, a, u)
}

// AsSeen and Humane are forwarded only when the chosen renderer implements them.
// Advertising a rendering a renderer does not have would make attest promise a
// reviewer something it then cannot produce.
func (m multiEvidence) AsSeen(ctx context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	if ev, ok := evidenceFor(a.ID, m.root).(attest.AsSeenEvidence); ok {
		return ev.AsSeen(ctx, a, u)
	}
	return attest.Artifact{}, fmt.Errorf("raglit: no as-seen rendering for %s", a.ID)
}

func (m multiEvidence) Humane(ctx context.Context, a attest.Asset, u attest.Unit) (attest.Artifact, error) {
	if ev, ok := evidenceFor(a.ID, m.root).(attest.HumaneEvidence); ok {
		return ev.Humane(ctx, a, u)
	}
	return attest.Artifact{}, fmt.Errorf("raglit: no humane rendering for %s", a.ID)
}

// sweepIndex writes a reading for every document in an index that can have one,
// and projects each asset's verdict log into the index.
//
// The reason this exists: attest lists ASSETS WITH A READING, so a corpus where
// nothing has been read looks empty rather than unreviewed. A sweep is what
// turns 406 indexed documents into 406 reviewable ones.
//
// Skips rather than fails, per document. A corpus contains things nothing can
// read — a spreadsheet, a zip, a PDF nobody has run regions over — and one of
// them must not abort a pass over the other four hundred. What was skipped is
// counted and returned, because a sweep that quietly covers half a corpus and
// reports success is the same failure as a review that claims coverage it does
// not have.
type sweepResult struct {
	Wrote   int               `json:"wrote"`
	Skipped int               `json:"skipped"`
	Reasons map[string]string `json:"reasons,omitempty"`
}

func sweepIndex(st *raglit.Store, root string) (sweepResult, error) {
	res := sweepResult{Reasons: map[string]string{}}
	docs, err := st.Documents()
	if err != nil {
		return res, err
	}
	for _, d := range docs {
		if !filepath.IsAbs(d.Path) || attest.IsReading(d.Path) || attest.IsLog(d.Path) ||
			raglit.IsTranscription(d.Path) {
			continue
		}
		rd, _, err := readingFor(d.Path)
		if err != nil {
			res.Skipped++
			res.Reasons[filepath.Base(d.Path)] = err.Error()
			continue
		}
		if _, err := attest.WriteReading(d.Path, rd); err != nil {
			res.Skipped++
			res.Reasons[filepath.Base(d.Path)] = err.Error()
			continue
		}
		res.Wrote++

		// Project whatever rulings already exist beside the asset. A sweep is
		// also how a machine that has never seen these logs catches up on them:
		// the jsonl travels with the corpus, the table does not.
		if log, err := attest.ReadLog(d.Path); err == nil && len(log) > 0 {
			rel, rerr := filepath.Rel(root, d.Path)
			if rerr != nil {
				rel = filepath.Base(d.Path)
			}
			if err := st.PutAttestations(rel, log); err != nil {
				res.Reasons[filepath.Base(d.Path)] = "reading written, verdicts not projected: " + err.Error()
			}
		}
	}
	return res, nil
}

type attestSweepIn struct {
	Index string `query:"index" doc:"index to sweep; required"`
}

type attestSweepOut struct{ Body sweepResult }

// attestSweepOp writes readings across an index so its documents can be reviewed.
func attestWriteReadingsOp(reg *raglit.Registry) func(context.Context, *attestSweepIn) (*attestSweepOut, error) {
	return func(_ context.Context, in *attestSweepIn) (*attestSweepOut, error) {
		if in.Index == "" {
			return nil, huma.Error400BadRequest("index is required")
		}
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound("open index", err)
		}
		root, err := indexRoot(st)
		if err != nil {
			return nil, huma.Error500InternalServerError("index root", err)
		}
		res, err := sweepIndex(st, root)
		if err != nil {
			return nil, huma.Error500InternalServerError("sweep", err)
		}
		return &attestSweepOut{Body: res}, nil
	}
}
