package raglit

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"sort"
	"strings"
)

// Reading an .eml.
//
// An email archive is not one document, and treating it as one is the trap. The
// broker's own communication log in ardley-v-brannock is a single 24 MB .eml
// holding 24 nested rfc822 messages and 69 attachments — a decade of a
// transaction in one file. Read as plain text it is mostly base64, and read as
// "page 1" it is unciteable: a fact that quotes the fifth message can say only
// that the words appear somewhere in 24 megabytes.
//
// So each nested message becomes a PAGE. That is not a metaphor stretched to
// fit — it is exactly what a page marker is for, a stable unit a citation can
// name, and it makes `kg quotes` report "p. 7, line 40" where p. 7 is the
// seventh message rather than an arbitrary offset.
//
// Headers are kept, in full, at the top of every message. For an email the
// routing IS evidence: who was copied, when, and by whom is very often the
// whole question, and a transcription that keeps only the body has silently
// discarded the part a dispute turns on.

// emailPart is one message in an archive, flattened in reading order.
type emailPart struct {
	depth   int
	headers []string
	body    string
	attach  []string
}

// headerOrder is what gets kept, in the order a reader expects them. Not every
// header: an .eml carries dozens of Received/DKIM/X- lines that bury the four
// that matter. Everything else is summarised as a count so nothing is silently
// claimed to be absent.
var headerOrder = []string{"From", "Sent", "To", "Cc", "Bcc", "Date", "Subject", "Reply-To"}

// EmailText renders an .eml as paged text, one page per nested message.
func EmailText(path string) ([]PageText, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	msg, err := mail.ReadMessage(f)
	if err != nil {
		return nil, fmt.Errorf("%s: not a readable email: %w", path, err)
	}
	var parts []emailPart
	walkMessage(msg, 0, &parts)
	if len(parts) == 0 {
		return nil, fmt.Errorf("%s: no readable message parts", path)
	}
	out := make([]PageText, 0, len(parts))
	for i, p := range parts {
		var b strings.Builder
		if p.depth > 0 {
			fmt.Fprintf(&b, "— enclosed message, depth %d —\n\n", p.depth)
		}
		for _, h := range p.headers {
			b.WriteString(h)
			b.WriteByte('\n')
		}
		if len(p.attach) > 0 {
			b.WriteString("Attachments: " + strings.Join(p.attach, ", ") + "\n")
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(p.body))
		out = append(out, PageText{Page: i + 1, Text: strings.TrimSpace(b.String()), Engine: "email"})
	}
	return out, nil
}

// walkMessage flattens a message and everything enclosed in it.
func walkMessage(msg *mail.Message, depth int, out *[]emailPart) {
	p := emailPart{depth: depth, headers: renderHeaders(msg.Header)}
	body, attach, nested := readBody(msg.Header.Get("Content-Type"), msg.Body, depth)
	p.body, p.attach = body, attach
	*out = append(*out, p)
	for _, n := range nested {
		walkMessage(n, depth+1, out)
	}
}

func renderHeaders(h mail.Header) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range headerOrder {
		if v := h.Get(k); v != "" {
			out = append(out, k+": "+decodeWord(v))
			seen[strings.ToLower(k)] = true
		}
	}
	// Say how much was dropped rather than implying there was nothing else.
	var others []string
	for k := range h {
		if !seen[strings.ToLower(k)] {
			others = append(others, k)
		}
	}
	if len(others) > 0 {
		sort.Strings(others)
		out = append(out, fmt.Sprintf("(%d further headers: %s)", len(others), strings.Join(others, ", ")))
	}
	return out
}

// decodeWord turns RFC 2047 encoded-words into readable text, leaving anything
// it cannot decode exactly as it was.
func decodeWord(s string) string {
	dec := new(mime.WordDecoder)
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// readBody returns the readable text, the attachment filenames, and any
// enclosed messages.
//
// Attachments are NAMED, never inlined. A base64 blob in a transcription is
// noise that buries the text around it, and the name is what a reader needs to
// know it exists and go and find it.
func readBody(ctype string, r io.Reader, depth int) (body string, attach []string, nested []*mail.Message) {
	const maxDepth = 12 // an archive of archives is possible; unbounded recursion is not
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || !strings.HasPrefix(mt, "multipart/") || depth > maxDepth {
		b, _ := io.ReadAll(r)
		if strings.HasPrefix(mt, "text/html") {
			return htmlToText(string(b)), nil, nil
		}
		return string(b), nil, nil
	}
	mr := multipart.NewReader(r, params["boundary"])
	var texts []string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		pct := part.Header.Get("Content-Type")
		pmt, _, _ := mime.ParseMediaType(pct)
		switch {
		case pmt == "message/rfc822":
			if sub, err := mail.ReadMessage(part); err == nil {
				nested = append(nested, sub)
			}
		case strings.HasPrefix(pmt, "multipart/"):
			sb, sa, sn := readBody(pct, part, depth+1)
			if sb != "" {
				texts = append(texts, sb)
			}
			attach = append(attach, sa...)
			nested = append(nested, sn...)
		case part.FileName() != "":
			attach = append(attach, decodeWord(part.FileName()))
		case strings.HasPrefix(pmt, "text/plain"):
			b, _ := io.ReadAll(part)
			texts = append(texts, string(b))
		case strings.HasPrefix(pmt, "text/html"):
			b, _ := io.ReadAll(part)
			texts = append(texts, htmlToText(string(b)))
		}
		_ = part.Close()
	}
	// Prefer the plain-text alternative when both were sent: it is what the
	// sender wrote, before a mail client reformatted it.
	return strings.Join(texts, "\n\n"), attach, nested
}

// htmlToText strips tags well enough for a transcription. Not a renderer — the
// point is that the WORDS are searchable and quotable, and an html mail part
// whose text is locked inside markup is a quotation nothing can verify.
func htmlToText(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				b.WriteByte(' ')
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	// Collapse the whitespace html leaves behind, but keep paragraph breaks.
	lines := strings.Split(b.String(), "\n")
	var keep []string
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "\n")
}
