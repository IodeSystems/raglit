package raglit

import "testing"

func rs(level string, doc string, at int64) Reading {
	return Reading{SourceSHA256: "rec", SourcePath: "/h.mp4", DocPath: doc, Level: level, At: at}
}

// Ranking must not decide which reading answers. An unverified transcript that
// happens to score higher is the case this exists to stop.
func TestCollapse_RankingDoesNotChooseTheReading(t *testing.T) {
	all := []Reading{rs(ReadingMachine, "/h.mp4", 1), rs(ReadingAttested, "/h.verified.md", 2)}
	byDoc := map[string]Reading{}
	for _, r := range all {
		byDoc[r.DocPath] = r
	}
	readingOf := func(d string) (Reading, bool) { r, ok := byDoc[d]; return r, ok }
	readingsOf := func(string) []Reading { return all }
	none := func(string) (string, bool) { return "", false }

	// The machine reading ranks FIRST. It must still lose.
	hits := []Hit{{Path: "/h.mp4", Score: 9}, {Path: "/h.verified.md", Score: 3}}
	got := CollapseToAuthoritative(hits, readingOf, readingsOf, none)
	if len(got) != 1 {
		t.Fatalf("want one hit per source, got %d", len(got))
	}
	if got[0].Path != "/h.verified.md" {
		t.Fatalf("the higher-scoring MACHINE reading won: %s", got[0].Path)
	}
}

// A person's ruling beats the level order, in both directions.
func TestCollapse_ARulingBeatsTheDefaultOrder(t *testing.T) {
	all := []Reading{rs(ReadingMachine, "/h.mp4", 1), rs(ReadingAttested, "/h.verified.md", 2)}
	byDoc := map[string]Reading{}
	for _, r := range all {
		byDoc[r.DocPath] = r
	}
	readingOf := func(d string) (Reading, bool) { r, ok := byDoc[d]; return r, ok }
	readingsOf := func(string) []Reading { return all }
	ruled := func(string) (string, bool) { return "/h.mp4", true }

	hits := []Hit{{Path: "/h.verified.md"}, {Path: "/h.mp4"}}
	got := CollapseToAuthoritative(hits, readingOf, readingsOf, ruled)
	if len(got) != 1 || got[0].Path != "/h.mp4" {
		t.Fatalf("the ruling was ignored: %+v", got)
	}
}

// A ruling naming a reading that is no longer indexed must not blank the answer.
func TestAuthoritative_AStaleRulingFallsBackRatherThanFailing(t *testing.T) {
	all := []Reading{rs(ReadingMachine, "/h.mp4", 1), rs(ReadingAttested, "/h.verified.md", 2)}
	best, ok := AuthoritativeReading(all, func(string) (string, bool) { return "/gone.md", true })
	if !ok {
		t.Fatal("a stale ruling produced no reading at all")
	}
	if best.DocPath != "/h.verified.md" {
		t.Fatalf("fell back to %s, want the attested reading", best.DocPath)
	}
}

// Same level → the newer read wins, because a re-read supersedes.
func TestAuthoritative_ARereadSupersedes(t *testing.T) {
	all := []Reading{rs(ReadingMachine, "/a", 10), rs(ReadingMachine, "/b", 20)}
	best, _ := AuthoritativeReading(all, nil)
	if best.DocPath != "/b" {
		t.Fatalf("older read won: %s", best.DocPath)
	}
}

// Documents with no reading recorded are never dropped — that is most of a
// corpus, and a hit must not vanish for want of a row nothing wrote.
func TestCollapse_UnregisteredDocumentsSurvive(t *testing.T) {
	readingOf := func(string) (Reading, bool) { return Reading{}, false }
	readingsOf := func(string) []Reading { return nil }
	hits := []Hit{{Path: "/a.pdf"}, {Path: "/b.pdf"}}
	got := CollapseToAuthoritative(hits, readingOf, readingsOf, func(string) (string, bool) { return "", false })
	if len(got) != 2 {
		t.Fatalf("dropped a hit that had no reading row: %d of 2 survived", len(got))
	}
}
