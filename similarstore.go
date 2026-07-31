package raglit

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The stored side of similarity detection: building sketches, and the two-phase
// search that uses them.
//
// PHASE 1 samples, PHASE 2 does not, and the split is deliberate. Candidate
// generation must not read every document's text, so it probes a 1-in-32 sample
// (shingle_index) and takes anything that shares a few of them. Scoring then
// recomputes EXACTLY from the two documents' text. The reason not to score from
// the sample is not performance: these numbers decide which copy of an instrument
// a filing cites, and an estimate that is usually right is the wrong instrument
// for that decision. A sampled containment of 0.94 ± 0.08 and an exact one of
// 0.94 read identically in a report and mean different things.
//
// The cost of that split is bounded because phase 1 is allowed to be generous. A
// false candidate costs one exact rescore of a few tens of kilobytes; a missed
// candidate is invisible. So every threshold here leans toward over-generating.
//
// The SQL here is raw rather than sqlc-generated, for the reason documented on
// TruePages: `sqlc generate` with the installed toolchain corrupts the SQL text of
// every existing query in internal/db, so adding two tables to the generated layer
// currently means breaking sixty. schema.sql still declares the tables (it is
// applied at runtime by store.go, independent of codegen), and this follows the
// precedent the schema header already sets for FTS5 and the vector scan.

// SketchDoc builds (or rebuilds) one document's page sketches from the index's
// own text.
//
// Reads the document through TruePages, so a page number in a later alignment is
// the page the text is actually on, not the page its fragment opened on.
// Replaces the document's rows wholesale: an incremental update would have to
// know which shingles a re-OCR'd page lost, and a re-read changes a page
// completely often enough that the bookkeeping would cost more than the rewrite.
func (s *Store) SketchDoc(docPath string, w, mod int) (int, error) {
	ctx := context.Background()
	doc, err := s.q.GetDocumentByPath(ctx, docPath)
	if err != nil {
		return 0, err
	}
	pages, err := s.TruePages(docPath)
	if err != nil {
		return 0, err
	}
	sketches := SketchPages(FoldPages(pages), w, mod)
	recipe := Recipe(w, mod)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM shingle_index WHERE doc_id = ?`, doc.ID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shingle_pages WHERE doc_id = ?`, doc.ID); err != nil {
		return 0, err
	}
	insPage, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO shingle_pages(doc_id, page, chars, total, sampled, recipe)
		 VALUES(?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insPage.Close()
	insIdx, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO shingle_index(hash, doc_id, page) VALUES(?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insIdx.Close()
	n := 0
	for _, ps := range sketches {
		if _, err := insPage.ExecContext(ctx, doc.ID, ps.Page, ps.Chars, ps.Total, len(ps.Hashes), recipe); err != nil {
			return 0, err
		}
		for _, h := range ps.Hashes {
			// int64(uint64) reinterprets the bits; the read side converts back the
			// same way. SQLite has no unsigned integer, and storing the decimal
			// string instead was tried first — it quadrupled the table for nothing,
			// since nothing ever compares these as numbers, only for equality.
			if _, err := insIdx.ExecContext(ctx, int64(h), doc.ID, ps.Page); err != nil {
				return 0, err
			}
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ClearSketches drops every stored sketch.
//
// Needed for a recipe change, and it has to be a truncate rather than a
// per-document rebuild: a document deleted from the index since the last build
// keeps its shingle rows (the ON DELETE CASCADE only fires when the document row
// itself goes, and a re-ingest replaces rather than deletes), so those rows go on
// generating candidates that resolve to a path TruePages cannot read. That showed
// up as `similar` reporting matches against documents that were not there.
func (s *Store) ClearSketches() error {
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM shingle_index`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM shingle_pages`)
	return err
}

// SketchedPaths lists documents carrying a sketch under this recipe, in a stable
// order. The all-pairs audit walks these and nothing else — comparing a document
// with no sketch would silently fall back to zero candidates and report it clean.
func (s *Store) SketchedPaths(w, mod int) ([]string, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT DISTINCT d.path FROM shingle_pages s JOIN documents d ON d.id = s.doc_id
		 WHERE s.recipe = ? ORDER BY d.path`, Recipe(w, mod))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// shinglePageRow is one stored page sketch, joined to its document.
type shinglePageRow struct {
	Path    string
	Title   string
	DocID   int64
	Page    int
	Chars   int
	Total   int
	Sampled int
	Recipe  string
}

func (s *Store) listShinglePages() ([]shinglePageRow, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT d.path, d.title, s.doc_id, s.page, s.chars, s.total, s.sampled, s.recipe
		 FROM shingle_pages s JOIN documents d ON d.id = s.doc_id
		 ORDER BY s.doc_id, s.page`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shinglePageRow
	for rows.Next() {
		var r shinglePageRow
		if err := rows.Scan(&r.Path, &r.Title, &r.DocID, &r.Page, &r.Chars, &r.Total, &r.Sampled, &r.Recipe); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// unsketched lists documents with no page sketch under this recipe.
//
// Expressed as a LEFT JOIN rather than NOT EXISTS because the correlated
// subquery is what sqlc's SQLite parser choked on hardest; keeping the two forms
// interchangeable means folding this back into the generated layer later needs no
// rewrite.
func (s *Store) unsketched(recipe string) ([]DocRef, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT d.path, d.title FROM documents d
		 LEFT JOIN shingle_pages s ON s.doc_id = d.id AND s.recipe = ?
		 WHERE s.doc_id IS NULL
		 GROUP BY d.id ORDER BY d.id`, recipe)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocRef
	for rows.Next() {
		var d DocRef
		if err := rows.Scan(&d.Path, &d.Title); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// shingleIndexRow is one posting: a sampled shingle and the page carrying it.
type shingleIndexRow struct {
	Hash  uint64
	DocID int64
	Page  int
}

func (s *Store) listShingleIndex() ([]shingleIndexRow, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT hash, doc_id, page FROM shingle_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shingleIndexRow
	for rows.Next() {
		var h int64
		var r shingleIndexRow
		if err := rows.Scan(&h, &r.DocID, &r.Page); err != nil {
			return nil, err
		}
		r.Hash = uint64(h)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SketchAll builds sketches for every document that has none under the current
// recipe. Returns how many documents were sketched. A document whose text cannot
// be read is skipped with its error collected rather than aborting the run: one
// unreadable document must not leave the rest of the corpus unchecked, which is
// the state in which `similar` silently reports "nothing found".
func (s *Store) SketchAll(w, mod int) (done int, errs []error) {
	rows, err := s.unsketched(Recipe(w, mod))
	if err != nil {
		return 0, []error{err}
	}
	for _, r := range rows {
		if _, err := s.SketchDoc(r.Path, w, mod); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Path, err))
			continue
		}
		done++
	}
	return done, errs
}

// SketchStatus is what the index knows about its own sketch coverage. Reported
// rather than assumed, because an unsketched document is UNCHECKED and reporting
// it as clean is the one wrong answer here.
type SketchStatus struct {
	Documents   int      `json:"documents"`
	Sketched    int      `json:"sketched"`
	Pages       int      `json:"pages"`
	IndexRows   int      `json:"index_rows"`
	Recipe      string   `json:"recipe"`
	Unsketched  []string `json:"unsketched,omitempty"`
	StaleRecipe []string `json:"stale_recipe,omitempty"`
}

// SketchStatusFor reports coverage under a recipe.
func (s *Store) SketchStatusFor(w, mod int) (SketchStatus, error) {
	ctx := context.Background()
	recipe := Recipe(w, mod)
	st := SketchStatus{Recipe: recipe}
	total, err := s.q.CountDocuments(ctx)
	if err != nil {
		return st, err
	}
	st.Documents = int(total)
	pages, err := s.listShinglePages()
	if err != nil {
		return st, err
	}
	byDoc := map[string]bool{}
	stale := map[string]bool{}
	for _, p := range pages {
		st.Pages++
		if p.Recipe == recipe {
			byDoc[p.Path] = true
		} else {
			stale[p.Path] = true
		}
	}
	st.Sketched = len(byDoc)
	un, err := s.unsketched(recipe)
	if err != nil {
		return st, err
	}
	for _, r := range un {
		st.Unsketched = append(st.Unsketched, r.Path)
	}
	for p := range stale {
		if !byDoc[p] {
			st.StaleRecipe = append(st.StaleRecipe, p)
		}
	}
	sort.Strings(st.StaleRecipe)
	idx, err := s.listShingleIndex()
	if err != nil {
		return st, err
	}
	st.IndexRows = len(idx)
	return st, nil
}

// docFacts is what phase 1 needs about one indexed document.
type docFacts struct {
	id     int64
	path   string
	title  string
	total  int         // distinct sampled shingles across the document
	pages  int         // pages carrying text
	shared map[int]int // page -> sampled shingles shared with the probe
}

// candidates is phase 1: which indexed documents share enough sampled shingles
// with the probe to be worth an exact comparison.
//
// The whole inverted index is loaded and probed in memory. At 10^2-10^3 documents
// that is the right call and banded LSH would be machinery for nothing: this
// corpus's index is 39,778 rows and loads in 56 ms, against re-folding 233
// documents (2.5 MB of fragment text, 1.59 MB folded, plus their offset tables) to
// answer the same question. The crossover is around 10^6 index rows — roughly
// 3,000 documents of this size — at which point the load stops being free and
// probing by hash range (the reason hash leads the primary key) is the next step,
// not LSH.
//
// Generic shingles are skipped, using the SAME cutoff the alignment mask uses.
// There were briefly two definitions of "too common to be evidence" — this one
// counted PAGES with its own 20%-of-corpus threshold, the mask counted DOCUMENTS —
// and the page-based one was dead code: a shingle's posting list is bounded by the
// pages it appears on, which on this corpus never exceeded 56 against a cutoff of
// 88, so it fired zero times on 23,790 shingles. Two thresholds where one never
// fires is worse than one, so there is now one.
func (s *Store) candidates(probe []uint64, minShared int, generic map[uint64]bool) (map[int64]*docFacts, error) {
	pages, err := s.listShinglePages()
	if err != nil {
		return nil, err
	}
	facts := map[int64]*docFacts{}
	for _, p := range pages {
		f := facts[p.DocID]
		if f == nil {
			f = &docFacts{id: p.DocID, path: p.Path, title: p.Title, shared: map[int]int{}}
			facts[p.DocID] = f
		}
		f.total += p.Sampled
		if p.Chars > 0 {
			f.pages++
		}
	}
	idx, err := s.listShingleIndex()
	if err != nil {
		return nil, err
	}
	postings := map[uint64][]shingleIndexRow{}
	for _, r := range idx {
		if generic[r.Hash] {
			continue
		}
		postings[r.Hash] = append(postings[r.Hash], r)
	}
	want := map[uint64]bool{}
	for _, h := range probe {
		want[h] = true
	}
	for h := range want {
		for _, r := range postings[h] {
			if f := facts[r.DocID]; f != nil {
				f.shared[r.Page]++
			}
		}
	}
	out := map[int64]*docFacts{}
	for id, f := range facts {
		n := 0
		best := 0
		for _, c := range f.shared {
			n += c
			if c > best {
				best = c
			}
		}
		// Either the document as a whole shares enough, OR one page does. The
		// per-page test is what finds a two-page exhibit inside a forty-page
		// filing: its contribution to the document total is noise, and a
		// document-level threshold that admits it admits everything.
		if n >= minShared || best >= minShared {
			out[id] = f
		}
	}
	return out, nil
}

// GenericDocFreq is the number of DISTINCT documents a sampled shingle must occur
// in before its text is masked out of every alignment.
//
// Set from the measured frequency distribution of this corpus rather than from a
// round number. Of the distinct sampled shingles on this corpus, 17,801 occur in
// exactly one document and 4,739 in two; the count is in single digits by 9
// documents, and then a thin tail runs out to 56. That tail IS the corpus's chrome —
// the lawyer's confidentiality notice reaches 56 documents, an email header
// template 32 — and it is only about 60 sampled shingles, which is why suppressing
// it costs so little.
//
// The cutoff must sit ABOVE the number of documents a genuinely reproduced
// instrument appears in, and that is what fixes the value. Measured here: the
// operative 2008 record of survey's text is detectably reproduced in 13 documents,
// 4 of them substantially (coverage above 0.25). A cutoff at or below 4 would begin
// masking the very instrument this exists to find, and that failure is silent.
//
// Measured across 8/12/18/25 on the all-pairs audit — 774 duplicate-or-containment
// pairs with no masking at all, then 51/57/57/60. The cutoff barely matters once
// masking exists; what the six extra pairs at 8 are is a judgment call rather than
// a correctness one. All six are VENDOR TEMPLATES — three Chicago Title order
// notifications for the same file number, two "Scanned from a Xerox Multifunction
// Printer" notices — so calling them duplicates is defensible and so is suppressing
// them. 12 keeps them (above the 4 substantive reproductions, below the 13
// incidental ones) and --generic-df exists because that judgment belongs to whoever
// is triaging, not to this constant.
const GenericDocFreq = 12

// genericShingles is the set of sampled shingles occurring in at least cutoff
// distinct documents.
func (s *Store) genericShingles(cutoff int) (map[uint64]bool, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT hash FROM (SELECT hash, COUNT(DISTINCT doc_id) AS df FROM shingle_index GROUP BY hash)
		 WHERE df >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]bool{}
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[uint64(h)] = true
	}
	return out, rows.Err()
}

// BuildMask locates a document's corpus-generic regions in folded offsets.
//
// Only a 1-in-mod sample of shingles is known to be generic, so a generic REGION is
// reconstructed by widening each generic sample's window. The widening is what
// makes this work rather than merely shred runs: sampled shingles inside a generic
// region are spaced about `mod` apart, so windows of width w abut only when
// mod <= w and leave gaps otherwise. Padding by mod on each side closes those gaps
// for any mod, and the cost is up to mod characters of real text at each region
// boundary — under the MinRunChars floor, so it cannot turn a real match into a
// miss on its own.
//
// Without the padding, at mod == w the masks met exactly and a one-character
// rounding left every other footer unmasked, which read as the cutoff being wrong.
func BuildMask(f FoldedText, generic map[uint64]bool, w, mod int) Mask {
	if len(generic) == 0 {
		return nil
	}
	var raw []iv
	for i, h := range Shingles(f.Body, w) {
		if !generic[h] {
			continue
		}
		lo, hi := i-mod, i+w+mod
		if lo < 0 {
			lo = 0
		}
		if hi > len(f.Body) {
			hi = len(f.Body)
		}
		raw = append(raw, iv{lo, hi})
	}
	return Mask(mergeIntervals(raw))
}

// mergeIntervals coalesces overlapping intervals into a sorted, disjoint set.
func mergeIntervals(ivs []iv) []iv {
	if len(ivs) == 0 {
		return nil
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].lo < ivs[j].lo })
	out := []iv{ivs[0]}
	for _, v := range ivs[1:] {
		last := &out[len(out)-1]
		if v.lo <= last.hi {
			if v.hi > last.hi {
				last.hi = v.hi
			}
			continue
		}
		out = append(out, v)
	}
	return out
}

// MinSharedSamples is how many sampled shingles a document (or one of its pages)
// must share with the probe to be rescored.
//
// At mod 32 an average page (3,600 folded chars) contributes ~90 samples, so 3
// samples is a few percent of one page — far below anything that would be reported,
// which is the point: phase 1 should over-generate and let the exact rescore decide.
//
// Measured with the operative record of survey as probe (100 sampled shingles):
// minShared 1 -> 19 candidate documents, 2 -> 16, 3 -> 16, 5 -> 11. So 3 costs
// nothing against 1 and 5 starts discarding candidates that would have been
// rescored. The whole probe (index load plus candidate selection) is 120 ms.
const MinSharedSamples = 3

// SimilarOpts are the knobs `raglit similar` exposes. Zero values mean defaults,
// so a caller can pass an empty struct.
type SimilarOpts struct {
	Width int // shingle width; 0 = FoldWidth
	Mod   int // sample divisor; 0 = SampleMod
	// MinChars is the matched-length floor for reporting, in folded characters.
	// 0 means MinReportChars.
	MinChars int
	// MinCoverage is an ADDITIONAL block-coverage floor, in either direction. 0
	// means no coverage floor — length is the primary gate (see MinReportChars).
	MinCoverage float64
	// Limit caps reported matches; 0 = unlimited.
	Limit int
	// Self keeps the probe's own path in the results. Off by default (a document
	// is trivially identical to itself); on for verifying a rebuild.
	Self bool
	// GenericDF overrides GenericDocFreq. Exposed because the right cutoff is a
	// property of the CORPUS — a corpus of 3,000 emails needs a higher one than a
	// corpus of 200 recorded instruments — and because a person who suspects a
	// duplicate was masked away needs to be able to raise it and look.
	GenericDF int
	// NoMask disables generic-text masking entirely. For diagnosing "why was this
	// not reported": if it appears with masking off and not on, its text is corpus
	// chrome and the finding was correct to suppress.
	NoMask bool
}

// MinReportChars is the least matched text worth reporting at all, in folded
// characters. Same value as MinClassifyChars, deliberately: a pair too small to
// say anything about is not worth a line of output either.
//
// Gating on matched LENGTH rather than on coverage, which was the first attempt
// and was wrong in both directions. A 0.05 coverage floor let through 1,440 pairs
// on this corpus whose median matched text was 75 folded characters — two
// shingles, a shared address line — while a floor high enough to kill those (0.20,
// leaving 150 pairs) would also discard a genuine paragraph-length quotation
// inside a long brief, which is 0.6% of a 40,000-character title commitment and is
// exactly the finding this corpus turns on. Length is the property that says
// whether there is anything there; coverage says what it means once there is.
//
// Measured: 250 folded characters takes the audit from 1,497 reported pairs to 257,
// of which 57 are duplicate-or-containment. It discards nothing strong — every
// duplicate and containment survives it, because they all rest on thousands of
// matched characters, not hundreds.
//
// 250 FOLDED characters is about 305 raw ones on this corpus's text (folding removes
// roughly 18% of it as spaces and punctuation), so the floor is a ~50-word passage. A
// shorter quotation than that is genuinely below it and needs --min-chars to surface;
// that is the cost, and it is the one this floor trades for not reporting 1,240 pairs
// whose median shared text is 75 characters.
const MinReportChars = MinClassifyChars

func (o SimilarOpts) width() int {
	if o.Width > 0 {
		return o.Width
	}
	return FoldWidth
}

func (o SimilarOpts) mod() int {
	if o.Mod > 0 {
		return o.Mod
	}
	return SampleMod
}

// genericDocFreq returns 0 when masking is off — genericShingles then matches
// nothing and BuildMask returns nil, so the whole mask path costs one empty query.
func (o SimilarOpts) genericDocFreq() int {
	if o.NoMask {
		return 1 << 30
	}
	if o.GenericDF > 0 {
		return o.GenericDF
	}
	return GenericDocFreq
}

func (o SimilarOpts) minChars() int {
	if o.MinChars > 0 {
		return o.MinChars
	}
	return MinReportChars
}

// SimilarTo compares folded probe text against the index and returns the report.
//
// Takes already-folded text rather than a path so the caller decides where the
// probe came from — an indexed document, a file on disk, or a raglit transcription
// sidecar. That mattered: making this open the file itself meant a probe could
// only be something raglit could extract, and the upload flow needs to compare
// text it already has in hand without writing it anywhere first.
func (s *Store) SimilarTo(probePath string, probe FoldedText, opts SimilarOpts) (SimilarReport, error) {
	w, mod := opts.width(), opts.mod()
	rep := SimilarReport{
		Probe: probePath, Pages: len(probe.Pages), Chars: len(probe.Body),
		Recipe: Recipe(w, mod),
	}
	all := Shingles(probe.Body, w)
	distinct := map[uint64]bool{}
	var sampled []uint64
	for _, h := range all {
		if distinct[h] {
			continue
		}
		distinct[h] = true
		if Sampled(h, mod) {
			sampled = append(sampled, h)
		}
	}
	rep.Shingled = len(distinct)
	if rep.Shingled == 0 {
		// Not "nothing similar" — "cannot tell". A blank scan or a page that is
		// pure figure folds to less than one shingle, and reporting that as a
		// clean result tells the upload flow to accept a document nothing checked.
		return rep, nil
	}

	generic, err := s.genericShingles(opts.genericDocFreq())
	if err != nil {
		return rep, err
	}
	cands, err := s.candidates(sampled, MinSharedSamples, generic)
	if err != nil {
		return rep, err
	}
	probeMask := BuildMask(probe, generic, w, mod)
	rep.GenericChars = maskLen(probeMask)
	minChars := opts.minChars()
	for _, f := range cands {
		if f.path == probePath && !opts.Self {
			continue
		}
		pages, err := s.TruePages(f.path)
		if err != nil {
			continue
		}
		mf := FoldPages(pages)
		dm := Compare(probe, mf, w, probeMask, BuildMask(mf, generic, w, mod))
		if dm.MatchedChars < minChars {
			continue
		}
		if opts.MinCoverage > 0 &&
			dm.BlockCoverProbe < opts.MinCoverage && dm.BlockCoverMatch < opts.MinCoverage {
			continue
		}
		dm.Path, dm.Title = f.path, f.title
		rep.Matches = append(rep.Matches, dm)
	}
	// Ordered by the strongest containment in either direction, because that is
	// what the caller acts on. Sorting by Jaccard put the wholly-contained exhibit
	// — the finding that matters — below a pair of unrelated documents on the same
	// county form.
	sort.SliceStable(rep.Matches, func(i, j int) bool {
		a, b := rep.Matches[i], rep.Matches[j]
		ka, kb := maxf(a.BlockCoverProbe, a.BlockCoverMatch), maxf(b.BlockCoverProbe, b.BlockCoverMatch)
		if ka != kb {
			return ka > kb
		}
		return a.Path < b.Path
	})
	if opts.Limit > 0 && len(rep.Matches) > opts.Limit {
		rep.Matches = rep.Matches[:opts.Limit]
	}
	return rep, nil
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// SimilarIndexed compares an already-indexed document against the rest of the
// index, and additionally reports any file byte-identical to it.
func (s *Store) SimilarIndexed(docPath string, opts SimilarOpts) (SimilarReport, error) {
	pages, err := s.TruePages(docPath)
	if err != nil {
		return SimilarReport{}, err
	}
	rep, err := s.SimilarTo(docPath, FoldPages(pages), opts)
	rep.Indexed = true
	if err != nil {
		return rep, err
	}
	same, herr := s.SameBytesAs(docPath)
	if herr != nil {
		return rep, herr
	}
	rep.Matches = markIdentical(rep.Matches, same)
	return rep, nil
}

// markIdentical promotes byte-identical matches, and adds any that similarity
// missed entirely.
//
// The second half is not hypothetical. A file consisting almost wholly of
// corpus-generic text — a bare cover sheet, a one-line notice with a footer —
// has nearly everything masked, so it can share no reportable run with its own
// exact copy and drop out of the shingle results completely. An exact-hash pair
// that similarity does not mention is precisely the case a triage tool must not
// miss, so it is added rather than only annotated.
func markIdentical(matches []DocMatch, same map[string]string) []DocMatch {
	if len(same) == 0 {
		return matches
	}
	seen := map[string]bool{}
	for i := range matches {
		if _, ok := same[matches[i].Path]; ok {
			matches[i].Relation = RelIdentical
			seen[matches[i].Path] = true
		}
	}
	var extra []DocMatch
	for p, title := range same {
		if seen[p] {
			continue
		}
		extra = append(extra, DocMatch{Path: p, Title: title, Relation: RelIdentical,
			Jaccard: 1, ContainProbe: 1, ContainMatch: 1})
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Path < extra[j].Path })
	// Identical first: it is the strongest claim available and the one that needs no
	// human comparison.
	return append(extra, matches...)
}

// SameBytesHash lists indexed documents whose recorded source hash equals hash.
//
// This is the upload case, and it is the cheapest true answer available: hash the
// file the client just sent and ask whether the index already holds those exact
// bytes. It runs before any extraction, so it needs no OCR, no model, and answers
// for a scanned PDF that raglit could not otherwise read at all — which is the one
// input the shingle path has to refuse.
func (s *Store) SameBytesHash(hash string) (map[string]string, error) {
	if hash == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT path, title FROM documents WHERE content_hash = ?`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// SameBytesAs lists other indexed documents whose source bytes hash the same,
// path -> title.
//
// Reusing documents.content_hash rather than adding anything: ingest already
// records it to skip re-indexing unchanged files, so exact duplication across
// PATHS was sitting in the index unqueried. On this corpus it names three pairs,
// one of which is a misfiled document —
// `2021-05-24-sale-disclosure-packet-COMPLETE-Form34-35-LotCert-Form17.pdf` is
// byte-identical to `2021-mls-listing-broker-remarks.pdf` and holds an MLS listing
// sheet, so the four instruments its filename claims are not in it.
//
// A document with an empty content_hash is skipped rather than matched: ” is
// "not recorded", and grouping on it would report every synthetic document as a
// duplicate of every other.
func (s *Store) SameBytesAs(docPath string) (map[string]string, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT b.path, b.title FROM documents a JOIN documents b
		   ON b.content_hash = a.content_hash AND b.id <> a.id
		 WHERE a.path = ? AND a.content_hash <> ''`, docPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// pageHeading matches RenderTranscription's `## Page N`. Written to accept any
// heading level, matching kgraph's `pageMark`, so the two tools cannot disagree
// about where a page starts.
var pageHeading = regexp.MustCompile(`^#{1,6}\s*Page\s+(\d+)\b`)

// transcriptionNote is raglit's OWN prose inside a page: the suspect-page warning
// and the empty-page placeholder.
//
// It has to go, and only it. Left in, every suspect page in the corpus carried the
// same 90-character sentence, which is over two shingles' worth of guaranteed
// agreement on exactly the pages whose OCR is least trustworthy — so the pages
// most likely to be a bad read were the ones most likely to be reported similar.
//
// Figure descriptions are also blockquoted (`> **figure:** …`) and must NOT be
// stripped: on a survey or a plat the figure description is most of the evidence
// on the page, and dropping it makes two different plats compare as two blank
// pages. So the match is anchored on the warning's own wording, not on the
// blockquote marker. A first pass dropped every `>` line and did exactly that.
var transcriptionNote = regexp.MustCompile(`(?m)^(?:>\s*⚠.*|_\(no text on this page\)_)$`)

// ParseTranscription reads a raglit transcription sidecar back into pages.
//
// The inverse of RenderTranscription, and the reason it exists is the upload case:
// a client sends a file whose text this index has never read, but whose sidecar is
// already beside it from a previous ingest elsewhere. Re-OCRing it to compare
// would need a vision model, which puts the whole check behind a network call and
// out of CI. The sidecar's `## Page N` headings are the documented contract, so
// parsing them is safe — and a heading whose number does not parse is skipped
// rather than guessed at, because a wrong page number in an alignment is worse
// than a missing one.
func ParseTranscription(md string) []PageText {
	var out []PageText
	cur := -1
	var buf []string
	flush := func() {
		if cur >= 0 {
			t := transcriptionNote.ReplaceAllString(strings.Join(buf, "\n"), "")
			out = append(out, PageText{Page: cur, Text: strings.TrimSpace(t), Engine: "text"})
		}
		buf = nil
	}
	for _, ln := range strings.Split(md, "\n") {
		if m := pageHeading.FindStringSubmatch(ln); m != nil {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			if n > 0 {
				flush()
				cur = n
			}
			continue
		}
		if cur >= 0 {
			buf = append(buf, ln)
		}
	}
	flush()
	return out
}

// IdenticalGroups returns every set of indexed documents whose SOURCE BYTES are
// the same, as groups of paths, each group sorted and the groups sorted by their
// first path.
//
// This is the one relation in the corpus that needs no shingles, no thresholds
// and no judgement. Two files with one sha256 are the same document — not
// similar to it, not probably it — so the answer does not depend on any of the
// tuning the rest of this file is careful about, and it is available on an index
// that has never been sketched.
//
// Grouped rather than paired because a document held three times is ONE finding.
// Emitting three pairs invites a reader to rule on them separately and reach two
// different answers about one set of bytes.
func (s *Store) IdenticalGroups() ([][]string, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT content_hash, path FROM documents
		  WHERE content_hash <> ''
		    AND content_hash IN (
		      SELECT content_hash FROM documents
		       WHERE content_hash <> '' GROUP BY content_hash HAVING count(*) > 1)
		  ORDER BY content_hash, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byHash := map[string][]string{}
	var order []string
	for rows.Next() {
		var h, p string
		if err := rows.Scan(&h, &p); err != nil {
			return nil, err
		}
		if _, seen := byHash[h]; !seen {
			order = append(order, h)
		}
		byHash[h] = append(byHash[h], p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([][]string, 0, len(order))
	for _, h := range order {
		if g := byHash[h]; len(g) > 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}
