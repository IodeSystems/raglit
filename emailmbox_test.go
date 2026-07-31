package raglit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMbox(t *testing.T, name, body string) []PageText {
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

// ClassifyDoc routed .mbox to KindEmail while EmailText called mail.ReadMessage,
// which reads exactly ONE message. A ten-year mailbox indexed its first message
// and reported success — the worst shape a bug can take, because the archive
// looks read and every question asked of it comes back "not in the corpus".
func TestMboxYieldsEveryMessageNotJustTheFirst(t *testing.T) {
	const mb = "From larry@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: Larry <larry@example.com>\r\nSubject: Lot lines\r\n\r\n" +
		"We only listed the house lot.\r\n" +
		"\r\n" +
		"From marta@example.com Sun May 16 09:00:00 2021\r\n" +
		"From: Marta <marta@example.com>\r\nSubject: Re: Lot lines\r\n\r\n" +
		"The plat says otherwise.\r\n" +
		"\r\n" +
		"From bert@example.com Mon May 17 11:30:00 2021\r\n" +
		"From: Bert <bert@example.com>\r\nSubject: Survey\r\n\r\n" +
		"Ordering a survey.\r\n"
	pages := writeMbox(t, "log.mbox", mb)
	if len(pages) != 3 {
		t.Fatalf("got %d page(s), want one per message — the split dropped %d", len(pages), 3-len(pages))
	}
	for i, want := range []string{"We only listed the house lot.", "The plat says otherwise.", "Ordering a survey."} {
		if !strings.Contains(pages[i].Text, want) {
			t.Errorf("page %d lost its body (%q):\n%s", i+1, want, pages[i].Text)
		}
	}
	// The separator is not a header. Reading it as one used to produce
	// "(1 further headers: From larry@example.com Sat May 15 21)".
	if strings.Contains(pages[0].Text, "Sat May 15 21)") {
		t.Errorf("the From_ separator was parsed as a header:\n%s", pages[0].Text)
	}
	if !strings.Contains(pages[1].Text, "Subject: Re: Lot lines") {
		t.Errorf("page 2 lost its headers:\n%s", pages[1].Text)
	}
}

// A body sentence starting "From " must not split the message in half. Real
// writers stuff it to ">From "; the separator must also follow a blank line.
func TestMboxDoesNotSplitOnAFromInsideABody(t *testing.T) {
	const mb = "From larry@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: Larry <larry@example.com>\r\nSubject: One\r\n\r\n" +
		"The first line.\r\n" +
		">From the outset we agreed on the price.\r\n" +
		"A line, and then:\r\n" +
		"From here on the survey governs.\r\n"
	pages := writeMbox(t, "stuffed.mbox", mb)
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1 — a body line split the message", len(pages))
	}
	txt := pages[0].Text
	// Unstuffed on the way past: the reader should see what the sender wrote.
	if !strings.Contains(txt, "From the outset we agreed on the price.") {
		t.Errorf("stuffed line not unstuffed:\n%s", txt)
	}
	if strings.Contains(txt, ">From the outset") {
		t.Errorf("the mbox quoting character survived into the transcription:\n%s", txt)
	}
	if !strings.Contains(txt, "From here on the survey governs.") {
		t.Errorf("an unstuffed mid-body From line was lost:\n%s", txt)
	}
}

// Everything the .eml path does — nested messages as their own pages, transfer
// decoding, attachments named — has to work inside an mbox too, and page
// numbering has to run continuously across the whole mailbox so a citation
// naming "p. 4" means one thing.
func TestMboxMessagesGetTheFullEmlTreatment(t *testing.T) {
	const mb = "From a@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: Larry <larry@example.com>\r\nSubject: QP\r\n" +
		"Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"The price is =2450,000.\r\n" +
		"\r\n" +
		"From b@example.com Sun May 16 09:00:00 2021\r\n" +
		"From: Marta <marta@example.com>\r\nSubject: Forward\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nSee the enclosure.\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"plat.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"plat.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nQUJDRA==\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: Bert <bert@example.com>\r\nSubject: Enclosed\r\nContent-Type: text/plain\r\n\r\n" +
		"The enclosed note.\r\n" +
		"--B--\r\n"
	pages := writeMbox(t, "rich.mbox", mb)
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3 (two messages + one enclosure)", len(pages))
	}
	if !strings.Contains(pages[0].Text, "The price is $50,000.") {
		t.Errorf("transfer decoding does not reach mbox messages:\n%s", pages[0].Text)
	}
	if !strings.Contains(pages[1].Text, "Attachments: plat.pdf") {
		t.Errorf("attachment not named inside an mbox:\n%s", pages[1].Text)
	}
	if !strings.Contains(pages[2].Text, "The enclosed note.") {
		t.Errorf("enclosed message not given its own page inside an mbox:\n%s", pages[2].Text)
	}
	for i, p := range pages {
		if p.Page != i+1 {
			t.Errorf("page numbering is not continuous across the mailbox: page %d at index %d", p.Page, i)
		}
	}
}

// The extension is not the authority. Ingest materialises fetched bytes to a
// temp file named raglit-*.eml whatever the source was, so a .mbox arriving
// through the worker would be read as one message if the split trusted the
// name.
func TestMboxIsRecognisedByContentNotExtension(t *testing.T) {
	const mb = "From a@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: A <a@example.com>\r\nSubject: One\r\n\r\nFirst.\r\n\r\n" +
		"From b@example.com Sun May 16 09:00:00 2021\r\n" +
		"From: B <b@example.com>\r\nSubject: Two\r\n\r\nSecond.\r\n"
	if n := len(writeMbox(t, "raglit-12345.eml", mb)); n != 2 {
		t.Errorf("an mbox named .eml yielded %d page(s), want 2", n)
	}
	// And the converse: an .eml is not sniffed as an mbox just because it has a
	// From header.
	const eml = "From: A <a@example.com>\r\nSubject: One\r\n\r\nOnly one message.\r\n"
	if n := len(writeMbox(t, "one.mbox", eml)); n != 1 {
		t.Errorf("a single message named .mbox yielded %d page(s), want 1", n)
	}
}

// One corrupt message in a mailbox must not cost the other 9,999, and it must
// not shift the page numbers under them — a citation to "p. 40" cannot become a
// citation to a different message because message 12 was malformed.
func TestOneUnreadableMboxMessageDoesNotLoseTheRest(t *testing.T) {
	const mb = "From a@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: A <a@example.com>\r\nSubject: One\r\n\r\nFirst body.\r\n\r\n" +
		"From b@example.com Sun May 16 09:00:00 2021\r\n" +
		"\x00\x01 not a header block at all\r\n\r\n" +
		"From c@example.com Mon May 17 09:00:00 2021\r\n" +
		"From: C <c@example.com>\r\nSubject: Three\r\n\r\nThird body.\r\n"
	pages := writeMbox(t, "bad.mbox", mb)
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3 — a bad message must still occupy its slot", len(pages))
	}
	if !strings.Contains(pages[0].Text, "First body.") || !strings.Contains(pages[2].Text, "Third body.") {
		t.Errorf("a malformed message cost its neighbours:\n1: %s\n3: %s", pages[0].Text, pages[2].Text)
	}
	if !strings.Contains(pages[1].Text, "unreadable message") {
		t.Errorf("the malformed message was silently blank rather than reported:\n%s", pages[1].Text)
	}
}

// An empty file, and a file that is only a separator, must not panic.
func TestDegenerateMboxes(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"separator only", "From a@example.com Sat May 15 21:07:00 2021\r\n"},
		{"no body", "From a@example.com Sat May 15 21:07:00 2021\r\nFrom: a@example.com\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "x.mbox")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			// Either pages or an error; never a panic and never a hang.
			_, _ = EmailText(p)
		})
	}
}

// A message with no line terminator at the end of file is still a message.
func TestMboxLastMessageWithoutTrailingNewline(t *testing.T) {
	const mb = "From a@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: A <a@example.com>\r\nSubject: One\r\n\r\nFirst.\r\n\r\n" +
		"From b@example.com Sun May 16 09:00:00 2021\r\n" +
		"From: B <b@example.com>\r\nSubject: Two\r\n\r\nThe unterminated tail."
	pages := writeMbox(t, "tail.mbox", mb)
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if !strings.Contains(pages[1].Text, "The unterminated tail.") {
		t.Errorf("the final message was lost for want of a newline:\n%s", pages[1].Text)
	}
}

// A base64 attachment written as one very long line: bufio.Scanner would fail
// the whole archive at 64 KB, which is why the split reads lines itself.
func TestMboxToleratesAVeryLongLine(t *testing.T) {
	long := strings.Repeat("QUJDRA", 30000) // ~180 KB on one line
	mb := "From a@example.com Sat May 15 21:07:00 2021\r\n" +
		"From: A <a@example.com>\r\nSubject: Big\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nCovering note.\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"scan.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"scan.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + long + "\r\n" +
		"--B--\r\n\r\n" +
		"From b@example.com Sun May 16 09:00:00 2021\r\n" +
		"From: B <b@example.com>\r\nSubject: After\r\n\r\nStill here.\r\n"
	pages := writeMbox(t, "longline.mbox", mb)
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2 — the long line broke the split", len(pages))
	}
	if !strings.Contains(pages[0].Text, "Covering note.") {
		t.Errorf("long-line message lost its body:\n%s", pages[0].Text)
	}
	if !strings.Contains(pages[1].Text, "Still here.") {
		t.Errorf("the message after a long line was lost:\n%s", pages[1].Text)
	}
}

// A large mailbox must not be quadratic or hold the file in memory. This is a
// smoke bound, not a benchmark: it fails if the split ever starts re-scanning.
func TestLargeMboxCompletes(t *testing.T) {
	var b strings.Builder
	const n = 2000
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "From a@example.com Sat May 15 21:07:00 2021\r\n"+
			"From: A <a@example.com>\r\nSubject: Message %d\r\n\r\n"+
			"%s\r\nBody of message %d.\r\n\r\n",
			i, strings.Repeat("Filler line of correspondence.\r\n", 20), i)
	}
	pages := writeMbox(t, "big.mbox", b.String())
	if len(pages) != n {
		t.Fatalf("got %d pages, want %d", len(pages), n)
	}
	if !strings.Contains(pages[n-1].Text, fmt.Sprintf("Body of message %d.", n-1)) {
		t.Errorf("last message wrong:\n%s", pages[n-1].Text)
	}
}

func TestMboxIsClassifiedAsEmail(t *testing.T) {
	if got := ClassifyDoc("archive.mbox", ""); got != KindEmail {
		t.Errorf("ClassifyDoc(.mbox) = %v, want KindEmail", got)
	}
}
