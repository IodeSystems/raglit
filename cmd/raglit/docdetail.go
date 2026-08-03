package main

import (
	"context"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/attest"
)

// Everything known about one document, in one answer.
//
// It was spread across five calls — /api/doc for pages, /api/get-document for
// text, /api/relations for rulings on copies, the similar machinery for where
// else this content shows up, and the attest mount for verdicts. A person asking
// "what do we actually have on this document, and how much of it has anybody
// checked" had to visit five places and hold the join in their head.
//
// Assembled server-side rather than fanned out from the browser because the
// interesting part is the JOIN: a transcription is worth something different
// when a person has corrected two of its pages, and a near-duplicate matters
// differently when somebody has already ruled the pair a copy. A UI stitching
// that together from five responses gets to decide what "reviewed" means, and
// that decision belongs next to the data.

// DocDetail is a document, its readings, and what is known about it.
type DocDetail struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Kind  string `json:"kind"`

	// Asset is this document's handle IN THE ATTEST MOUNT — root-relative,
	// because Root is attest's security boundary and every one of its operations
	// addresses assets that way.
	//
	// Stated as its own field rather than left for a caller to derive. The review
	// UI derived it by reusing the absolute path it already had, and every "open
	// the review for this document" link answered 404 "no such asset" — a failure
	// that looks like a broken mount rather than a wrong argument, because the
	// path in the URL is a real document either way.
	Asset string `json:"asset,omitempty"`

	// Identity is what this document IS, as opposed to what its file is called
	// (identity.go): a caption, a summary, a kind, and whether a machine or a
	// person said so. Nil when nobody has established it.
	Identity *raglit.DocIdentity `json:"identity,omitempty"`

	// Original is where the raw bytes can be fetched, or empty when this mount
	// cannot serve them. A link rather than the bytes: a scanned deed is
	// megabytes and nothing on the page needs it until somebody asks.
	Original string `json:"original,omitempty"`

	Pages []DocDetailPage `json:"pages,omitempty"`
	Text  string          `json:"text,omitempty"`

	// SeenIn is where else this content appears: near-duplicates and
	// containment, plus any ruling a person has made on the pair.
	SeenIn []SeenIn `json:"seen_in,omitempty"`

	Attest *AttestSummary `json:"attest,omitempty"`

	// History is every recorded reading of every page, plus every ruling, in the
	// order they happened. What this document has been SAID to say over time,
	// which is a different question from what it says now and the one you need
	// when a quotation somewhere no longer matches.
	History []HistoryEvent `json:"history,omitempty"`

	// InFlight is work already queued or running for THIS document.
	//
	// NOT for deduplication — Enqueue already refuses a url that is pending or
	// running, which was measured and fixed long before this. It is so a reader
	// knows the document is mid-read: the transcript on screen is about to be
	// replaced, a quotation taken from it now may not survive, and a button that
	// appears to do nothing has in fact already been pressed.
	//
	// One real gap it does cover: EnqueueFresh dedupes on `fresh >= ?`, so a
	// FRESH re-ingest does stack on top of a plain pending job. That is
	// deliberate — the caller is asking for work the queued job would not do —
	// and it is exactly the case where a person should see what is already
	// running before adding to it.
	InFlight []DocJob `json:"in_flight,omitempty"`
}

// DocJob is one queued or running ingest job for this document.
type DocJob struct {
	ID    int64  `json:"id"`
	State string `json:"state"` // pending | running
	Mode  string `json:"mode,omitempty"`
	Since int64  `json:"since,omitempty"`
}

// HistoryEvent is one thing that happened to this document.
type HistoryEvent struct {
	Kind   string `json:"kind"` // reading | verdict
	Page   int    `json:"page,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	Source string `json:"source,omitempty"` // machine | corrected, or the verdict kind
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
	Active bool   `json:"active,omitempty"`
	Unit   string `json:"unit,omitempty"`
	// Text is the reading, or a correction's replacement wording. Trimmed for
	// the list; the full text of the active reading is on the Pages tab.
	Text string `json:"text,omitempty"`
}

// DocDetailPage is one page: its transcript, and the image it was read from.
type DocDetailPage struct {
	Page     int    `json:"page"`
	Text     string `json:"text"`
	Source   string `json:"source,omitempty"` // machine | corrected
	Engine   string `json:"engine,omitempty"`
	ReadBy   string `json:"read_by,omitempty"`
	ReadAt   string `json:"read_at,omitempty"`
	ImageURL string `json:"image_url,omitempty"`

	// Figures described on this page.
	//
	// The transcript says "[FIGURE: a survey map showing property lines…]" and
	// the reader's next question is "so where is it". These were extracted,
	// described by the VLM and stored in `media` at ingest, and nothing surfaced
	// them — so a page could tell you a diagram existed and give you no way to
	// look at it or to see what the machine thought it was.
	Figures []DocFigure `json:"figures,omitempty"`
}

// DocFigure is one described figure. No crop: bbox is not recorded, so the
// figure IS its page as far as the image goes, and saying that plainly beats
// inventing a rectangle.
type DocFigure struct {
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description"`
}

// SeenIn is one other document holding this one's content.
type SeenIn struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
	// Jaccard says "are these the same document"; the two containments say how
	// much of each side is inside the other. All three, because a deed INSIDE a
	// title commitment has containment near 1.0 one way and a Jaccard near 0.05,
	// and one number cannot express "wholly inside".
	Jaccard      float64 `json:"jaccard"`
	ContainProbe float64 `json:"contain_probe"`
	ContainMatch float64 `json:"contain_match"`
	// Relation is what the COMPUTATION proposes; Ruling is what a person decided.
	Relation string `json:"relation,omitempty"`

	// Where the overlap actually sits, on both sides. A score says two documents
	// share text; only this says WHICH text and WHERE, which is the difference
	// between "these look related" and "pages 21-24 of that declaration are this
	// deed".
	Blocks []SeenBlock `json:"blocks,omitempty"`

	// MatchedChars is how much was proved identical. Coverage alone cannot be
	// acted on for a short document — 0.83 of a 400-character email is a
	// signature block and 0.83 of a deed is a deed.
	MatchedChars int `json:"matched_chars,omitempty"`

	// GenericChars is how much of this document was excluded as corpus-generic.
	// Reported rather than applied silently: masking is the one step that can
	// hide a real duplicate, so a low score on a mostly-generic document has to
	// be explainable.
	GenericChars int `json:"generic_chars,omitempty"`

	// NumericOnlyIn* are numbers inside the aligned span that appear on ONE side
	// only. Two copies of a deed agreeing at 0.97 and disagreeing about "25.00"
	// versus "30.00" is not a housekeeping detail, and averaging it into a score
	// is how it gets missed.
	NumericOnlyHere  []string `json:"numeric_only_here,omitempty"`
	NumericOnlyThere []string `json:"numeric_only_there,omitempty"`

	// Ruling is what a PERSON decided about the pair — copy, version, unrelated —
	// or empty for a pair nobody has ruled on. Kept beside the scores rather than
	// replacing them, because a score is a candidate and a ruling is an answer.
	Ruling string `json:"ruling,omitempty"`

	// Link is where to go and look at the other document.
	Link string `json:"link,omitempty"`
}

// SeenBlock is one aligned region: where it sits on each side, and how well the
// two copies actually agree across it.
type SeenBlock struct {
	HerePages  string  `json:"here_pages"`
	TherePages string  `json:"there_pages"`
	Agreement  float64 `json:"agreement"`
	Chars      int     `json:"chars"`
	// Runs is how many separate identical spans this was chained from. 1 means
	// identical throughout; a high count on a short block means the two copies
	// differ constantly, which is itself the finding.
	Runs int `json:"runs"`
}

// AttestSummary is how far the review of this document has got.
//
// Counts, never a percentage complete. attest's own provenance is a sentence
// for a reason: "96 of 96 untouched" and "94 affirmed, 2 corrected" are
// different states that a single number would render identically.
type AttestSummary struct {
	Producer  string `json:"producer,omitempty"`
	Total     int    `json:"total"`
	Confirmed int    `json:"confirmed"`
	Corrected int    `json:"corrected"`
	Affirmed  int    `json:"affirmed"`
	Unclear   int    `json:"unclear"`
	Untouched int    `json:"untouched"`
	// Provenance is attest's own sentence, quoted rather than summarised. A tool
	// that paraphrases a qualified claim has changed what was attested to.
	Provenance string `json:"provenance,omitempty"`
	// Workbench is where to go and rule on it.
	Workbench string `json:"workbench,omitempty"`
	Verdicts  int    `json:"verdicts"`
}

type docDetailIn struct {
	Index string `query:"index"`
	Path  string `query:"path" required:"true" doc:"document path as indexed"`
}

type docDetailOut struct{ Body DocDetail }

func docDetailOp(reg *raglit.Registry) func(context.Context, *docDetailIn) (*docDetailOut, error) {
	return func(_ context.Context, in *docDetailIn) (*docDetailOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound("open index", err)
		}
		root, _ := indexRoot(st)

		// Two spellings of the same document, and they are not interchangeable.
		// The index stores ABSOLUTE paths, so that is what the store's readers
		// want; attest addresses assets ROOT-RELATIVE, because Root is its
		// security boundary. A caller may hand us either, so both are derived
		// once here rather than guessed at four times below.
		abs, rel := in.Path, in.Path
		if filepath.IsAbs(in.Path) {
			if r, rerr := filepath.Rel(root, in.Path); rerr == nil && root != "" {
				rel = r
			}
		} else if root != "" {
			abs = filepath.Join(root, in.Path)
		}

		d := DocDetail{Path: rel, Asset: rel, Kind: detailKind(abs)}
		if id, ierr := st.DocumentIdentity(abs); ierr == nil && !id.Empty() {
			d.Identity = &id
		}
		if root != "" {
			d.Original = "/api/attest/" + in.Index + "/source?asset=" + url.QueryEscape(rel)
		}

		// Pages and text, from whichever reading the document has.
		if c, err := st.DocText(abs, 0, 0, 0); err == nil {
			d.Title, d.Text = c.Title, c.Text

			// Pages come from TruePages, not from DocText's grouping.
			//
			// DocText groups fragments by their START page, so a fragment the
			// segmenter stitched ACROSS a page boundary lands wholly under the
			// page it began on and the next page gets no entry at all. On a
			// 2-page record of survey that showed as one page in the UI, with
			// page 2's EXISTING CORNERS table — every found monument and its
			// offset from calculated position — folded invisibly into page 1.
			//
			// The text was never lost and search always found it. What was wrong
			// was WHERE it appeared to be, which for a survey is most of the
			// point: a monument call belongs to the sheet it is drawn on.
			//
			// TruePages exists for exactly this and says so in its own comment —
			// it de-overlaps by offsets AND splits each fragment by its recorded
			// page_spans. Nothing here needed writing; it needed calling.
			pages := c.Pages
			if tp, terr := st.TruePages(abs); terr == nil && len(tp) > 0 {
				pages = make([]raglit.DocPageText, 0, len(tp))
				for _, t := range tp {
					pages = append(pages, raglit.DocPageText{Page: t.Page, Text: t.Text})
				}
			}
			for _, p := range pages {
				dp := DocDetailPage{Page: p.Page, Text: p.Text}
				// Only offer an image when one was actually stored. A page comes
				// from the FRAGMENT table and a page image from ocr_pages, and
				// they do not always agree: a born-digital PDF has pages and no
				// rasterisation, and a region read stores its own crops. Emitting
				// the URL regardless gave every such page a broken <img> — a
				// thin grey line where a reviewer expects the thing the words
				// were read from, which is worse than saying there is no image.
				if img, ierr := st.PageImagePath(abs, p.Page); ierr == nil && img != "" {
					q := url.Values{}
					q.Set("index", in.Index)
					q.Set("path", abs)
					q.Set("page", strconv.Itoa(p.Page))
					dp.ImageURL = "/api/page-image?" + q.Encode()
				}
				for _, f := range c.Figures {
					if f.Page == p.Page {
						dp.Figures = append(dp.Figures, DocFigure{Kind: f.Kind, Description: f.Description})
					}
				}
				d.Pages = append(d.Pages, dp)
			}
		}

		// Where else this content lives, with any ruling on the pair.
		if rep, err := st.SimilarIndexed(abs, raglit.SimilarOpts{}); err == nil {
			marks := map[string]string{}
			if js, jerr := raglit.OpenJudgements(raglit.AuditPath(root)); jerr == nil {
				if ms, merr := js.RelationsFor(abs); merr == nil {
					for _, m := range ms {
						other := m.A
						if other == abs {
							other = m.B
						}
						marks[other] = string(m.Kind)
					}
				}
			}
			for _, h := range rep.Matches {
				si := SeenIn{
					Path: h.Path, Title: h.Title, Jaccard: h.Jaccard,
					ContainProbe: h.ContainProbe, ContainMatch: h.ContainMatch,
					Relation: string(h.Relation), Ruling: marks[h.Path],
					MatchedChars: h.MatchedChars, GenericChars: h.GenericChars,
					NumericOnlyHere: h.NumericOnlyInProbe, NumericOnlyThere: h.NumericOnlyInMatch,
					Link: "#/documents/" + url.PathEscape(in.Index) + "/" +
						url.PathEscape(h.Path) + "/transcript",
				}
				for _, b := range h.Blocks {
					si.Blocks = append(si.Blocks, SeenBlock{
						HerePages:  pageRange(b.ProbeFromPage, b.ProbeToPage),
						TherePages: pageRange(b.MatchFromPage, b.MatchToPage),
						Agreement:  b.Agreement(), Chars: b.MatchedChars, Runs: b.Runs,
					})
				}
				d.SeenIn = append(d.SeenIn, si)
			}
		}

		// How far the review has got.
		if state, lerr := attest.Load(abs); lerr == nil && state != nil {
			sum := &AttestSummary{
				Producer: state.Producer, Provenance: state.Provenance(),
				Total:     state.Stats.Total,
				Confirmed: state.Stats.Confirmed, Corrected: state.Stats.Corrected,
				Affirmed: state.Stats.Affirmed, Unclear: state.Stats.Unclear,
				Untouched: state.Stats.Untouched,
				// The review route in the SPA. The standalone page is still
				// mounted at /attest/<index>?asset=… and still serves audio,
				// which this route does not — see plan/spa-ui.md.
				Workbench: "/i/" + url.PathEscape(in.Index) + "/attest/a/" + url.PathEscape(rel),
			}
			if rows, rerr := st.Attestations(rel); rerr == nil {
				sum.Verdicts = len(rows)
			}
			d.Attest = sum
		}
		// Anything already queued or running for this document. Both states,
		// because "running" and "waiting behind three others" are both reasons
		// not to queue it again.
		for _, st8 := range []string{"pending", "running"} {
			jobs, jerr := st.Jobs(st8, 200)
			if jerr != nil {
				continue
			}
			for _, j := range jobs {
				if !sameTarget(j.URL, abs, rel) {
					continue
				}
				since := j.StartedAt
				if since == 0 {
					since = j.EnqueuedAt
				}
				d.InFlight = append(d.InFlight, DocJob{ID: j.ID, State: j.State, Mode: j.Mode, Since: since})
			}
		}

		// What has been said about this document, over time.
		if hist, herr := st.DocReadingHistory(abs); herr == nil {
			for _, h := range hist {
				d.History = append(d.History, HistoryEvent{
					Kind: "reading", Page: h.Page, Seq: h.Seq, Source: h.Source,
					Note: h.Note, By: h.By, At: h.At, Active: h.Active, Text: clip(h.Text, 400),
				})
			}
		}
		if rows, rerr := st.Attestations(rel); rerr == nil {
			for _, r := range rows {
				d.History = append(d.History, HistoryEvent{
					Kind: "verdict", Seq: r.Seq, Source: r.Kind, Note: r.Note,
					By: r.RuledBy, At: r.RuledAt, Unit: r.Unit, Text: clip(r.Text, 400),
				})
			}
		}
		// Undated events keep their arrival order rather than sorting to the
		// front: a reading recorded before this column existed has no timestamp,
		// and inventing a position for it would misreport the sequence.
		sort.SliceStable(d.History, func(i, j int) bool {
			a, b := d.History[i].At, d.History[j].At
			if a == "" || b == "" {
				return false
			}
			return a < b
		})

		return &docDetailOut{Body: d}, nil
	}
}

// detailKind names what a reviewer is about to be shown, using the same split
// the evidence renderers use so the tabs and the workbench never disagree.
func detailKind(path string) string {
	switch {
	case isAudioAsset(path):
		return "audio"
	case isTextAsset(path):
		return "text"
	case isPDF(path):
		return "pdf"
	}
	return "other"
}

// sameTarget reports whether a job's target is this document.
//
// Matched against both spellings rather than one. A job is queued with whatever
// the caller typed — an absolute path from a sweep, a root-relative one from the
// UI, a file:// URL from the watcher — and comparing only the form we happen to
// hold here would report "nothing queued" for a document that is mid-ingest,
// which is the exact wrong answer for a button that starts another one.
func sameTarget(jobURL, abs, rel string) bool {
	u := strings.TrimPrefix(jobURL, "file://")
	return u == abs || u == rel || filepath.Clean(u) == filepath.Clean(abs)
}
