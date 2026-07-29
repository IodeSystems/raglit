# Reading mail archives

An email archive is not one document. The broker's communication log in
ardley-v-brannock is a single 24 MB `.eml` holding 24 nested `message/rfc822`
messages and 69 attachments — a decade of a transaction in one file. Read as
plain text it is mostly base64; read as one page a quotation from the fifth
message can only be located "somewhere in 24 megabytes".

So each message is a PAGE. That is settled and `email.go` already did it. This
document records the decisions taken to finish the job.

## Decision — implement `.mbox`, do not withdraw it

`ClassifyDoc` routed `.mbox` to `KindEmail` while `EmailText` called
`mail.ReadMessage`, which reads exactly ONE message. A ten-year mailbox therefore
indexed its first message and reported success. Nothing downstream could tell.

Withdrawing the claim does not fix that. `.mbox` would fall to `KindUnknown`,
`expandIngestTargets` would stop discovering it, and an explicit
`raglit ingest x.mbox` would read the whole mailbox as one undifferentiated text
blob — still unciteable, still base64, just failing in a different place. The
split itself is the only thing that makes an mbox readable at all, and it is
short. Implemented.

**How a message boundary is recognised.** There is no length header and no
univermarta-applied escape, so the `From ` line at column 0 is the only signal
there is (RFC 4155). Two guards keep a quoted `From the outset,` in a body from
splitting a message in half:

- the separator must be preceded by a blank line, or start the file;
- `>From ` at the start of a body line is unstuffed to `From ` (mboxrd/mboxo both
  quote it; one `>` is stripped, which is right for both in the common case).

**Sniffed, not trusted from the extension.** Ingest materialises fetched bytes to
a temp file whose name raglit chose (`raglit-*.eml`), so by the time `EmailText`
runs the extension is raglit's, not the corpus's. The first five bytes are the
authority.

## Decision — extract attachments beside the archive, opt in

69 attachments, several of them substantive instruments — a survey, a plat, an
amendment. Named-only leaves them unreachable: you can see that a survey was sent
and you cannot read it.

Rejected alternatives:

- **Transcribe inline.** `EmailText` has no context, no OCR handle, and no
  business acquiring one — it is also the `ocr` MCP tool's read path, which must
  not run a VLM as a side effect of reading a file. And an attachment's text
  folded into its covering message's page destroys the citation: "p. 7" would mean
  both the email and the 30-page survey inside it.
- **Leave them named.** Status quo. The evidence stays unreadable.

**Chosen: extract byte-for-byte to `<archive>.raglit-attachments/`, indexed on
the next `sync`/watch pass.** No queue plumbing — the directory is an ordinary
directory of ordinary files, and discovery already handles those. The extracted
files are deliberately NOT added to `builtinIgnore`: unlike a transcription they
are not raglit's derived output, they are originals that happened to travel
inside an envelope.

**The "second original" objection, and the answer to it.** An extracted file
dropped somewhere anonymous is a document with no chain — worse than no copy.
Three things make the chain hold:

1. The bytes are the decoded MIME part, copied, never converted. The extracted
   file's sha256 IS the sha256 of what was received.
2. The filename carries the message: `p07-02-survey.pdf` is the second attachment
   of message 7, and message 7 is `## Page 7` of the archive's transcription.
3. `MANIFEST.md` in the directory records, per file: the archive it came from,
   the message page, that message's From/Date/Subject, the declared filename, the
   declared media type, the byte count, and the sha256. That is the chain of
   custody, written down.

**Duplication.** A PDF attached to five messages in a thread extracts once —
dedup is by content hash within the archive, and the manifest lists every message
that carried it. Both the disk cost and the fact that it was sent five times are
handled by the same mechanism.

**Opt-in, following `writeback_transcription_md` exactly.** Writing 69 files into
someone's corpus uninvited is not something an indexer should do.
`extract_email_attachments` is settable project-wide or per index, and — like the
transcription writeback — the document's OWN project config wins, because whether
to write next to a document is a property of that document's project and the
daemon never sees it.

**Page text does not mention the extracted paths.** It names every attachment
with its media type and byte count whether or not extraction ran. If the path
appeared, turning extraction on would rewrite every page and re-ingest the whole
archive for no change in what was read.

## What was actually broken (measured, `scratch_probe_test.go`, since removed)

Four of these silently corrupt a transcription. They are the reason to distrust
anything indexed from mail before this change.

- **A nested message over ~4 KB was TRUNCATED.** `readBody` collected
  `*mail.Message` values whose bodies read from a `multipart.Part`, then walked
  them after the loop had advanced past those parts. It appeared to work only
  because `mail.ReadMessage`'s `bufio.Reader` had already pulled the first 4096
  bytes. A 21 KB nested message came out at 3998 bytes, cut mid-sentence, with no
  error. In an archive of nested forwards that is most of the content. Fixed by
  buffering each enclosed message's bytes when it is encountered.
- **base64 text parts were NOT decoded.** `multipart.Reader` decodes
  quoted-printable and *nothing else*, so a base64 `text/plain` or `text/html`
  part landed in the index as raw base64. Fixed.
- **A non-multipart body was not decoded at all.** `mail.ReadMessage` does no
  transfer decoding, so a plain quoted-printable email — the commonest shape there
  is — indexed as `Price =2450,000=20today.` Fixed.
- **`multipart/alternative` was indexed twice**, once as text and once as
  de-tagged HTML, despite a comment claiming plain text won. Fixed: the last
  `text/plain` wins, `text/html` only if there is no plain alternative.
- **A part with no filename was silently dropped** — an inline `application/pdf`
  with no `Content-Disposition` left no trace at all.
- **A `multipart/*` Content-Type with no `boundary` lost the entire body.**

Quoted-printable inside a multipart part was already correct, and stays correct:
`multipart.Part` decodes it AND removes the header, so re-reading the header
cannot double-decode. `TestQuotedPrintableIsNotDecodedTwice` pins that.

## Not done

- **Charset coverage is `golang.org/x/text/encoding/ianaindex`** (promoted to a
  direct dependency), which covers the labels that appear in real mail. An
  unrecognised label leaves the bytes alone: raw is recoverable, a wrong
  transliteration is not.
- **Extraction re-parses the archive.** `EmailText` stays pure — the `ocr` MCP
  tool calls it and a read tool must not write files — so the attachment pass is a
  second walk. Milliseconds on 24 MB; not worth threading a sink through a
  function two callers use differently.
- **An mbox's pages are still all held in memory.** `[]PageText` is the return
  type; the split streams, the result does not. Fine at mailbox scale, wrong at
  mail-server scale. If that ever matters the return type is what has to change.
