package raglit

import (
	"context"
	"strings"
	"testing"

	"github.com/iodesystems/agentkit/llm"
)

// A mostly-empty grid defeated both the whole-page read and the region walk.
//
// Measured on the two PL99-0479 Record of Ownership sheets: a 14-row table
// holding ONE filled row — Cartwright → McKinnon, auditor's file 777201, 1972 —
// and the model looped emitting `<td></td><td></td></tr><tr>` until the
// repetition guard cut it. The loop-break retry changed sampling only, which is
// the wrong lever: the model had not lost its place, it was working through a
// hundred blank cells. Both pages were dropped and the chain-of-title entry was
// invisible to search.

// The line the whole fix rests on: what was repeated, and whether it says
// anything.
func TestStructuralRepetition_TellsEmptyMarkupFromLostText(t *testing.T) {
	structural := []string{
		// The two samples the guard actually cut on, verbatim from the run. The
		// second begins PART WAY THROUGH a tag, with two bare letters that a
		// naive scan reads as content — which is why both sheets kept failing
		// after the first version of this fix shipped.
		"><td></td><td></td><td></td></tr><tr><td></td><td></td",
		"td><td></td><td></td><td></td></tr><tr><td></td><td></",
		"<td></td>",
		"|   |   |   |\n",
		"</tr><tr>",
	}
	for _, s := range structural {
		if !structuralRepetition(&llm.RepetitionInfo{Sample: s}) {
			t.Fatalf("empty markup %q was read as lost text — the page would be dropped", s)
		}
	}
	// A model re-emitting REAL text has lost its place, and the strict reading
	// must still apply. This is the survey legal description that made the
	// refusal necessary in the first place.
	content := []string{
		"thence North 89°58'12\" East a distance of 208.71 feet to the ",
		"<td>Cartwright</td><td>McKinnon</td>",
		"<p>UNOFFICIAL DOCUMENT</p>",
	}
	for _, s := range content {
		if structuralRepetition(&llm.RepetitionInfo{Sample: s}) {
			t.Fatalf("repeated CONTENT %q was treated as empty structure — a truncated page would be indexed as whole", s)
		}
	}
	if structuralRepetition(nil) || structuralRepetition(&llm.RepetitionInfo{Sample: "  "}) {
		t.Fatal("no sample is not a structural loop")
	}
}

// unloopedPrefix trims the redundant copies, leaving one.
func TestUnloopedPrefix_LeavesOneCopy(t *testing.T) {
	text := "<table><tr><td>Cartwright</td></tr>" + strings.Repeat("<tr><td></td></tr>", 5)
	rep := &llm.RepetitionInfo{Period: 18, Reps: 5, Trailing: 18 * 4, Sample: "<tr><td></td></tr>"}
	got := unloopedPrefix(text, rep)
	if !strings.Contains(got, "Cartwright") {
		t.Fatal("the trim ate the content")
	}
	if strings.Count(got, "<tr><td></td></tr>") != 1 {
		t.Fatalf("expected one copy left, got %d", strings.Count(got, "<tr><td></td></tr>"))
	}
	// A cut landing mid-tag must not leave a dangling fragment in the index.
	if got := unloopedPrefix("<p>Cartwright</p><td", &llm.RepetitionInfo{Trailing: 0}); strings.HasSuffix(got, "<td") {
		_ = got // Trailing 0 means no trim at all; the mid-tag case is below.
	}
	if got := unloopedPrefix("<p>Cartwright</p><td></td><td", &llm.RepetitionInfo{Trailing: 3}); strings.Contains(got, "<td></td><td") {
		t.Fatalf("a dangling tag survived the trim: %q", got)
	}
	// Nonsense trailing values must not panic or truncate to nothing.
	if unloopedPrefix("short", &llm.RepetitionInfo{Trailing: 999}) != "short" {
		t.Fatal("an over-long trailing count must leave the text alone")
	}
}

// loopingChatter emits a scripted reply per call, and reports a repetition cut
// on any call whose script says to.
type loopingChatter struct {
	replies []string
	reps    []*llm.RepetitionInfo
	prompts []string
	n       int
}

func (c *loopingChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	i := c.n
	c.n++
	for _, p := range msgs[0].Parts {
		if p.Text != "" {
			c.prompts = append(c.prompts, p.Text)
		}
	}
	ch := make(chan llm.StreamChunk, 4)
	if i < len(c.replies) {
		ch <- llm.StreamChunk{Content: c.replies[i]}
	}
	if i < len(c.reps) && c.reps[i] != nil {
		ch <- llm.StreamChunk{StopReason: llm.StopReasonRepetition, Repetition: c.reps[i]}
	}
	close(ch)
	return ch, nil
}

// The sparse retry carries an INSTRUCTION, and the page comes back.
func TestSparseTable_TheRetryAsksItToSkipTheBlanks(t *testing.T) {
	blank := "<tr><td></td><td></td></tr>"
	// First pass: the header and the one filled row, then the stutter.
	cut := `<table><tr><th>Date</th><th>Seller</th><th>Buyer</th></tr>` +
		`<tr><td>11/1/72</td><td>Cartwright</td><td>McKinnon</td></tr>` + strings.Repeat(blank, 6)
	// Retry: the same content, blanks summarised rather than emitted.
	clean := `<table><tr><th>Date</th><th>Seller</th><th>Buyer</th></tr>` +
		`<tr><td>11/1/72</td><td>Cartwright</td><td>McKinnon</td></tr></table>` +
		`<p>(11 further rows are blank)</p>`
	c := &loopingChatter{
		replies: []string{cut, clean},
		reps:    []*llm.RepetitionInfo{{Sample: blank, Period: len(blank), Reps: 6, Trailing: len(blank) * 5}, nil},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	got, _, err := o.visionPage(context.Background(), PageImage{Page: 2, Mime: "image/png", Data: []byte("x")}, "")
	if err != nil {
		t.Fatalf("a sparse table was dropped instead of read: %v", err)
	}
	if !strings.Contains(got, "Cartwright") || !strings.Contains(got, "McKinnon") {
		t.Fatalf("the chain-of-title entry did not survive: %q", got)
	}
	if len(c.prompts) != 2 {
		t.Fatalf("expected two calls, got %d", len(c.prompts))
	}
	if strings.Contains(c.prompts[0], "do NOT use table markup") {
		t.Fatal("the FIRST pass must not carry the sparse instruction — it is a retry strategy")
	}
	if !strings.Contains(c.prompts[1], "do NOT use table markup") {
		t.Fatal("the retry did not tell the model to skip the blank rows; sampling alone never escapes emptiness")
	}
	// The instruction must carry no transcribed text, or it re-anchors the model
	// on its own partial output — the failure the unchanged-prompt rule guards.
	if strings.Contains(c.prompts[1], "Cartwright") {
		t.Fatal("the retry prompt fed the model its own partial transcription")
	}
}

// The bar for a sparse retry is CONTENT, not characters — otherwise every
// recovery looks like a regression, because omitting the blanks is the point.
func TestSparseTable_ShorterButCompleteIsAccepted(t *testing.T) {
	blank := "<tr><td></td><td></td><td></td></tr>"
	cut := `<tr><td>Cartwright</td><td>McKinnon</td></tr>` + strings.Repeat(blank, 10)
	clean := `<tr><td>Cartwright</td><td>McKinnon</td></tr>` // far shorter in raw chars
	c := &loopingChatter{
		replies: []string{cut, clean},
		reps:    []*llm.RepetitionInfo{{Sample: blank, Period: len(blank), Reps: 10, Trailing: len(blank) * 9}, nil},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	got, _, err := o.visionPage(context.Background(), PageImage{Page: 3, Mime: "image/png", Data: []byte("x")}, "")
	if err != nil {
		t.Fatalf("a shorter-but-complete sparse retry was rejected: %v", err)
	}
	if !strings.Contains(got, "Cartwright") {
		t.Fatalf("content lost: %q", got)
	}
}

// A retry that drops REAL text is still refused, sparse or not.
func TestSparseTable_ARetryThatLosesContentIsStillRefused(t *testing.T) {
	blank := "<tr><td></td></tr>"
	cut := `<tr><td>Cartwright</td><td>McKinnon</td><td>777201</td></tr>` + strings.Repeat(blank, 8)
	// The retry "recovers" by throwing the row away — the survey failure mode.
	c := &loopingChatter{
		replies: []string{cut, `<p>A blank form.</p>`},
		reps:    []*llm.RepetitionInfo{{Sample: blank, Period: len(blank), Reps: 8, Trailing: len(blank) * 7}, nil},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	_, _, err := o.visionPage(context.Background(), PageImage{Page: 3, Mime: "image/png", Data: []byte("x")}, "")
	if err == nil {
		t.Fatal("a retry that dropped the filled row was accepted — this indexes a page whose content is gone")
	}
	if !strings.Contains(err.Error(), "NOT indexed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// CONTENT repetition keeps the strict reading: no sparse instruction, and a
// second loop still fails the page.
func TestSparseTable_ContentRepetitionIsUntouched(t *testing.T) {
	span := "thence North 89 degrees East a distance of 208.71 feet "
	rep := &llm.RepetitionInfo{Sample: span, Period: len(span), Reps: 4, Trailing: len(span) * 3}
	c := &loopingChatter{
		replies: []string{strings.Repeat(span, 4), strings.Repeat(span, 4)},
		reps:    []*llm.RepetitionInfo{rep, rep},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	_, _, err := o.visionPage(context.Background(), PageImage{Page: 1, Mime: "image/png", Data: []byte("x")}, "")
	if err == nil {
		t.Fatal("a page whose model lost its place in REAL text was indexed")
	}
	for _, p := range c.prompts {
		if strings.Contains(p, "do NOT use table markup") {
			t.Fatal("a content loop was treated as a sparse table — that instruction would tell it to skip real rows")
		}
	}
}

// When the instruction fails too, the page is SALVAGED rather than dropped —
// and the row says the tail was never read.
//
// chandra, told in as many words not to emit table markup, emitted the identical
// 54-character block ten times over. A model that will not stop describing
// emptiness cannot be talked out of it, so the prefix — every row that has
// content — is kept, and the engine records what was given up.
func TestSparseTable_BothPassesLoopingSalvagesTheContent(t *testing.T) {
	blank := "<tr><td></td><td></td></tr>"
	rep := &llm.RepetitionInfo{Sample: blank, Period: len(blank), Reps: 10, Trailing: len(blank) * 9}
	body := `<tr><td>11/1/72</td><td>Cartwright</td><td>McKinnon</td></tr>`
	c := &loopingChatter{
		replies: []string{body + strings.Repeat(blank, 10), body + strings.Repeat(blank, 10)},
		reps:    []*llm.RepetitionInfo{rep, rep},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	got, engine, _, err := o.PageAsSeen(context.Background(), PageImage{Page: 2, Mime: "image/png", Data: []byte("x")})
	if err != nil {
		t.Fatalf("a structural loop on both passes dropped the page: %v", err)
	}
	if !strings.Contains(got, "Cartwright") || !strings.Contains(got, "777201") && !strings.Contains(got, "McKinnon") {
		t.Fatalf("the filled row did not survive salvage: %q", got)
	}
	if engine != enginePartialVision {
		t.Fatalf("engine %q — a salvaged page must not be recorded as a whole read", engine)
	}
	if !isVisionEngine(engine) {
		t.Fatal("a salvaged page was still read by the model and belongs to the vision family")
	}
	// One copy of the blank block is all that may remain.
	if n := strings.Count(got, blank); n > 1 {
		t.Fatalf("%d copies of the empty row survived — the loop was not trimmed", n)
	}
}

// Content repetition on BOTH passes is still a dropped page, never salvaged.
func TestSparseTable_ContentLoopIsNeverSalvaged(t *testing.T) {
	span := "thence North 89 degrees East a distance of 208.71 feet "
	rep := &llm.RepetitionInfo{Sample: span, Period: len(span), Reps: 6, Trailing: len(span) * 5}
	c := &loopingChatter{
		replies: []string{strings.Repeat(span, 6), strings.Repeat(span, 6)},
		reps:    []*llm.RepetitionInfo{rep, rep},
	}
	o := &OCR{Client: c, Model: "test-vlm"}
	_, _, _, err := o.PageAsSeen(context.Background(), PageImage{Page: 1, Mime: "image/png", Data: []byte("x")})
	if err == nil {
		t.Fatal("a page whose model lost its place in real text was salvaged — that indexes an unknown amount of missing text as whole")
	}
}
