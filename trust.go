package raglit

import (
	"encoding/json"
	"fmt"
	"sort"
)

// How far a reading can be trusted, and about WHAT.
//
// A reading is not one claim. A hearing transcript asserts both the words spoken
// and who spoke them, and those are produced by different machinery with
// different failure rates — the recogniser is good and the diarizer is worse.
// Collapsing them into one number, or one "verified" flag, throws away the
// distinction a reader needs most. oidio's own verified transcripts say so on
// their face:
//
//	✅ Speaker attribution is complete — 29 ruled on individually, 45 affirmed…
//	⚠ The WORDS are unverified. Attribution was checked; the text was not.
//
// Marking such a file `attested` and stopping would assert the opposite of what
// it says. So trust is per FACET: attribution is ruled on, wording is not, and
// both facts travel with the reading.
//
// The same shape covers the other kinds. A page lifted from a text layer is the
// document's own bytes and its text is certain; a page a model READ is not. An
// image description is the model's account of what it saw, which is a claim
// about SUBJECT rather than about text, and it is the least certain thing here.

// Facets — the parts of a reading that can be trusted separately.
const (
	// FacetText is the words: what the document says, or what was spoken.
	FacetText = "text"
	// FacetSpeaker is who said them. Only diarized readings have it, and it is
	// the facet that fails independently of the words.
	FacetSpeaker = "speaker"
	// FacetLayout is reading order and structure — which column follows which,
	// where a table's cells belong. A page can be read perfectly and assembled
	// wrongly.
	FacetLayout = "layout"
	// FacetSubject is what a model says an image DEPICTS. Not a transcription of
	// anything: nobody wrote "a red Chevrolet Malibu", the model inferred it.
	FacetSubject = "subject"
)

// Confidence is how far a facet of a reading can be relied on, 0-100.
//
// 100 means "this is the thing itself, or a person has ruled on it". Everything
// below is a machine's output, and the number is a POLICY default for that
// machinery — not a measurement of this particular reading, which nothing can
// produce. Where a measurement exists it is cited on the constant.
type Confidence int

// Default confidences, by what produced the facet.
const (
	// TrustExact — the bytes are the text. A plain file, a text layer lifted
	// from a PDF, a mail body decoded from its transfer encoding: no model is
	// involved and there is nothing to be wrong about beyond the file itself.
	TrustExact Confidence = 100
	// TrustRuled — a person went through it. The ceiling, and the only way a
	// machine-produced facet reaches it.
	TrustRuled Confidence = 100
	// TrustPandoc — a converter's reading of a structured document. Not exact:
	// a .docx has a layout that prose does not, and pandoc chooses.
	TrustPandoc Confidence = 95
	// TrustASRWords — a recogniser's words. High: modern ASR on clear audio is
	// mostly right, and it is wrong in ways a reader can see (a mangled name, a
	// dropped clause) rather than in ways that read as fluent.
	TrustASRWords Confidence = 85
	// TrustVisionText — a VLM's transcription of a page. Measured on this setup,
	// not guessed: chandra scored 89.6% on olmOCR-bench long_tiny_text against a
	// published 92.3, and 14/17 on the delano ground-truth checks.
	TrustVisionText Confidence = 90
	// TrustSubject — a model's account of what an image shows. The lowest, and
	// the one that most needs saying out loud, because it reads as fact: a plate
	// number, a make and model, a colour, none of it written by anybody.
	TrustSubject Confidence = 80
	// TrustDiarization — who spoke. Lower than the words on purpose. The
	// diarizer's own output says so, labelling turns "blended, attribution
	// uncertain", and a verified transcript exists precisely because this is the
	// facet people have to correct.
	TrustDiarization Confidence = 65
	// TrustLayout — reading order over a page a model assembled.
	TrustLayout Confidence = 85
)

// DefaultTrust is what a reading produced by this method can be trusted on,
// before anybody has ruled.
//
// A facet that is ABSENT is not untrusted, it is not claimed: a page
// transcription asserts nothing about who spoke, and reporting 0 for it would
// read as "we know it is wrong".
func DefaultTrust(method string) map[string]Confidence {
	switch method {
	case MethodVerbatim, MethodTextLayer, MethodEmail:
		return map[string]Confidence{FacetText: TrustExact}
	case MethodPandoc:
		return map[string]Confidence{FacetText: TrustPandoc}
	case MethodASR:
		return map[string]Confidence{FacetText: TrustASRWords, FacetSpeaker: TrustDiarization}
	case MethodVision, MethodRegion:
		return map[string]Confidence{FacetText: TrustVisionText, FacetLayout: TrustLayout}
	case MethodCompiled:
		return map[string]Confidence{FacetText: TrustExact}
	}
	return nil
}

// Trust returns this reading's confidence per facet: the method's defaults, with
// anything a person has ruled on layered over them.
func (r Reading) Trust() map[string]Confidence {
	out := map[string]Confidence{}
	for f, c := range DefaultTrust(r.Method) {
		out[f] = c
	}
	// What a reading DESCRIBED is a claim about subject, not about text — whatever
	// produced it. Three cases, and the middle one is the one a flag could not
	// express:
	//
	//   wholly described  — a photograph. It says nothing about text, and
	//                       reporting a text score would invite quoting a model's
	//                       account of a picture as though somebody wrote it.
	//   mixed             — a SCREENSHOT. It transcribes the messages AND narrates
	//                       the phone around them, so it makes both claims and
	//                       both must be reported. The delano SMS exhibit measures
	//                       15% described; recorded as pure transcription it
	//                       overstated its text and hid its subject entirely.
	//   wholly transcribed— an ordinary page. No subject claim at all.
	switch {
	case r.Describes || r.DescribedPct >= int(describedPageThreshold*100):
		delete(out, FacetText)
		out[FacetSubject] = TrustSubject
	case r.DescribedPct > int(describedTraceFloor*100):
		out[FacetSubject] = TrustSubject
	}
	for f, c := range r.decodeRuled() {
		out[f] = c
	}
	return out
}

// RuledFacets are the facets a person has been through, and what they are worth
// afterwards. Stored on the reading as JSON.
func (r Reading) decodeRuled() map[string]Confidence {
	if r.Ruled == "" {
		return nil
	}
	var m map[string]Confidence
	if json.Unmarshal([]byte(r.Ruled), &m) != nil {
		return nil
	}
	return m
}

// WithRuling returns the reading with a facet marked as ruled on by a person.
//
// Per facet, because that is how ruling actually happens: somebody sits down and
// checks the speaker attribution of a hearing, or corrects the wording of a
// page, and almost never both at once. A transcript whose attribution was ruled
// on and whose words were not is the common case, not an edge one.
func (r Reading) WithRuling(facet string, c Confidence) Reading {
	m := r.decodeRuled()
	if m == nil {
		m = map[string]Confidence{}
	}
	m[facet] = c
	b, err := json.Marshal(m)
	if err != nil {
		return r
	}
	r.Ruled = string(b)
	return r
}

// LowestTrust is the weakest thing this reading claims, which is what a reader
// deciding whether to quote it needs. 0 when it claims nothing.
func (r Reading) LowestTrust() Confidence {
	t := r.Trust()
	if len(t) == 0 {
		return 0
	}
	low := Confidence(101)
	for _, c := range t {
		if c < low {
			low = c
		}
	}
	return low
}

// TrustSummary renders the facets in a stable order, weakest first, for a reader
// and for a log line.
func (r Reading) TrustSummary() []string {
	t := r.Trust()
	facets := make([]string, 0, len(t))
	for f := range t {
		facets = append(facets, f)
	}
	sort.Slice(facets, func(i, j int) bool {
		if t[facets[i]] != t[facets[j]] {
			return t[facets[i]] < t[facets[j]]
		}
		return facets[i] < facets[j]
	})
	out := make([]string, 0, len(facets))
	for _, f := range facets {
		out = append(out, fmt.Sprintf("%s %d%%", f, t[f]))
	}
	return out
}
