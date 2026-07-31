package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEml materialises a message and reads it back as pages.
func writeEml(t *testing.T, name, body string) []PageText {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := EmailText(p)
	if err != nil {
		t.Fatalf("EmailText(%s): %v", name, err)
	}
	return pages
}

// mail.ReadMessage does no transfer decoding at all, so the commonest shape
// there is — a plain quoted-printable message with no multipart wrapper —
// indexed as "Price =2450,000=20today." Every quotation taken from a body like
// that is corrupt, and nothing downstream can tell.
func TestQuotedPrintableBodyIsDecoded(t *testing.T) {
	const eml = "From: larry@example.com\r\n" +
		"Subject: Price\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"The price is =2450,000 and closing is=\r\n" +
		" the 1st.=20Confirmed=3F\r\n"
	txt := writeEml(t, "qp.eml", eml)[0].Text
	if !strings.Contains(txt, "The price is $50,000 and closing is the 1st. Confirmed?") {
		t.Errorf("quoted-printable body not decoded:\n%s", txt)
	}
	for _, leak := range []string{"=24", "=20", "=3F"} {
		if strings.Contains(txt, leak) {
			t.Errorf("raw quoted-printable %q survived into the transcription:\n%s", leak, txt)
		}
	}
}

// multipart.Reader decodes quoted-printable and NOTHING ELSE, so a base64
// text/plain or text/html part landed in the index as base64 — a wall of
// unsearchable noise where the body should be.
func TestBase64TextPartIsDecoded(t *testing.T) {
	// "The deed was recorded on 4 March." / "<p>HTML version.</p>"
	const eml = "From: a@example.com\r\nSubject: B64\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"VGhlIGRlZWQgd2FzIHJlY29yZGVkIG9uIDQgTWFyY2gu\r\n" +
		"--B\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"PHA+SFRNTCB2ZXJzaW9uLjwvcD4=\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "b64.eml", eml)[0].Text
	if !strings.Contains(txt, "The deed was recorded on 4 March.") {
		t.Errorf("base64 text/plain not decoded:\n%s", txt)
	}
	if !strings.Contains(txt, "HTML version.") {
		t.Errorf("base64 text/html not decoded:\n%s", txt)
	}
	if strings.Contains(txt, "VGhlIGRlZWQ") {
		t.Errorf("raw base64 survived into the transcription:\n%s", txt)
	}
}

// Real mail wraps base64 with spaces and tabs as well as newlines. Go's decoder
// skips \r and \n only, so an indented continuation line would otherwise fail
// the decode and drop the body.
func TestBase64ToleratesIndentedWrapping(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: B64\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		" VGhlIGRlZWQg\t\r\n\t d2FzIHJlY29yZGVkLg==\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "b64w.eml", eml)[0].Text
	if !strings.Contains(txt, "The deed was recorded.") {
		t.Errorf("wrapped base64 not decoded:\n%s", txt)
	}
}

// multipart.Part decodes quoted-printable AND removes the header when it does.
// Reading the header back and decoding again would turn a legitimate "=3D" —
// an escaped equals sign, common in quoted URLs — into a decode error that
// truncates the body at that point.
func TestQuotedPrintableIsNotDecodedTwice(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: QP twice\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"See http://x.test/a?b=3D1=26c=3D2 for the plat.\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "qp2.eml", eml)[0].Text
	if !strings.Contains(txt, "http://x.test/a?b=1&c=2") {
		t.Errorf("quoted-printable part decoded wrongly (double-decode?):\n%s", txt)
	}
	if !strings.Contains(txt, "for the plat.") {
		t.Errorf("body truncated at the escape:\n%s", txt)
	}
}

// Mail from a Windows client is routinely windows-1252, in the body and in the
// Subject. Read as UTF-8 the bytes are invalid and a quotation carrying a
// curly apostrophe or a £ cannot be matched against the source.
func TestNonUTF8CharsetIsDecoded(t *testing.T) {
	// windows-1252: 0x92 = right single quote, 0xA3 = pound sign.
	body := "The broker\x92s fee was \xA3500.\r\n"
	eml := "From: a@example.com\r\n" +
		"Subject: =?windows-1252?Q?The_broker=92s_fee?=\r\n" +
		"Content-Type: text/plain; charset=windows-1252\r\n\r\n" + body
	txt := writeEml(t, "cp1252.eml", eml)[0].Text
	if !strings.Contains(txt, "The broker\u2019s fee was \u00a3500.") {
		t.Errorf("windows-1252 body not decoded to UTF-8:\n%q", txt)
	}
	if !strings.Contains(txt, "Subject: The broker\u2019s fee") {
		t.Errorf("windows-1252 encoded-word in the Subject not decoded:\n%q", txt)
	}
}

// An unknown charset label must leave the bytes alone. Raw bytes a human can
// still recover; a confident transliteration through the wrong table is a
// silent, permanent corruption.
func TestUnknownCharsetLeavesBytesAlone(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: X\r\n" +
		"Content-Type: text/plain; charset=x-nonesuch-9000\r\n\r\n" +
		"Plain ascii survives.\r\n"
	txt := writeEml(t, "unk.eml", eml)[0].Text
	if !strings.Contains(txt, "Plain ascii survives.") {
		t.Errorf("unknown charset lost the body:\n%s", txt)
	}
}

// A *mail.Message reading from a multipart.Part is only valid until NextPart
// advances. The reader used to collect enclosed messages and walk them after
// the loop, which appeared to work purely because mail.ReadMessage's bufio had
// already pulled the first 4096 bytes — so an enclosed message longer than that
// was cut off mid-sentence with no error anywhere. In an archive of nested
// forwards that is most of the content.
func TestLargeEnclosedMessageIsNotTruncated(t *testing.T) {
	const marker = "TAIL-MARKER-THE-DEED-WAS-DELIVERED"
	filler := strings.Repeat("Enclosed correspondence, line of no consequence.\r\n", 400)
	eml := "From: larry@example.com\r\nSubject: Forward\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: marta@example.com\r\nSubject: Re\r\nContent-Type: text/plain\r\n\r\n" +
		filler + marker + "\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nCovering note.\r\n" +
		"--B--\r\n"
	pages := writeEml(t, "big.eml", eml)
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if !strings.Contains(pages[1].Text, marker) {
		t.Errorf("enclosed message truncated: %d bytes read of %d sent, tail missing",
			len(pages[1].Text), len(filler)+len(marker))
	}
	if !strings.Contains(pages[0].Text, "Covering note.") {
		t.Errorf("buffering the enclosed message lost the part after it:\n%s", pages[0].Text)
	}
}

// multipart/alternative is the SAME message twice. Concatenating both indexes
// every sentence a second time and doubles every BM25 term count — and the
// comment claiming plain text won was describing code that did not.
func TestAlternativePrefersPlainTextAndDoesNotDuplicate(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: Alt\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nThe lot line runs north.\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<html><body><p>The lot line runs north.</p></body></html>\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "alt.eml", eml)[0].Text
	if n := strings.Count(txt, "The lot line runs north."); n != 1 {
		t.Errorf("body appears %d times, want 1 (plain text wins over the html twin):\n%s", n, txt)
	}
}

// With no plain alternative the html one is still the message; dropping it
// would leave the page empty.
func TestAlternativeFallsBackToHTML(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: Alt\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<p>Only an html body.</p>\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "althtml.eml", eml)[0].Text
	if !strings.Contains(txt, "Only an html body.") {
		t.Errorf("html-only alternative lost its body:\n%s", txt)
	}
}

// multipart/mixed is sequential content, not alternatives — every part is a
// different piece of the message and all of them belong in the page.
func TestMixedKeepsEveryPart(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: Mixed\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nFirst paragraph.\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nSecond paragraph.\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "mixed.eml", eml)[0].Text
	for _, want := range []string{"First paragraph.", "Second paragraph."} {
		if !strings.Contains(txt, want) {
			t.Errorf("multipart/mixed dropped %q:\n%s", want, txt)
		}
	}
}

// An inline application/pdf with no Content-Disposition and no filename used to
// match no case in the switch and vanish without trace — the page said nothing
// had been sent.
func TestUnnamedNonTextPartIsStillNamed(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: X\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nSee below.\r\n" +
		"--B\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJDRA==\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "unnamed.eml", eml)[0].Text
	if !strings.Contains(txt, "(unnamed)") || !strings.Contains(txt, "application/pdf") {
		t.Errorf("unnamed attachment left no trace:\n%s", txt)
	}
	// 4 bytes decoded from QUJDRA==, not the 8 base64 characters.
	if !strings.Contains(txt, "4 bytes") {
		t.Errorf("attachment size is of the encoded form, not the file:\n%s", txt)
	}
}

// A Content-Disposition: attachment with no filename is still a document
// somebody sent.
func TestAttachmentWithNoFilenameIsNamed(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: X\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nBody.\r\n" +
		"--B\r\nContent-Type: text/plain\r\nContent-Disposition: attachment\r\n\r\nnot the body\r\n" +
		"--B--\r\n"
	txt := writeEml(t, "nofn.eml", eml)[0].Text
	if !strings.Contains(txt, "Attachments: (unnamed)") {
		t.Errorf("dispositioned attachment not named:\n%s", txt)
	}
	if strings.Contains(txt, "not the body") {
		t.Errorf("an attachment was read as the message body:\n%s", txt)
	}
}

// A multipart declaring no boundary has no parsable structure. Handing it to
// multipart.NewReader returns an immediate error and used to lose the entire
// body; reading it flat at least keeps the words, and the note says why the
// page looks odd.
func TestMultipartWithNoBoundaryKeepsItsBody(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: X\r\n" +
		"Content-Type: multipart/mixed\r\n\r\n" +
		"The words that would otherwise be lost.\r\n"
	txt := writeEml(t, "nb.eml", eml)[0].Text
	if !strings.Contains(txt, "The words that would otherwise be lost.") {
		t.Errorf("body lost with no boundary:\n%s", txt)
	}
	if !strings.Contains(txt, "no boundary") {
		t.Errorf("nothing said the structure was unreadable:\n%s", txt)
	}
}

// A truncated archive — the final boundary never arrives — must yield what was
// read AND say that it stopped early. Silently returning a short page is the
// failure mode nothing downstream can detect.
func TestTruncatedArchiveSaysSoRatherThanPretending(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: Cut\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nThe part that arrived.\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nand then the file ends mid-"
	txt := writeEml(t, "cut.eml", eml)[0].Text
	if !strings.Contains(txt, "The part that arrived.") {
		t.Errorf("truncation lost the parts that DID arrive:\n%s", txt)
	}
	if !strings.Contains(txt, "unreadable MIME structure") {
		t.Errorf("truncation was not reported:\n%s", txt)
	}
}

// No Content-Type at all is a legal RFC 822 message and means text/plain.
func TestMessageWithNoContentType(t *testing.T) {
	const eml = "From: a@example.com\r\nSubject: Bare\r\n\r\nJust a body.\r\n"
	txt := writeEml(t, "bare.eml", eml)[0].Text
	if !strings.Contains(txt, "Just a body.") {
		t.Errorf("bare message lost its body:\n%s", txt)
	}
}

// Deeply nested forwards must terminate. Past maxDepth the reader stops
// enclosing rather than recursing; it must not panic, hang, or drop the
// messages it already read.
func TestDeeplyNestedForwardsTerminate(t *testing.T) {
	// Build maxDepth*3 levels of message/rfc822 enclosure, innermost first.
	const levels = maxDepth * 3
	body := "From: deep@example.com\r\nSubject: Innermost\r\nContent-Type: text/plain\r\n\r\nThe innermost note.\r\n"
	for i := levels; i > 0; i-- {
		b := "B" + strings.Repeat("x", i)
		body = "From: l" + string(rune('a'+i%26)) + "@example.com\r\n" +
			"Subject: Level\r\n" +
			"Content-Type: multipart/mixed; boundary=" + b + "\r\n\r\n" +
			"--" + b + "\r\nContent-Type: message/rfc822\r\n\r\n" + body +
			"--" + b + "--\r\n"
	}
	pages := writeEml(t, "deep.eml", body)
	if len(pages) == 0 {
		t.Fatal("deep nesting produced no pages")
	}
	if len(pages) > levels+2 {
		t.Errorf("got %d pages from %d levels — the depth guard did not hold", len(pages), levels)
	}
}

// A file that is not a message at all is an error, not an empty success.
func TestNonMessageFileIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.eml")
	if err := os.WriteFile(p, []byte("\x00\x01\x02 not mail at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EmailText(p); err == nil {
		t.Error("a non-message file read as an email without error")
	}
}
