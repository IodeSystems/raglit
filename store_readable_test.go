package raglit

import (
	"strings"
	"testing"
)

// A PAGE WE COULD NOT READ IS NOT A PAGE THAT SAYS NOTHING.
//
// A failed image extraction hands back raw JPEG, and storing that in `fragments`
// makes it searchable text — it will never match a real quotation, so a citation
// to that page reports ABSENT ("the document does not say this") when the truth
// is that nobody could read it. Found in a live corpus: a 2008 survey supporting
// six facts had page 1 stored as raw bytes, and a query over the index crashed
// decoding it.
func TestBytesAreNotStoredAsATranscription(t *testing.T) {
	// Raw JPEG header and body, the exact shape that got through.
	jpeg := "\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00" + strings.Repeat("\x8a\x00\x1f\xd9", 40)
	if isReadableText(jpeg) {
		t.Error("raw image bytes were accepted as a transcription")
	}
	// Invalid UTF-8 alone is enough — a query over the index cannot even decode it.
	if isReadableText("valid text then \xff\xfe\xfd broken") {
		t.Error("text that is not valid UTF-8 was accepted")
	}
	if isReadableText("") {
		t.Error("empty accepted")
	}
}

// AND IT MUST NOT REJECT REAL DOCUMENTS. A survey is mostly numbers, a plat
// mostly punctuation, an exhibit may be in another language — none of that is a
// failed extraction, and a guard that discarded them would lose evidence to
// prevent a smaller harm.
func TestRealTranscriptionsAreKept(t *testing.T) {
	for name, s := range map[string]string{
		"a survey":      "N 89°14'22\" E 132.50' TO THE TRUE POINT OF BEGINNING; AF 201503110045",
		"a plat":        "LOT 6 | LOT 7 | LOT 8 —— 25' STRIP ——  (SEE SHEET 2 OF 3)",
		"non-ascii":     "Ich erkläre hiermit, daß die Grenze südlich verläuft — § 12 Abs. 3",
		"tabbed table":  "Date\tEntry\n2022-06-15\tOrder entered\r\n2022-06-27\tReturn hearing",
		"a figure note": "[FIGURE: A black ink drawing of a stylized curved line]",
	} {
		if !isReadableText(s) {
			t.Errorf("%s was rejected as unreadable", name)
		}
	}
}
