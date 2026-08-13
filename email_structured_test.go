package raglit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The structured reader and the indexed page text must agree about which
// message is on which page — a search hit cites a page, and a reader following
// it has to land on the message the hit came from.
//
// They are two renderings of one parse, and nothing but this test stops them
// drifting: EmailText numbers pages by putting a manifest first, and
// EmailMessages reproduces that arithmetic.
func TestEmailMessages_PageNumbersMatchTheIndexedText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thread.eml")
	const raw = "From: Larry <larry@example.com>\r\n" +
		"To: Carl <carl@example.com>\r\n" +
		"Subject: Outer\r\n" +
		"Date: Tue, 14 Jul 2026 12:43:24 -0700\r\n" +
		"Reply-To: someone-else@example.com\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nthe outer body\r\n" +
		"--b1\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: Sally <sally@example.com>\r\nSubject: Inner\r\n\r\nthe enclosed body\r\n" +
		"--b1--\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	pages, err := EmailText(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := EmailMessages(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want the outer message and the enclosed one, got %d", len(msgs))
	}
	byPage := map[int]string{}
	for _, p := range pages {
		byPage[p.Page] = p.Text
	}
	for _, m := range msgs {
		text, ok := byPage[m.Page]
		if !ok {
			t.Fatalf("message %q claims page %d, which the indexed text does not have", m.Subject, m.Page)
		}
		if !strings.Contains(text, m.Subject) {
			t.Fatalf("page %d does not carry subject %q — the two renderings disagree about ordering",
				m.Page, m.Subject)
		}
	}

	// Enclosure is the thread structure, and it must survive.
	if msgs[0].Depth != 0 || msgs[1].Depth != 1 {
		t.Fatalf("depths %d/%d — the enclosed message is not marked as enclosed", msgs[0].Depth, msgs[1].Depth)
	}

	// Every header, not the four the transcription keeps. Reply-To is exactly
	// the kind a reader asks about and "N further headers" cannot be opened.
	var found bool
	for _, h := range msgs[0].Headers {
		if strings.EqualFold(h.Name, "Reply-To") && strings.Contains(h.Value, "someone-else") {
			found = true
		}
	}
	if !found {
		t.Fatal("Reply-To is absent — the structured reader kept only the rendered headers")
	}
}

// An archive whose attachments were never extracted must say the attachment
// exists and that there is no file, which is a different statement from having
// no attachment at all.
func TestEmailMessages_UnextractedAttachmentHasNoPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "one.eml")
	const raw = "From: a@example.com\r\nSubject: With a file\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--b1\r\nContent-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"survey.pdf\"\r\n\r\n%PDF-1.7 fake\r\n" +
		"--b1--\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, err := EmailMessages(path, ResolveExtractedAttachments(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 {
		t.Fatalf("want one message carrying one attachment, got %d/%v", len(msgs), msgs)
	}
	a := msgs[0].Attachments[0]
	if a.Name != "survey.pdf" {
		t.Fatalf("declared name lost: %q", a.Name)
	}
	if a.Path != "" {
		t.Fatalf("nothing was extracted, but a path was claimed: %q", a.Path)
	}
	if a.Sum == "" {
		t.Fatal("no hash recorded — it is the only handle on an unextracted attachment")
	}
}

// And once extraction HAS run, each attachment resolves to the file on disk —
// by content, because the extractor dedups within an archive.
func TestResolveExtractedAttachments_MatchesByContentNotName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "two.eml")
	const body = "%PDF-1.7 the same bytes on two messages"
	raw := "From: a@example.com\r\nSubject: One\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--b1\r\nContent-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"survey.pdf\"\r\n\r\n" + body + "\r\n" +
		"--b1--\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	n, _, err := ExtractEmailAttachments(path, path)
	if err != nil || n != 1 {
		t.Fatalf("extract: n=%d err=%v", n, err)
	}
	msgs, err := EmailMessages(path, ResolveExtractedAttachments(path))
	if err != nil {
		t.Fatal(err)
	}
	a := msgs[0].Attachments[0]
	if a.Path == "" {
		t.Fatal("an extracted attachment resolved to no file")
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatalf("resolved to a path that is not there: %v", err)
	}
}
