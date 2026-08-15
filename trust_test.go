package raglit

import "testing"

// A hearing transcript makes TWO claims and they are not equally good. The
// recogniser's words are strong; who said them is weaker, and it is the facet
// people actually correct.
func TestTrust_AudioSplitsWordsFromSpeaker(t *testing.T) {
	r := Reading{Method: MethodASR}
	tr := r.Trust()
	if tr[FacetText] <= tr[FacetSpeaker] {
		t.Fatalf("words %d, speaker %d — diarization must not be trusted as much as the words",
			tr[FacetText], tr[FacetSpeaker])
	}
	if r.LowestTrust() != tr[FacetSpeaker] {
		t.Fatalf("the weakest claim is not the one reported: %d", r.LowestTrust())
	}
}

// Ruling on attribution must NOT promote the wording.
//
// The verified transcripts say it themselves: "Speaker attribution is complete
// … ⚠ The WORDS are unverified." A single verified flag would assert the
// opposite of what the file it came from says.
func TestTrust_RulingOnSpeakerLeavesTheWordsAlone(t *testing.T) {
	r := Reading{Method: MethodASR}.WithRuling(FacetSpeaker, TrustRuled)
	tr := r.Trust()
	if tr[FacetSpeaker] != TrustRuled {
		t.Fatalf("attribution was ruled on and reads %d", tr[FacetSpeaker])
	}
	if tr[FacetText] != TrustASRWords {
		t.Fatalf("ruling on attribution promoted the words to %d — that is a claim nobody made", tr[FacetText])
	}
	if r.LowestTrust() != TrustASRWords {
		t.Fatalf("lowest trust %d, want the unverified words", r.LowestTrust())
	}
}

// Raw text is the bytes themselves; a model's account of a photograph is the
// least certain thing in the corpus, and it is a claim about SUBJECT — asking
// what its "text" is worth is asking the wrong question.
func TestTrust_ExactBeatsReadBeatsDescribed(t *testing.T) {
	exact := Reading{Method: MethodVerbatim}.Trust()
	read := Reading{Method: MethodVision}.Trust()
	desc := Reading{Method: MethodVision, Describes: true}.Trust()

	if exact[FacetText] != TrustExact {
		t.Fatalf("a text layer is the document's own bytes: %d", exact[FacetText])
	}
	if read[FacetText] >= exact[FacetText] {
		t.Fatalf("a page a model READ is not as certain as one lifted: %d", read[FacetText])
	}
	if _, ok := desc[FacetText]; ok {
		t.Fatal("a description claims nothing about text — reporting a text score invites quoting it")
	}
	if desc[FacetSubject] != TrustSubject {
		t.Fatalf("subject %d", desc[FacetSubject])
	}
	if desc[FacetSubject] >= read[FacetText] {
		t.Fatal("describing an image must not rate as high as transcribing a page")
	}
}

// A facet that is absent is NOT untrusted — it is unclaimed. Reporting 0 for
// "who spoke" on a scanned page would read as "we know it is wrong".
func TestTrust_UnclaimedFacetsAreAbsentNotZero(t *testing.T) {
	tr := Reading{Method: MethodVision}.Trust()
	if _, ok := tr[FacetSpeaker]; ok {
		t.Fatal("a page transcription claimed something about who spoke")
	}
}

// A SCREENSHOT is both, and that is the case a flag could not express.
//
// chandra reading a page of SMS messages transcribes the messages and narrates
// the phone around them — status bar, app icons, microphone and camera buttons.
// Measured at 15% of the delano SMS exhibit and 28% of its worst page, such a
// page failed the binary test and was recorded as pure transcription: the
// reading claimed `text 90%` and said nothing at all about subject, overstating
// one and hiding the other.
func TestTrust_AMixedPageClaimsBoth(t *testing.T) {
	mixed := Reading{Method: MethodVision, DescribedPct: 40}.Trust()
	if _, ok := mixed[FacetText]; !ok {
		t.Fatal("a mixed page transcribes real text — dropping the text claim hides the record")
	}
	if mixed[FacetSubject] != TrustSubject {
		t.Fatal("a mixed page also describes — a reader must be told the prose is a model's")
	}
	mr := Reading{Method: MethodVision, DescribedPct: 40}
	if mr.LowestTrust() != TrustSubject {
		t.Fatal("the weakest claim of a mixed page is the description")
	}
}

// A figure caption inside a page of transcribed text is not worth reporting as a
// subject claim — below the trace floor the page is a transcription.
func TestTrust_ATraceOfDescriptionIsNotASubjectClaim(t *testing.T) {
	tr := Reading{Method: MethodVision, DescribedPct: 2}.Trust()
	if _, ok := tr[FacetSubject]; ok {
		t.Fatal("a stray caption made the whole page a subject claim")
	}
	if tr[FacetText] != TrustVisionText {
		t.Fatalf("text %d — a transcribed page with one caption is still transcription", tr[FacetText])
	}
}

// Wholly described still claims nothing about text.
func TestTrust_WhollyDescribedMakesNoTextClaim(t *testing.T) {
	for _, r := range []Reading{
		{Method: MethodVision, Describes: true},
		{Method: MethodVision, DescribedPct: 95},
	} {
		tr := r.Trust()
		if _, ok := tr[FacetText]; ok {
			t.Fatalf("a photograph claimed something about text: %+v", tr)
		}
		if tr[FacetSubject] != TrustSubject {
			t.Fatalf("subject %d", tr[FacetSubject])
		}
	}
}
