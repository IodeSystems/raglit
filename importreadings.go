package raglit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Adopting transcripts that were produced outside raglit.
//
// A corpus can already hold verified transcripts — oidio writes one beside each
// recording it diarizes, and a person rules on the speaker attribution there.
// raglit indexed those as ORDINARY DOCUMENTS, so a hearing appeared twice: once
// as the recording, whose text is the machine transcript, and once as the
// verified file, with nothing to say they were the same 44 minutes or which one
// could be quoted.
//
// They should be two READINGS of one recording. Turning them into that is not
// automatic, because the chain that would say so is broken at the far end:
//
//	verified.md   → "Source: …diarized.json"
//	truth.json    → "source: …diarized.json"
//	diarized.json → duration, language, segments, speakers, text   ← names no media
//
// So the transcript cannot name its recording, and the filenames do not help —
// `Larry Renewal Order - H22-00139_2023-07-17_10-00-31 AM-2.mp4` against
// `H22-139-2023-07-17-renewal-hearing.verified.md`.
//
// What IS reliable is the text. Two transcripts of one recording share almost
// every word, and two transcripts of different hearings share almost none: the
// same words in the same room, against different rooms entirely. So this matches
// on CONTENT — containment of the smaller word set in the larger — and
// refuses anything short of overwhelming.

// importMatchFloor is how much of the verified transcript's vocabulary must
// appear in the machine one before they are called the same recording.
//
// High on purpose. A wrong match attaches a verified transcript to the wrong
// hearing and then RANKS IT ABOVE the right one, which is worse than leaving
// both as separate documents — the state this is trying to improve on is merely
// untidy, and the failure mode is a quotation attributed to the wrong day in
// court. Unmatched transcripts are reported, not guessed at.
const importMatchFloor = 0.75

// TranscriptMatch is one verified transcript and the recording it belongs to.
type TranscriptMatch struct {
	Transcript string  `json:"transcript"`
	Recording  string  `json:"recording,omitempty"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score"`
	// Why is set when nothing was adopted, and says which of the two failure
	// shapes it was: nothing close enough, or nothing to compare against.
	Why string `json:"why,omitempty"`
}

// ImportVerifiedTranscripts finds verified transcripts ON DISK and records them
// as ATTESTED readings of the recordings they transcribe — so search collapses
// to one, and the one it keeps is the transcript a person ruled on.
//
// From DISK, not from the index, and that ordering is the point. A transcript
// beside a recording should not be a document at all: it is a reading, and once
// the corpus stops indexing these sidecars there is nothing in the index to walk.
// An importer that could only see what was already indexed would stop working
// the moment its job was done.
//
// Candidates come from the READINGS, not from the documents: a recording with a
// machine reading is the only thing a transcript can be adopted onto, and the
// digest to join on lives there. Transcripts are looked for in the directories
// those recordings came from, which is where every producer puts them.
//
// dryRun reports the matches and records nothing.
func (s *Store) ImportVerifiedTranscripts(dryRun bool, ruledBy string) ([]TranscriptMatch, error) {
	all, err := s.q.ListAllReadings(context.Background())
	if err != nil {
		return nil, err
	}
	type candidate struct {
		reading Reading
		words   map[string]bool
	}
	var cands []candidate
	adopted := map[string]bool{}
	dirs := map[string]bool{}
	for _, row := range all {
		r := readingFrom(row)
		adopted[r.DocPath] = true
		if r.Method != MethodASR || r.SourceSHA256 == "" || r.Level != ReadingMachine {
			continue
		}
		cands = append(cands, candidate{reading: r, words: wordSet(r.Text)})
		if d := filepath.Dir(strings.TrimPrefix(r.SourcePath, "file://")); d != "." {
			dirs[d] = true
		}
	}

	var files []string
	for d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(d, e.Name())
			if isVerifiedTranscript(p) && !adopted[p] {
				files = append(files, p)
			}
		}
	}
	sort.Strings(files)

	var out []TranscriptMatch
	for _, tp := range files {
		b, err := os.ReadFile(tp)
		if err != nil {
			out = append(out, TranscriptMatch{Transcript: tp, Why: err.Error()})
			continue
		}
		tw := wordSet(string(b))
		m := TranscriptMatch{Transcript: tp}
		if len(cands) == 0 {
			m.Why = "no recording in this index carries a machine reading to compare against"
			out = append(out, m)
			continue
		}
		for _, c := range cands {
			if sc := containment(tw, c.words); sc > m.Score {
				m.Score, m.Recording, m.Source = sc, c.reading.DocPath, c.reading.SourceSHA256
			}
		}
		if m.Score < importMatchFloor {
			m.Why = fmt.Sprintf("best match %.0f%% is below the floor — left alone rather than guessed at", m.Score*100)
			m.Recording, m.Source = "", ""
			out = append(out, m)
			continue
		}
		if !dryRun {
			if err := s.RecordReading(Reading{
				SourceSHA256: m.Source, SourcePath: m.Recording, DocPath: tp,
				Method: MethodASR, Level: ReadingAttested, RuledBy: ruledBy, Text: string(b),
			}); err != nil {
				m.Why = err.Error()
				out = append(out, m)
				continue
			}
			// The SOURCE's document now holds the governing reading's words.
			//
			// This is what adopting means. A transcript that is not indexed cannot
			// answer a search, so recording it and stopping would take the hearing
			// out of the corpus entirely — the reading would govern and nothing
			// would be there to return. One document per source, carrying the best
			// account of it, is the whole shape: the recording stays the thing you
			// cite, and its text is now the one a person ruled on rather than the
			// machine's speaker-1.
			if err := s.reindexFromReading(m.Recording, string(b)); err != nil {
				m.Why = "adopted, but the recording could not be re-indexed: " + err.Error()
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// isVerifiedTranscript reports whether a path looks like a transcript somebody
// has been through. Name-based, because that is the only signal a file produced
// outside raglit carries — and it is only ever used to pick CANDIDATES, never to
// decide a match.
func isVerifiedTranscript(path string) bool {
	l := strings.ToLower(path)
	return strings.HasSuffix(l, ".verified.md")
}

// wordSet is the distinct lowercase words of a text, for comparing what two
// transcripts say rather than how they are formatted. Speaker labels, timestamps
// and markdown differ between the two renderings of one hearing; the words
// spoken do not.
func wordSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(f) > 2 {
			out[f] = true
		}
	}
	return out
}

// containment is how much of a is present in b — NOT Jaccard.
//
// The two transcripts are not the same size: one carries a verification header,
// real speaker names and corrected wording, the other carries speaker-1 and a
// machine's guesses. Jaccard punishes that difference; containment asks the
// question actually being asked, which is whether this transcript's words appear
// in that recording's.
func containment(a, b map[string]bool) float64 {
	if len(a) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}


// reindexFromReading replaces a document's text with a reading's, keeping the
// document's identity — its path, and everything keyed to it.
func (s *Store) reindexFromReading(docPath, text string) error {
	title := filepath.Base(strings.TrimPrefix(docPath, "file://"))
	w, st, fl := ResolveFragParams(0, 0, 0, 0)
	var frags []Fragment
	for _, of := range OverlapFragments(text, w, st, fl) {
		frags = append(frags, Fragment{Text: of.Text})
	}
	if len(frags) == 0 {
		frags = []Fragment{{Text: text}}
	}
	return s.Ingest(context.Background(), Document{Path: docPath, Title: title, Fragments: frags})
}
