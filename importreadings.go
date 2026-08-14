package raglit

import (
	"fmt"
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

// ImportVerifiedTranscripts finds documents that are verified transcripts of
// recordings already indexed, and records them as ATTESTED readings of the same
// source — so search collapses to one, and the one it keeps is the transcript a
// person ruled on.
//
// dryRun reports the matches and records nothing.
func (s *Store) ImportVerifiedTranscripts(dryRun bool, ruledBy string) ([]TranscriptMatch, error) {
	docs, err := s.Documents()
	if err != nil {
		return nil, err
	}

	// The candidates: readings of a recording. Only a document that already has
	// a reading row can be a target, because the source digest is the thing
	// being joined on and nothing else knows it.
	type candidate struct {
		reading Reading
		words   map[string]bool
	}
	var cands []candidate
	var transcripts []string
	for _, d := range docs {
		r, ok, _ := s.ReadingFor(d.Path)
		switch {
		case ok && r.Method == MethodASR && r.SourceSHA256 != "":
			cands = append(cands, candidate{reading: r, words: wordSet(r.Text)})
		case !ok && isVerifiedTranscript(d.Path):
			transcripts = append(transcripts, d.Path)
		}
	}
	sort.Strings(transcripts)

	var out []TranscriptMatch
	for _, tp := range transcripts {
		txt, err := s.DocText(tp, 0, 0, 0)
		if err != nil {
			continue
		}
		tw := wordSet(txt.Text)
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
				Method: MethodASR, Level: ReadingAttested, RuledBy: ruledBy, Text: txt.Text,
			}); err != nil {
				m.Why = err.Error()
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
