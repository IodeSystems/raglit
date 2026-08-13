package raglit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

// Reading a mail archive (.eml, .mbox).
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
//
// The decisions behind the mbox split and the attachment sidecar, and the list
// of what this reader used to get silently wrong, are in plan/email.md.

// emailPart is one message in an archive, flattened in reading order.
type emailPart struct {
	depth   int
	headers []string
	body    string
	attach  []emailAttachment
	// from/date/subject are kept apart from the rendered headers because the
	// attachment manifest cites a message by them, and re-parsing a formatted
	// header line back into fields would be a second, divergent parser.
	from, date, subject string
	// to/cc are kept for the same reason, for a reader that wants to see who a
	// message went to without expanding every header.
	to, cc string
	// all is every header the message carried, unrendered. The page text
	// deliberately keeps only a few and COUNTS the rest (renderHeaders), which is
	// right for a transcription and wrong for a reader who needs to check one:
	// a Received chain or a Reply-To is exactly what somebody asks about, and
	// "43 further headers" cannot be opened. Kept apart so the transcription is
	// unchanged.
	all mail.Header
	// notes record structure this reader could not parse. They go into the page
	// text on purpose: a transcription that drops content silently is worse than
	// one that says it dropped it, because only the second is recoverable.
	notes []string
}

// emailAttachment is one attachment as the transcription sees it. Size and hash
// are recorded even when the bytes are not kept — "a 4 MB PDF called survey.pdf
// was sent on this date" is itself evidence, and it is the only trace left when
// extraction is off.
//
// Name is exactly what the message DECLARED, empty included. Substituting a
// placeholder here would reach the extractor, which builds a filename from it
// and would then have no idea it was free to invent an extension from the media
// type — leaving an unnamed PDF on disk with no suffix and invisible to
// discovery. The placeholder belongs at the point of display.
type emailAttachment struct {
	Name  string
	Mime  string
	Size  int64
	Sum   string
	inner []byte // decoded bytes; held only when an extractor asked for them
}

// headerOrder is what gets kept, in the order a reader expects them. Not every
// header: an .eml carries dozens of Received/DKIM/X- lines that bury the four
// that matter. Everything else is summarised as a count so nothing is silently
// claimed to be absent.
var headerOrder = []string{"From", "Sent", "To", "Cc", "Bcc", "Date", "Subject", "Reply-To"}

// Bounds. None of these is a tuning knob — each is the point past which a
// malformed or hostile archive would otherwise cost unbounded time or memory.
const (
	// maxDepth bounds enclosure: an archive of archives is possible, unbounded
	// recursion is not.
	maxDepth = 12
	// maxParts bounds ONE multipart container. A generator loop can emit
	// millions of empty parts; a real message has tens.
	maxParts = 4096
	// maxMessages bounds an mbox. Past this the split stops and says so rather
	// than building an unbounded []PageText.
	maxMessages = 100000
)

// EmailText renders a mail archive as paged text, one page per message.
//
// Pure: it reads and returns, and writes nothing. That is load-bearing — the
// `ocr` MCP tool calls this to read a file the user named, and a read tool that
// materialises 69 files as a side effect is a tool nobody can trust. Attachment
// extraction is ExtractEmailAttachments, called separately by ingest.
func EmailText(path string) ([]PageText, error) {
	parts, err := readArchive(path, false)
	if err != nil {
		return nil, err
	}
	// Page 1 is the MANIFEST: what this archive carries, and where.
	//
	// An archive is a CONTAINER, not a composition. A scanned bundle is several
	// instruments printed onto one run of paper, so carving it by page range
	// yields the instrument itself — that is what `raglit slice` is for. An
	// archive holds separate documents whole, in their own encodings, and its own
	// text is a transcript that REFERS to them. Reading it end to end tells you
	// what was said and never what was enclosed.
	//
	// So the container states its contents first, in one place a person or a
	// search can land on: every message, and every document each one carries.
	// Without it, "what is in this archive" is answerable only by reading all 25
	// messages, and "which message carried the preapproval letter" is answerable
	// only by reading them twice.
	out := make([]PageText, 0, len(parts)+1)
	out = append(out, PageText{Page: 1, Text: manifestPage(parts), Engine: "email"})
	for i, p := range parts {
		out = append(out, PageText{Page: i + 2, Text: renderPart(p), Engine: "email"})
	}
	return out, nil
}

// manifestPage lists what the archive holds, message by message.
//
// Message numbers here are PAGE numbers, so the manifest is usable as an index:
// "the preapproval letter is on page 4" is a citation, not a hint. That is why
// the manifest takes page 1 rather than being appended — a reader who lands on
// page 1 of a container should be told what the container is before reading a
// word of it.
func manifestPage(parts []emailPart) string {
	var b strings.Builder
	b.WriteString("ARCHIVE MANIFEST\n\n")
	fmt.Fprintf(&b, "This is a container: %d message(s), listed below with the documents each\n", len(parts))
	b.WriteString("carries. The messages follow on the pages named here. An enclosed document\n")
	b.WriteString("is a SEPARATE document that travelled inside this archive; it is not part of\n")
	b.WriteString("the text of the message that carried it.\n\n")

	total := 0
	for i, p := range parts {
		page := i + 2
		subject, from, date := "", "", ""
		for _, h := range p.headers {
			switch {
			case strings.HasPrefix(h, "Subject:"):
				subject = strings.TrimSpace(strings.TrimPrefix(h, "Subject:"))
			case strings.HasPrefix(h, "From:"):
				from = strings.TrimSpace(strings.TrimPrefix(h, "From:"))
			case strings.HasPrefix(h, "Date:"), strings.HasPrefix(h, "Sent:"):
				if date == "" {
					date = strings.TrimSpace(h[strings.Index(h, ":")+1:])
				}
			}
		}
		if subject == "" {
			subject = "(no subject)"
		}
		fmt.Fprintf(&b, "Page %d: %s\n", page, subject)
		if from != "" || date != "" {
			fmt.Fprintf(&b, "  from %s  %s\n", from, date)
		}
		for _, a := range p.attach {
			total++
			fmt.Fprintf(&b, "  encloses: %s\n", a.describe())
		}
	}
	fmt.Fprintf(&b, "\n%d message(s), %d enclosed document(s) in total.\n", len(parts), total)
	return b.String()
}

// renderPart is the page text for one message.
//
// It never mentions where an attachment was extracted to. If it did, turning
// extraction on would rewrite every page and force a re-ingest of the whole
// archive without a single word of what was READ having changed. The manifest
// holds that mapping instead.
func renderPart(p emailPart) string {
	var b strings.Builder
	if p.depth > 0 {
		fmt.Fprintf(&b, "— enclosed message, depth %d —\n\n", p.depth)
	}
	for _, h := range p.headers {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	if len(p.attach) > 0 {
		names := make([]string, 0, len(p.attach))
		for _, a := range p.attach {
			names = append(names, a.describe())
		}
		b.WriteString("Attachments: " + strings.Join(names, ", ") + "\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(p.body))
	for _, n := range p.notes {
		b.WriteString("\n\n" + n)
	}
	return strings.TrimSpace(b.String())
}

// describe names an attachment with what it declared itself to be and how big it
// was, so a reader can tell a signed survey from a signature image without
// opening either.
func (a emailAttachment) describe() string {
	mt := a.Mime
	if mt == "" {
		mt = "unknown type"
	}
	name := a.Name
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf("%s (%s, %d bytes)", name, mt, a.Size)
}

// readArchive parses .eml or .mbox into flattened messages. keepBytes decides
// whether attachment payloads are retained; false streams them past a counter
// and throws them away, which is what keeps a 24 MB archive of scans from
// costing 24 MB of heap just to list its filenames.
func readArchive(path string, keepBytes bool) ([]emailPart, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	var parts []emailPart
	if isMbox(br) {
		if err := eachMboxMessage(br, func(raw []byte) error {
			msg, err := mail.ReadMessage(bytes.NewReader(raw))
			if err != nil {
				// One unparseable message must not lose the other 9,999. Record it
				// as a page of its own so the numbering — which citations depend
				// on — does not shift under every message after it.
				parts = append(parts, emailPart{notes: []string{
					fmt.Sprintf("[unreadable message, %d bytes: %v]", len(raw), err)}})
				return nil
			}
			walkMessage(msg, 0, &parts, keepBytes)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else {
		msg, err := mail.ReadMessage(br)
		if err != nil {
			return nil, fmt.Errorf("%s: not a readable email: %w", path, err)
		}
		walkMessage(msg, 0, &parts, keepBytes)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%s: no readable message parts", path)
	}
	return parts, nil
}

// isMbox sniffs the RFC 4155 signature: a "From " line at column 0.
//
// Sniffed rather than taken from the extension because ingest materialises
// fetched bytes to a temp file whose name RAGLIT chose (raglit-*.eml) — by the
// time this runs the extension is not the corpus's. Peeks, so the reader is
// still positioned at byte 0 either way.
func isMbox(br *bufio.Reader) bool {
	head, _ := br.Peek(5)
	return bytes.Equal(head, []byte("From "))
}

// eachMboxMessage splits an mbox and hands each message's bytes to fn.
//
// An mbox has no length header and no escape that every writer applies, so a
// "From " line at column 0 is the only separator there is. Two guards stop a
// body sentence beginning "From the outset," from cutting a message in half:
// the separator must be preceded by a blank line (or start the file), and a
// body line stuffed to ">From " is unstuffed on the way past. Neither is
// airtight — nothing about mbox is — but together they are what every real
// parser does.
//
// Streams: one message is held at a time, not the file.
func eachMboxMessage(br *bufio.Reader, fn func(raw []byte) error) error {
	var cur bytes.Buffer
	prevBlank := true // start of file counts as a boundary
	n := 0
	flush := func() error {
		if cur.Len() == 0 {
			return nil
		}
		raw := append([]byte(nil), cur.Bytes()...)
		cur.Reset()
		n++
		return fn(raw)
	}
	for {
		line, err := readLine(br)
		if len(line) == 0 && err != nil {
			break
		}
		if prevBlank && bytes.HasPrefix(line, []byte("From ")) {
			if err := flush(); err != nil {
				return err
			}
			if n >= maxMessages {
				return fmt.Errorf("mbox holds more than %d messages; refusing to read further", maxMessages)
			}
			prevBlank = false
			if err != nil {
				break
			}
			continue // the separator itself is not part of the message
		}
		// From-unstuffing. mboxo quotes a body "From " as ">From "; mboxrd also
		// quotes ">From " as ">>From ". Stripping exactly one ">" is right for
		// both in the case that actually occurs.
		if body := bytes.TrimLeft(line, ">"); len(body) < len(line) && bytes.HasPrefix(body, []byte("From ")) {
			line = line[1:]
		}
		cur.Write(line)
		prevBlank = len(bytes.TrimRight(line, "\r\n")) == 0
		if err != nil {
			break
		}
	}
	return flush()
}

// readLine reads one line INCLUDING its terminator, without the 64 KB cap a
// bufio.Scanner imposes — a base64 attachment written as one long line is
// legal, and a Scanner would fail the whole archive on it.
func readLine(br *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		out = append(out, chunk...)
		if err != nil {
			return out, err
		}
		if !isPrefix {
			return append(out, '\n'), nil
		}
	}
}

// walkMessage flattens a message and everything enclosed in it, in reading
// order: the covering message, then each message it encloses.
func walkMessage(msg *mail.Message, depth int, out *[]emailPart, keepBytes bool) {
	if depth > maxDepth {
		return
	}
	i := len(*out)
	*out = append(*out, emailPart{
		depth:   depth,
		headers: renderHeaders(msg.Header),
		all:     msg.Header,
		from:    decodeWord(msg.Header.Get("From")),
		to:      decodeWord(msg.Header.Get("To")),
		cc:      decodeWord(msg.Header.Get("Cc")),
		date:    msg.Header.Get("Date"),
		subject: decodeWord(msg.Header.Get("Subject")),
	})
	r := decodeTransfer(msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	body, attach, nested, notes := readBody(msg.Header.Get("Content-Type"), r, depth, keepBytes)
	(*out)[i].body, (*out)[i].attach, (*out)[i].notes = body, attach, notes
	for _, raw := range nested {
		sub, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			(*out)[i].notes = append((*out)[i].notes,
				fmt.Sprintf("[unreadable enclosed message, %d bytes: %v]", len(raw), err))
			continue
		}
		walkMessage(sub, depth+1, out, keepBytes)
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
	// CharsetReader handles the encoded-words whose charset is not one of the two
	// Go decodes itself — "=?windows-1252?Q?...?=" in a Subject is ordinary in
	// mail from a Windows client, and without this it comes through as mojibake.
	dec.CharsetReader = func(cs string, r io.Reader) (io.Reader, error) {
		return charsetReader(cs, r), nil
	}
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// bodyText is one candidate body found in a container, tagged with the media
// type that produced it, so multipart/alternative can choose between them.
type bodyText struct {
	mt   string
	text string
}

// readBody returns the readable text, the attachments, the raw bytes of any
// enclosed messages, and notes about structure it could not parse.
//
// Attachments are NAMED, never inlined. A base64 blob in a transcription is
// noise that buries the text around it, and the name is what a reader needs to
// know it exists and go and find it. ExtractEmailAttachments is how the bytes
// become readable.
func readBody(ctype string, r io.Reader, depth int, keepBytes bool) (body string, attach []emailAttachment, nested [][]byte, notes []string) {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil && ctype != "" {
		notes = append(notes, fmt.Sprintf("[unparseable Content-Type %q: %v]", ctype, err))
	}
	// A multipart with no boundary parameter has no parsable structure. Reading
	// it raw keeps the words; handing it to multipart.NewReader (which is what
	// used to happen) returned an immediate error and lost the entire body.
	if !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" || depth > maxDepth {
		if strings.HasPrefix(mt, "multipart/") && params["boundary"] == "" {
			notes = append(notes, "[multipart declared with no boundary; read as flat text]")
		}
		b, _ := io.ReadAll(r)
		b = decodeCharset(b, params["charset"])
		if strings.HasPrefix(mt, "text/html") {
			return htmlToText(string(b)), nil, nil, notes
		}
		return string(b), nil, nil, notes
	}

	mr := multipart.NewReader(r, params["boundary"])
	var texts []bodyText
	for n := 0; ; n++ {
		if n >= maxParts {
			notes = append(notes, fmt.Sprintf("[stopped after %d MIME parts]", maxParts))
			break
		}
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("[unreadable MIME structure after %d part(s): %v]", n, err))
			break
		}
		pmt, pparams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		// multipart.Part transparently decodes quoted-printable AND removes the
		// header when it does, so reading the header back cannot double-decode.
		// It decodes nothing else — base64 text parts arrive as base64 unless
		// this call handles them, which is what used to put raw base64 in the
		// index.
		pr := decodeTransfer(part.Header.Get("Content-Transfer-Encoding"), part)
		switch {
		case pmt == "message/rfc822":
			// Buffered HERE, not walked later. A *mail.Message reading from a
			// multipart.Part is only valid until NextPart advances, and a
			// deferred walk therefore saw whatever mail.ReadMessage's bufio had
			// already pulled — 4 KB — and silently truncated everything past it.
			if b, err := io.ReadAll(pr); err == nil {
				nested = append(nested, b)
			}
		case strings.HasPrefix(pmt, "multipart/"):
			sb, sa, sn, snotes := readBody(part.Header.Get("Content-Type"), pr, depth+1, keepBytes)
			if sb != "" {
				texts = append(texts, bodyText{mt: pmt, text: sb})
			}
			attach = append(attach, sa...)
			nested = append(nested, sn...)
			notes = append(notes, snotes...)
		case isAttachment(part, pmt):
			attach = append(attach, readAttachment(part, pmt, pr, keepBytes))
		case strings.HasPrefix(pmt, "text/plain"), pmt == "":
			b, _ := io.ReadAll(pr)
			texts = append(texts, bodyText{mt: "text/plain", text: string(decodeCharset(b, pparams["charset"]))})
		case strings.HasPrefix(pmt, "text/html"):
			b, _ := io.ReadAll(pr)
			texts = append(texts, bodyText{mt: "text/html", text: htmlToText(string(decodeCharset(b, pparams["charset"])))})
		default:
			// Anything else is an attachment that forgot to say so — an inline
			// application/pdf with no Content-Disposition used to match no case
			// at all and vanish without trace.
			attach = append(attach, readAttachment(part, pmt, pr, keepBytes))
		}
		_ = part.Close()
	}
	return chooseBody(mt, texts), attach, nested, notes
}

// chooseBody picks the text a container contributes.
//
// multipart/alternative holds the SAME message twice; concatenating both (which
// is what used to happen, under a comment claiming otherwise) indexes every
// sentence a second time and doubles every BM25 term count. Plain text wins: it
// is what the sender typed, before a mail client reformatted it. Every other
// container is sequential content, so its parts are joined.
func chooseBody(mt string, texts []bodyText) string {
	if mt == "multipart/alternative" {
		for _, want := range []string{"text/plain", "text/html"} {
			for i := len(texts) - 1; i >= 0; i-- {
				if texts[i].mt == want && strings.TrimSpace(texts[i].text) != "" {
					return texts[i].text
				}
			}
		}
	}
	var out []string
	for _, t := range texts {
		if strings.TrimSpace(t.text) != "" {
			out = append(out, t.text)
		}
	}
	return strings.Join(out, "\n\n")
}

// isAttachment reports whether a part is a file rather than the message body.
// A filename is the usual signal; an explicit attachment disposition without one
// counts too, because a scanner that names nothing still sent a document.
func isAttachment(part *multipart.Part, pmt string) bool {
	if part.FileName() != "" {
		return true
	}
	disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	return disp == "attachment"
}

// readAttachment drains a part into an attachment record. With keepBytes false
// the payload is counted and hashed on the way to io.Discard, so listing the
// attachments of a 24 MB archive of scans costs a hash, not 24 MB of heap.
func readAttachment(part *multipart.Part, pmt string, r io.Reader, keepBytes bool) emailAttachment {
	a := emailAttachment{Name: decodeWord(part.FileName()), Mime: pmt}
	var buf bytes.Buffer
	var sink io.Writer = io.Discard
	if keepBytes {
		sink = &buf
	}
	// Hashed ALWAYS, streaming, which is what the comment above always claimed
	// and the code only did when it was already holding the bytes. The hash is
	// the only handle on an attachment nothing extracted — it is how a reader
	// asks "is this the same PDF that came on the other three messages", and how
	// an extracted file is matched back to the message that carried it. Costing
	// a hash rather than 24 MB of heap is the whole point of streaming here.
	h := sha256.New()
	n, _ := io.Copy(io.MultiWriter(sink, h), r)
	a.Size = n
	a.Sum = hex.EncodeToString(h.Sum(nil))
	if keepBytes {
		a.inner = buf.Bytes()
	}
	return a
}

// decodeTransfer undoes a Content-Transfer-Encoding.
//
// Nothing upstream does this for a message body: mail.ReadMessage does no
// decoding at all, and multipart.Part does quoted-printable and only that. A
// quoted-printable body read raw carries "=20" and soft line breaks into every
// quotation taken from it, and a base64 body read raw is not text.
func decodeTransfer(enc string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &spaceStripper{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r // 7bit / 8bit / binary / absent — already the bytes
}

// spaceStripper drops whitespace before the base64 decoder sees it. Go's decoder
// skips \r and \n but not spaces or tabs, and real mail wraps base64 with both.
type spaceStripper struct{ r io.Reader }

func (s *spaceStripper) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	w := 0
	for i := 0; i < n; i++ {
		switch p[i] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
		default:
			p[w] = p[i]
			w++
		}
	}
	if w == 0 && err == nil {
		return 0, nil
	}
	return w, err
}

// decodeCharset converts a body to UTF-8 from whatever Content-Type declared.
//
// An unrecognised label leaves the bytes alone on purpose: raw bytes are still
// recoverable by a human, a confident transliteration through the wrong table
// is not, and mail from a Windows client mislabels its charset often enough
// that guessing would be worse than not.
func decodeCharset(b []byte, charset string) []byte {
	dec := charsetDecoder(charset)
	if dec == nil {
		return b
	}
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(b), dec))
	if err != nil {
		return b
	}
	return out
}

func charsetReader(charset string, r io.Reader) io.Reader {
	if dec := charsetDecoder(charset); dec != nil {
		return transform.NewReader(r, dec)
	}
	return r
}

// charsetDecoder returns the transform for a charset label, or nil when the
// bytes are already UTF-8 or the label means nothing to us.
func charsetDecoder(charset string) transform.Transformer {
	switch cs := strings.ToLower(strings.TrimSpace(charset)); cs {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return nil
	default:
		enc, err := ianaindex.MIME.Encoding(cs)
		if err != nil || enc == nil {
			return nil
		}
		return enc.NewDecoder()
	}
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

// ── structured reading, for a reader rather than an index ──────────────────
//
// EmailText renders an archive as PAGES, which is what the index wants: one
// message per page, headers reduced to the few that matter and the rest counted.
// That is the right transcription and the wrong thing to read.
//
// A 25-message thread rendered that way is 26 undifferentiated blocks of text.
// The nesting is a line of dashes, the headers are prose, "43 further headers"
// cannot be opened, and an attachment is a filename with nothing behind it even
// though the file was extracted and indexed as its own document. Everything a
// reader needs was parsed and then flattened.
//
// So this returns the same parse, unflattened. It re-reads the file rather than
// parsing the stored page text: the page text is a RENDERING, and a second
// parser that turned it back into fields would be free to disagree with the one
// that wrote it.

// EmailMessage is one message in an archive, structured for display.
type EmailMessage struct {
	// Page is the page number this message occupies in the indexed document, so
	// a citation from search lands on the same message a reader is looking at.
	Page int `json:"page"`
	// Depth is enclosure: 0 is a top-level message in the archive, 1 is a message
	// forwarded inside one, and so on. This is the thread structure.
	Depth   int    `json:"depth"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Cc      string `json:"cc,omitempty"`
	Date    string `json:"date,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Headers is EVERY header, name and value, sorted. The page text keeps four
	// and counts the rest; this is the rest.
	Headers []EmailHeader `json:"headers,omitempty"`
	Body    string        `json:"body,omitempty"`
	// Attachments are what this message carried, whether or not extraction ran.
	// "A 4 MB PDF called survey.pdf was sent on this date" is itself evidence.
	Attachments []EmailAttachmentRef `json:"attachments,omitempty"`
	// Notes is structure the reader could not parse, carried rather than dropped.
	Notes []string `json:"notes,omitempty"`
}

// EmailHeader is one header line.
type EmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// EmailAttachmentRef is an attachment as a reader can act on it.
type EmailAttachmentRef struct {
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
	Sum  string `json:"sum,omitempty"`
	// Path is the extracted file beside the archive, when extraction ran and the
	// bytes are on disk. Empty means the attachment was seen and not extracted —
	// which is a different statement from "there is no attachment", and the UI
	// must not render them the same way.
	Path string `json:"path,omitempty"`
}

// EmailMessages reads an archive into structured messages. Pure: it reads the
// file and writes nothing, for the reason EmailText gives.
//
// resolve, when non-nil, maps an attachment's sha256 to the path it was
// extracted to. Passing nil returns attachments with no Path, which is honest
// for an archive whose attachments were never extracted.
func EmailMessages(path string, resolve func(sum string) string) ([]EmailMessage, error) {
	parts, err := readArchive(path, false)
	if err != nil {
		return nil, err
	}
	out := make([]EmailMessage, 0, len(parts))
	for i, p := range parts {
		m := EmailMessage{
			// +2: page 1 is the archive manifest, so the first message is page 2.
			// The same arithmetic manifestPage uses — kept in step by the test.
			Page: i + 2, Depth: p.depth,
			From: p.from, To: p.to, Cc: p.cc, Date: p.date, Subject: p.subject,
			Body: p.body, Notes: p.notes,
		}
		for name, vals := range p.all {
			for _, v := range vals {
				m.Headers = append(m.Headers, EmailHeader{Name: name, Value: decodeWord(v)})
			}
		}
		sort.Slice(m.Headers, func(a, b int) bool { return m.Headers[a].Name < m.Headers[b].Name })
		for _, a := range p.attach {
			ref := EmailAttachmentRef{Name: a.Name, Mime: a.Mime, Size: a.Size, Sum: a.Sum}
			if resolve != nil {
				ref.Path = resolve(a.Sum)
			}
			m.Attachments = append(m.Attachments, ref)
		}
		out = append(out, m)
	}
	return out, nil
}

// ResolveExtractedAttachments maps sha256 → extracted file path for one archive,
// by hashing what is in its sidecar directory.
//
// By CONTENT, not by name. The extractor dedups within an archive — the same PDF
// on five messages in a thread is one file cited five times — so the filename it
// chose encodes the FIRST message that carried it and reconstructing it per
// message would point four of those five at a file that does not exist.
//
// Hashing the directory on each call is affordable because these directories
// hold tens of files, and it cannot drift from whatever is actually on disk,
// which reconstructing a name from the extractor's format string could.
func ResolveExtractedAttachments(archivePath string) func(string) string {
	dir := AttachmentDir(archivePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return func(string) string { return "" }
	}
	bySum := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == manifestName {
			continue
		}
		full := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		bySum[HashHex(b)] = full
	}
	return func(sum string) string { return bySum[sum] }
}

// IsMailArchive reports whether a path is one this reader handles.
func IsMailArchive(path string) bool {
	return ClassifyDoc(path, "") == KindEmail
}
