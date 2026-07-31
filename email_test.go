package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An archive is not one document. The broker's log in ardley-v-brannock is a
// single 24 MB .eml holding 24 nested messages; read as one page, a fact
// quoting the fifth of them can say only that the words appear somewhere in 24
// megabytes.
func TestEachNestedMessageIsItsOwnPage(t *testing.T) {
	const eml = "From: Larry <larry@example.com>\r\n" +
		"To: Marta <marta@example.com>\r\n" +
		"Subject: Lots\r\n" +
		"Content-Type: multipart/mixed; boundary=BOUND\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"We only listed the house lot for sale.\r\n" +
		"--BOUND\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"\r\n" +
		"From: Marta <marta@example.com>\r\n" +
		"To: Larry <larry@example.com>\r\n" +
		"Subject: Re: Lots\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Understood!\r\n" +
		"--BOUND--\r\n"
	path := filepath.Join(t.TempDir(), "log.eml")
	if err := os.WriteFile(path, []byte(eml), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := EmailText(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("got %d page(s), want a manifest plus one per message", len(pages))
	}
	if !strings.Contains(pages[1].Text, "We only listed the house lot") {
		t.Errorf("page 1 lost the outer body:\n%s", pages[1].Text)
	}
	if !strings.Contains(pages[2].Text, "Understood!") {
		t.Errorf("page 2 lost the enclosed message:\n%s", pages[2].Text)
	}
	if !strings.Contains(pages[2].Text, "enclosed message") {
		t.Errorf("page 2 does not say it is enclosed:\n%s", pages[2].Text)
	}
}

// For an email the routing IS evidence: who was copied and when is very often
// the whole question, and a transcription keeping only the body has discarded
// the part a dispute turns on.
func TestHeadersSurviveIntoTheTranscription(t *testing.T) {
	const eml = "From: Larry <larry@example.com>\r\n" +
		"To: Marta <marta@example.com>\r\n" +
		"Cc: Bert <bert@example.com>\r\n" +
		"Date: Sat, 15 May 2021 21:07:00 -0700\r\n" +
		"Subject: Lots\r\n" +
		"X-Mailer: something\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Body text.\r\n"
	path := filepath.Join(t.TempDir(), "m.eml")
	if err := os.WriteFile(path, []byte(eml), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := EmailText(path)
	if err != nil {
		t.Fatal(err)
	}
	txt := pages[1].Text
	for _, want := range []string{"From: Larry", "To: Marta", "Cc: Bert", "Subject: Lots", "Date:"} {
		if !strings.Contains(txt, want) {
			t.Errorf("transcription dropped %q:\n%s", want, txt)
		}
	}
	// Dropped headers are COUNTED rather than silently omitted — "there was
	// nothing else" and "we kept the four that matter" are different claims.
	if !strings.Contains(txt, "further headers") {
		t.Errorf("no note of the headers not shown:\n%s", txt)
	}
}

// A base64 blob buries the text around it, and the filename is what a reader
// needs in order to know the attachment exists and go and find it.
func TestAttachmentsAreNamedNotInlined(t *testing.T) {
	const eml = "From: a@example.com\r\n" +
		"Subject: With a scan\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"See attached.\r\n" +
		"--B\r\n" +
		"Content-Type: application/pdf; name=\"legal.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"legal.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQKJSXXXXXXXXXXXXXXXXXXXXXXXX\r\n" +
		"--B--\r\n"
	path := filepath.Join(t.TempDir(), "a.eml")
	if err := os.WriteFile(path, []byte(eml), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := EmailText(path)
	if err != nil {
		t.Fatal(err)
	}
	txt := pages[1].Text
	if !strings.Contains(txt, "Attachments: legal.pdf") {
		t.Errorf("attachment not named:\n%s", txt)
	}
	if strings.Contains(txt, "JVBERi0xLjQK") {
		t.Errorf("base64 payload was inlined into the transcription:\n%s", txt)
	}
}

func TestEmlIsClassifiedAsEmail(t *testing.T) {
	if got := ClassifyDoc("log.eml", ""); got != KindEmail {
		t.Errorf("ClassifyDoc(.eml) = %v, want KindEmail", got)
	}
	if got := ClassifyDoc("x", "message/rfc822"); got != KindEmail {
		t.Errorf("ClassifyDoc(message/rfc822) = %v, want KindEmail", got)
	}
	// .txt must NOT be swept in — the extracts are still plain text.
	if got := ClassifyDoc("a.txt", ""); got != KindText {
		t.Errorf("ClassifyDoc(.txt) = %v, want KindText", got)
	}
}

// Page 1 of a container states what the container HOLDS.
//
// An archive is not a composition. A scanned bundle is several instruments on
// one run of paper, so carving it by page range yields the instrument — that is
// what slicing is for. An archive holds separate documents whole, and its own
// text only REFERS to them, so reading it end to end tells you what was said and
// never what was enclosed. The manifest is the answer to "what is in here",
// available without reading all of it.
func TestPageOneIsTheManifestOfWhatTheArchiveCarries(t *testing.T) {
	eml := "From: a@x\r\nSubject: With a scan\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"legal.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"legal.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--B--\r\n"
	path := filepath.Join(t.TempDir(), "m.eml")
	if err := os.WriteFile(path, []byte(eml), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := EmailText(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 2 {
		t.Fatalf("want a manifest plus messages, got %d page(s)", len(pages))
	}
	m := pages[0]
	if m.Page != 1 {
		t.Errorf("the manifest must be page 1, got %d", m.Page)
	}
	if !strings.Contains(m.Text, "ARCHIVE MANIFEST") {
		t.Errorf("page 1 is not a manifest:\n%s", m.Text)
	}
	// It has to be usable as an index: a page number a citation can name.
	if !strings.Contains(m.Text, "Page 2:") {
		t.Errorf("the manifest must name the page each message is on:\n%s", m.Text)
	}
	// And it has to name what travelled inside, which no message body does.
	if !strings.Contains(m.Text, "encloses:") {
		t.Errorf("the manifest must name enclosed documents:\n%s", m.Text)
	}
	// Messages start at 2, contiguously — numbering has to stay complete or a
	// consumer resolving a match to a page reports the wrong one.
	for i, p := range pages[1:] {
		if p.Page != i+2 {
			t.Fatalf("page %d out of order: got %d", i+2, p.Page)
		}
	}
}
