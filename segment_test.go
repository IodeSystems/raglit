package raglit

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iodesystems/agentkit/llm"
)

// scriptChatter returns canned replies in sequence, one per Chat call.
type scriptChatter struct {
	replies []string
	calls   int
}

func (c *scriptChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	r := ""
	if c.calls < len(c.replies) {
		r = c.replies[c.calls]
	}
	c.calls++
	return streamReply(r), nil
}

func TestSegmenter_ParsesValidJSON(t *testing.T) {
	c := &scriptChatter{replies: []string{
		"```json\n{\"continues_previous\":false,\"fragments\":[{\"text\":\"alpha\"},{\"text\":\"bravo\"}]}\n```",
	}}
	r, err := NewSegmenter(c).SegmentText(context.Background(), "some text", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ContinuesPrevious || len(r.Fragments) != 2 || r.Fragments[0].Text != "alpha" {
		t.Fatalf("bad parse: %+v", r)
	}
}

func TestSegmenter_FixLoopRetries(t *testing.T) {
	c := &scriptChatter{replies: []string{
		"not json at all", // attempt 0: invalid
		`{"continues_previous":true,"fragments":[{"text":"x"}]}`, // attempt 1: valid
	}}
	r, err := NewSegmenter(c).SegmentText(context.Background(), "t", "open")
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 {
		t.Fatalf("expected a retry, got %d calls", c.calls)
	}
	if !r.ContinuesPrevious || len(r.Fragments) != 1 {
		t.Fatalf("bad retry result: %+v", r)
	}
}

func TestSegmenter_FallsBackToWholeUnit(t *testing.T) {
	c := &scriptChatter{replies: []string{"garbage", "still garbage", "nope"}}
	sg := NewSegmenter(c)
	sg.MaxRetries = 2 // 3 tries total, all bad
	r, err := sg.SegmentText(context.Background(), "the whole window text", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fragments) != 1 || r.Fragments[0].Text != "the whole window text" {
		t.Fatalf("fallback should be the whole unit: %+v", r)
	}
	// The fallback is a nil error and a plausible result. Without a reason coming
	// out with it, a document finishes "done" with pages returned as undivided
	// blocks and nothing anywhere says how many or why.
	if r.Degraded == "" {
		t.Fatal("fell back to the whole unit and reported it as a clean segmentation")
	}
}

// A unit the model DID segment must not be marked degraded — an alarm that fires
// on healthy documents is one nobody reads.
func TestSegmenter_CleanResultIsNotMarkedDegraded(t *testing.T) {
	c := &scriptChatter{replies: []string{`{"continues_previous":false,"fragments":[{"text":"a"},{"text":"b"}]}`}}
	r, err := NewSegmenter(c).SegmentText(context.Background(), "text", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Degraded != "" {
		t.Fatalf("a clean segmentation was marked degraded: %q", r.Degraded)
	}
}

// The degradation has to survive out of the pipeline with its page attached, or
// the stage row can say something happened but not where.
func TestSegmentReportCarriesDegradedPages(t *testing.T) {
	pages := []resolvedPage{{page: 1, text: strings.Repeat("clean page. ", 40)},
		{page: 2, text: strings.Repeat("stubborn page. ", 40)}}
	seg := func(_ context.Context, text, _ string) (SegResult, error) {
		if strings.Contains(text, "stubborn") {
			return SegResult{Fragments: []Segment{{Text: text}}, Degraded: "no fragments (after 3 attempt(s))"}, nil
		}
		return SegResult{Fragments: []Segment{{Text: text}}}, nil
	}
	rep, err := segmentLLMWith(context.Background(), seg, pages, nil, nil, func(stagedFrag) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.degraded) != 1 || rep.degraded[0].page != 2 {
		t.Fatalf("report did not name the degraded page: %+v", rep.degraded)
	}
	if rep.requests != 2 {
		t.Errorf("requests = %d, want 2", rep.requests)
	}
	if d := rep.degradedDetail(); !strings.Contains(d, "no fragments") || !strings.Contains(d, "2") {
		t.Errorf("stage detail loses the page or the reason: %q", d)
	}
}

// The heart of the design: an open fragment is DEFERRED across a unit boundary
// and merged when the next unit continues it — and it is never sinked (embedded)
// until it closes.
func TestAssembler_DefersAndMergesOpenFragment(t *testing.T) {
	type sunk struct {
		page, ord int
		text      string
	}
	var got []sunk
	a := NewAssembler(func(page, ord int, text string, _ []PageSpan) error {
		got = append(got, sunk{page, ord, text})
		return nil
	})
	a.MinChars = 0 // test pure continuation, not the size floor

	// Page 1: [A, B]. A closes; B is the open (deferred) fragment.
	if err := a.Feed(1, SegResult{Fragments: []Segment{{Text: "A"}, {Text: "B"}}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].text != "A" {
		t.Fatalf("after page1 only A should be sinked (B deferred): %+v", got)
	}

	// Page 2: continues → first fragment C extends B; then D closes B\n\nC.
	if err := a.Feed(2, SegResult{ContinuesPrevious: true, Fragments: []Segment{{Text: "C"}, {Text: "D"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("want 3 finalized fragments, got %d: %+v", len(got), got)
	}
	// A(p1,o0), then the merged B\n\nC keeping B's start position (p1,o1), then D(p2,o0).
	if got[0] != (sunk{1, 0, "A"}) {
		t.Errorf("frag0 = %+v", got[0])
	}
	if got[1].page != 1 || got[1].ord != 1 || !strings.Contains(got[1].text, "B") || !strings.Contains(got[1].text, "C") {
		t.Errorf("merged fragment wrong (should be B+C at p1/o1): %+v", got[1])
	}
	if got[2] != (sunk{2, 0, "D"}) {
		t.Errorf("frag2 = %+v", got[2])
	}
}

func TestAssembler_NonContinuationClosesOpen(t *testing.T) {
	var texts []string
	a := NewAssembler(func(_, _ int, text string, _ []PageSpan) error {
		texts = append(texts, text)
		return nil
	})
	a.MinChars = 0                                                                    // pure continuation, not the size floor
	a.Feed(1, SegResult{Fragments: []Segment{{Text: "P"}}})                           // P open
	a.Feed(2, SegResult{ContinuesPrevious: false, Fragments: []Segment{{Text: "Q"}}}) // P closes, Q open
	a.Close()
	if len(texts) != 2 || texts[0] != "P" || texts[1] != "Q" {
		t.Fatalf("non-continuation should keep P and Q separate: %v", texts)
	}
}

// The size floor: sub-floor sibling fragments are absorbed into the open one
// (up to the ceiling) rather than emitted as tiny fragments — so a hit always
// carries enough context to concept-chain.
func TestAssembler_SizeFloorMergesSmallSiblings(t *testing.T) {
	var got []string
	a := NewAssembler(func(_, _ int, text string, _ []PageSpan) error {
		got = append(got, text)
		return nil
	})
	a.MinChars = 10 // floor
	a.MaxChars = 30 // ceiling

	// Three small siblings on one unit: "aaa","bbb","ccc" (3 chars each). The
	// first two merge toward the floor; the third keeps absorbing (still < 30).
	a.Feed(1, SegResult{Fragments: []Segment{{Text: "aaa"}, {Text: "bbb"}, {Text: "ccc"}}})
	a.Close()
	if len(got) != 1 {
		t.Fatalf("small siblings should merge into one fragment, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "aaa") || !strings.Contains(got[0], "ccc") {
		t.Fatalf("merged fragment lost content: %q", got[0])
	}
}

func TestAssembler_CeilingStopsAbsorption(t *testing.T) {
	var got []string
	a := NewAssembler(func(_, _ int, text string, _ []PageSpan) error {
		got = append(got, text)
		return nil
	})
	a.MinChars = 100 // floor high...
	a.MaxChars = 12  // ...but ceiling low: absorbing a second block would overflow
	a.Feed(1, SegResult{Fragments: []Segment{{Text: "eight888"}, {Text: "nine99999"}}})
	a.Close()
	if len(got) != 2 {
		t.Fatalf("ceiling should stop absorption → 2 fragments, got %d: %v", len(got), got)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                       `{"a":1}`,
		"```json\n{\"a\":1}\n```":       `{"a":1}`,
		"here you go: {\"a\":1} thanks": `{"a":1}`,
		"```\n{\"a\":[1,2]}\n```":       `{"a":[1,2]}`,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// The first BALANCED object, not the first brace to the last one.
//
// These are the replies that were actually failing in the field, not invented
// shapes: 239 of 290 failed identity jobs on the delano index reported
// `invalid character 'X' after top-level value`, which is what json says when
// it is handed one object followed by anything at all.
func TestExtractJSON_StopsAtTheEndOfTheFirstObject(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"a second object after the first": {
			`{"name":"A deed"}` + "\n" + `{"name":"A deed"}`,
			`{"name":"A deed"}`,
		},
		"objects run together with a comma": {
			`{"name":"A deed"},{"name":"another"}`,
			`{"name":"A deed"}`,
		},
		"a trailing note after the answer": {
			`{"name":"A deed"}` + "\n\nLet me know if you want more detail.",
			`{"name":"A deed"}`,
		},
		"prose with braces before the answer": {
			`I will use the shape {like this}: {"name":"A deed"}`,
			`{"name":"A deed"}`,
		},
		"a nested object is not cut short": {
			`{"a":{"b":{"c":1}},"d":2} trailing`,
			`{"a":{"b":{"c":1}},"d":2}`,
		},
		// A summary quoting a document is the common case here, and legal text
		// carries braces. Counting one inside a string as structure ends the
		// object early and produces JSON that is invalid for a reason nothing in
		// the reply explains.
		"braces inside a string are content": {
			`{"summary":"the clause reads {redacted} in the original"} and that is all`,
			`{"summary":"the clause reads {redacted} in the original"}`,
		},
		"an escaped quote does not end the string": {
			`{"summary":"he said \"no\" twice"}x`,
			`{"summary":"he said \"no\" twice"}`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := extractJSON(c.in); got != c.want {
				t.Errorf("extractJSON(%q)\n = %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// When a reply holds no object at all, hand back the reply. The caller reports
// what it could not parse, and the useful version of that message quotes what
// the model actually said.
func TestExtractJSON_NoObject(t *testing.T) {
	for _, in := range []string{"/home/nthalk/some/path", "I cannot answer that.", ""} {
		if got := extractJSON(in); got != in {
			t.Errorf("extractJSON(%q) = %q, want it unchanged", in, got)
		}
	}
}

// An unclosed fence is not a fence. Stripping the opening marker and keeping
// everything after it turns a reply that merely MENTIONS one into a truncated
// reply, and the object that followed goes missing.
func TestExtractJSON_UnclosedFence(t *testing.T) {
	in := "use ```json like so\n" + `{"a":1}`
	if got, want := extractJSON(in), `{"a":1}`; got != want {
		t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
	}
}

// A malformed object still comes back as the OBJECT. A raw newline inside a
// string literal is the model's error, not the extractor's, and the validator's
// message about it is only useful if it is pointed at the object rather than at
// the whole reply.
func TestExtractJSON_MalformedObjectIsStillIsolated(t *testing.T) {
	in := "here: {\"summary\":\"line one\nline two\"} — done"
	got := extractJSON(in)
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("extractJSON(%q) = %q, want just the object", in, got)
	}
	if strings.Contains(got, "here:") || strings.Contains(got, "done") {
		t.Errorf("the prose came back too: %q", got)
	}
}

// A fragment that stitches across a page boundary must record WHERE the next
// page begins inside it.
//
// The assembler absorbs a continuation, or a sub-floor sibling, from the
// following page into the open fragment and keeps only the start page. That made
// `fragments.page` correct for a fragment's beginning and wrong for the rest of
// it, so a search hit inside a stitched fragment resolved to the wrong page —
// which defeats the point of storing a page at all.
func TestStitchedFragmentRecordsPageBoundaries(t *testing.T) {
	var gotPage, gotOrd int
	var gotText string
	var gotSpans []PageSpan
	a := NewAssembler(func(page, ord int, text string, spans []PageSpan) error {
		gotPage, gotOrd, gotText, gotSpans = page, ord, text, spans
		return nil
	})
	a.MinChars = 0 // no absorption; exercise the continuation path alone

	if err := a.Feed(4, SegResult{Fragments: []Segment{{Text: "ends mid-sentence and"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Feed(5, SegResult{ContinuesPrevious: true,
		Fragments: []Segment{{Text: "carries on over here"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if gotPage != 4 {
		t.Errorf("the fragment still starts on page 4, got %d", gotPage)
	}
	if len(gotSpans) != 2 {
		t.Fatalf("a fragment spanning two pages needs two boundaries, got %+v", gotSpans)
	}
	if gotSpans[0] != (PageSpan{Off: 0, Page: 4}) {
		t.Errorf("first boundary must be the start page at offset 0, got %+v", gotSpans[0])
	}
	if gotSpans[1].Page != 5 {
		t.Errorf("second boundary must name page 5, got %+v", gotSpans[1])
	}
	// The offset must actually point at page 5's text, not merely be non-zero.
	if got := gotText[gotSpans[1].Off:]; got != "carries on over here" {
		t.Errorf("boundary offset does not land on page 5's text: %q", got)
	}
	// And the resolution this exists for.
	if p := PageAt(gotPage, gotSpans, 0); p != 4 {
		t.Errorf("offset 0 is on page 4, got %d", p)
	}
	if p := PageAt(gotPage, gotSpans, gotSpans[1].Off+3); p != 5 {
		t.Errorf("an offset past the boundary is on page 5, got %d", p)
	}
	_ = gotOrd
}

// A fragment wholly on one page stores nothing, so the column costs nothing in
// the common case and `page` alone stays authoritative.
func TestSinglePageFragmentStoresNoSpans(t *testing.T) {
	var spans []PageSpan
	a := NewAssembler(func(_, _ int, _ string, s []PageSpan) error { spans = s; return nil })
	if err := a.Feed(2, SegResult{Fragments: []Segment{{Text: "all on one page"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if spans != nil {
		t.Errorf("a single-page fragment needs no boundaries, got %+v", spans)
	}
	if encodePageSpans(spans) != "" {
		t.Error("nothing should be persisted for a single-page fragment")
	}
}

// growingChatter records the total prompt bytes of every attempt, so a test can
// assert the fix loop does not feed the model its own failures back.
type growingChatter struct {
	promptBytes []int
	replies     []string
	n           int
}

func (g *growingChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		for _, p := range m.Parts {
			total += len(p.Text)
		}
	}
	g.promptBytes = append(g.promptBytes, total)
	r := g.replies[min(g.n, len(g.replies)-1)]
	g.n++
	return streamReply(r), nil
}

// The fix loop used to append each failed answer IN FULL. On the input that
// triggers it — a cut generation, cut because it ran long — that is unbounded
// growth, and twelve jobs on a live corpus died at "request (180273 tokens)
// exceeds the available context size (180224)". The document was not the
// problem; the retry was.
func TestSegmenterFixLoopDoesNotGrowTheContextByTheFailedAnswer(t *testing.T) {
	huge := strings.Repeat("NOT JSON. ", 6000) // ~60 KB per failed answer
	gc := &growingChatter{replies: []string{huge, huge, huge}}
	sg := NewSegmenter(gc)

	if _, err := sg.SegmentText(context.Background(), "a page of text", ""); err != nil {
		t.Fatalf("segmentation should fall back, not error: %v", err)
	}
	if len(gc.promptBytes) < 3 {
		t.Fatalf("expected the fix loop to retry, saw %d attempts", len(gc.promptBytes))
	}
	first, last := gc.promptBytes[0], gc.promptBytes[len(gc.promptBytes)-1]
	// Each retry may add a bounded excerpt plus an instruction; it must not add
	// the whole answer. Two retries of a 60 KB answer would be +120 KB.
	if grew := last - first; grew > 8*retryExcerptChars {
		t.Errorf("the fix loop grew the prompt by %d bytes across %d attempts — "+
			"it is feeding failed answers back in full", grew, len(gc.promptBytes))
	}
}

// The excerpt still has to carry enough for the model to see what it did.
func TestExcerptForRetryKeepsTheHeadAndMarksTheCut(t *testing.T) {
	short := "{\"fragments\":[]}"
	if got := excerptForRetry(short); got != short {
		t.Errorf("a short answer must pass through unchanged, got %q", got)
	}
	long := strings.Repeat("x", retryExcerptChars*3)
	got := excerptForRetry(long)
	if len(got) >= len(long) {
		t.Error("a long answer was not truncated")
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Error("the head of the answer was not kept")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the truncation was not marked, so the model cannot tell it was cut")
	}
}

// The deterministic windower is capped by ResolveFragParams; the LLM path was
// not. It asks for "roughly 400-800 words" and takes whatever comes back, so a
// dense page could yield a fragment no embedder would accept — and the failure
// surfaced as a 500 about batch sizes, with the whole document lost.
func TestSplitOversizedBoundsAModelsFragments(t *testing.T) {
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 400) // ~17.6k chars
	in := []Segment{{Text: "short one"}, {Text: long}, {Text: "short two"}}
	b := NewTokenBudget(context.Background(), &fakeTokenizer{charsPerToken: 4.0}, 1000)
	out := SplitOversized(context.Background(), b, in)

	if len(out) <= len(in) {
		t.Fatalf("the oversized fragment was not split: %d -> %d", len(in), len(out))
	}
	for i, f := range out {
		if n, _ := b.counter.CountTokens(context.Background(), f.Text); n > 1000 {
			t.Errorf("fragment %d is %d tokens, over the 1000 limit", i, n)
		}
	}
	// Splitting, not truncating: a truncated fragment is indexed, searchable and
	// quietly missing its tail.
	var joined int
	for _, f := range out {
		joined += len(strings.ReplaceAll(f.Text, " ", ""))
	}
	want := len(strings.ReplaceAll("short one"+long+"short two", " ", ""))
	if joined < want-50 {
		t.Errorf("content was lost: kept %d non-space chars of %d", joined, want)
	}
}

// A fragment already inside the limit is passed through untouched, so ordinary
// documents are unaffected.
func TestSplitOversizedLeavesNormalFragmentsAlone(t *testing.T) {
	in := []Segment{{Text: "one"}, {Text: "two"}}
	b := NewTokenBudget(context.Background(), &fakeTokenizer{charsPerToken: 4.0}, 1000)
	out := SplitOversized(context.Background(), b, in)
	if len(out) != 2 || out[0].Text != "one" || out[1].Text != "two" {
		t.Errorf("normal fragments were altered: %+v", out)
	}
	if got := SplitOversized(context.Background(), NewTokenBudget(context.Background(), nil, 0), in); len(got) != 2 {
		t.Errorf("an unknown limit must be a no-op, got %+v", got)
	}
}

// Cuts prefer a paragraph, then a sentence, then whitespace — a piece should
// rarely end mid-word and must never end mid-rune.
func TestSplitAtBoundaryPrefersRealBreaks(t *testing.T) {
	s := strings.Repeat("alpha beta gamma. ", 60) // sentences every 18 chars
	for _, p := range splitAtBoundary(s, 200) {
		if !utf8.ValidString(p) {
			t.Fatalf("split produced invalid UTF-8: %q", p)
		}
		if strings.HasSuffix(p, "alph") || strings.HasSuffix(p, "gam") {
			t.Errorf("cut mid-word: %q", p[max(0, len(p)-20):])
		}
	}
	// An unbroken run has no boundary to find and must still be cut safely.
	for _, p := range splitAtBoundary(strings.Repeat("é", 500), 100) {
		if !utf8.ValidString(p) {
			t.Fatalf("cut a multi-byte rune in half: %q", p)
		}
	}
}
