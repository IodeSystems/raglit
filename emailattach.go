package raglit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Attachments: making the documents inside an archive readable.
//
// The broker's log in ardley-v-brannock carries 69 attachments and several of
// them are substantive instruments — a survey, a plat, an amendment. Named-only
// is a dead end: you can see that a survey was sent and you cannot read it.
//
// Transcribing them inline was rejected. EmailText has no context and no OCR
// handle and has no business acquiring one — it is the `ocr` MCP tool's read
// path, and a read tool that runs a VLM as a side effect is not a read tool.
// And folding a 30-page survey's text into its covering message's page destroys
// the citation: "p. 7" would mean both the email and the survey inside it.
//
// So: extract byte-for-byte into a sidecar directory beside the archive. The
// files are ordinary files in an ordinary directory, so `raglit sync` and the
// watcher index them with no queue plumbing at all — and unlike a
// transcription they are NOT added to builtinIgnore, because they are not
// raglit's derived output. They are originals that happened to travel inside an
// envelope.
//
// The objection to that is real: an extracted file dropped somewhere anonymous
// is a document with no chain, which is worse than no copy. Three things answer
// it, and all three are load-bearing.
//
//  1. The bytes are the DECODED MIME PART, copied, never converted. The
//     extracted file's sha256 is the sha256 of what was received.
//  2. The filename carries the message: p07-02-survey.pdf is the second
//     attachment of message 7, and message 7 is `## Page 7` of the archive's
//     transcription.
//  3. MANIFEST.md records, per file, the archive, the message page, that
//     message's From/Date/Subject, the declared filename, the declared media
//     type, the byte count and the sha256. That is the chain of custody,
//     written down rather than assumed.
//
// Opt-in, for the same reason the transcription writeback is: writing 69 files
// into somebody's corpus uninvited is not something an indexer should do.

// attachmentDirSuffix names the sidecar after the archive, so which archive a
// directory belongs to is answerable from its name alone — a directory called
// "attachments" next to twelve .eml files answers nothing.
const attachmentDirSuffix = ".raglit-attachments"

// AttachmentDir is where an archive's attachments are extracted to.
func AttachmentDir(archivePath string) string { return archivePath + attachmentDirSuffix }

// manifestName is the chain-of-custody file. Named in caps because a human
// opening the directory should read it first.
const manifestName = "MANIFEST.md"

// ExtractEmailAttachments writes every attachment in the archive at srcPath into
// a sidecar beside archivePath, and returns how many files it wrote.
//
// srcPath and archivePath differ on the ingest path: the worker materialises
// fetched bytes to a temp file, but the sidecar belongs next to the document's
// real location in the corpus. Passing both keeps the bytes and the identity
// from being confused for one another.
//
// The archive is parsed a second time rather than threaded through EmailText.
// EmailText must stay pure — the `ocr` MCP tool calls it — and a second walk of
// 24 MB costs milliseconds, which is not worth a sink parameter that one of the
// two callers would always pass nil for.
func ExtractEmailAttachments(srcPath, archivePath string) (int, string, error) {
	parts, err := readArchive(srcPath, true)
	if err != nil {
		return 0, "", err
	}
	dir := AttachmentDir(archivePath)

	// Rows are built before anything is written, so a manifest is never left
	// describing files that a failure half-created.
	type row struct {
		file, cite, declared, mime, sum string
		size                            int64
		data                            []byte
	}
	var rows []row
	// bySum dedups WITHIN the archive: the same PDF attached to five messages in
	// a thread is one file on disk, cited five times in the manifest. Bounding
	// the disk cost and recording that it was sent five times fall out of the
	// same map.
	bySum := map[string]int{}
	wrote := 0
	for i, p := range parts {
		page := i + 1
		for j, a := range p.attach {
			cite := citeMessage(page, p)
			if at, seen := bySum[a.Sum]; seen {
				// The same bytes under a different declared name is worth saying:
				// a document renamed between two forwards is sometimes the point.
				if d := declaredName(a); d != rows[at].declared {
					cite += " (as " + d + ")"
				}
				rows[at].cite += "<br>" + cite
				continue
			}
			name := fmt.Sprintf("p%02d-%02d-%s", page, j+1, safeName(a.Name, a.Mime))
			bySum[a.Sum] = len(rows)
			rows = append(rows, row{
				file: name, cite: cite, declared: declaredName(a),
				mime: a.Mime, size: a.Size, sum: a.Sum, data: a.inner,
			})
			wrote++
		}
	}
	if wrote == 0 {
		return 0, dir, nil // nothing to extract; do not leave an empty directory
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, dir, err
	}
	for _, r := range rows {
		if err := os.WriteFile(filepath.Join(dir, r.file), r.data, 0o644); err != nil {
			return 0, dir, fmt.Errorf("%s: %w", r.file, err)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Attachments extracted from %s\n\n", filepath.Base(archivePath))
	b.WriteString("Each file below travelled INSIDE the archive named above and was written\n" +
		"here by raglit during ingest. Every one is a byte-for-byte copy of the decoded\n" +
		"MIME part — not a conversion — so its sha256 is the sha256 of what was received.\n\n" +
		"Cite the ARCHIVE and the message, not this copy. A message is `## Page N` of the\n" +
		"archive's transcription, and the filename repeats that N.\n\n" +
		"This manifest is rewritten in full on every ingest: a file in this directory that\n" +
		"is not listed below no longer corresponds to anything in the archive.\n\n")
	b.WriteString("| file | from message | declared name | media type | bytes | sha256 |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, r := range rows {
		mt := r.mime
		if mt == "" {
			mt = "unknown"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %d | `%s` |\n",
			r.file, mdCell(r.cite), mdCell(r.declared), mt, r.size, r.sum)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(b.String()), 0o644); err != nil {
		return wrote, dir, err
	}
	return wrote, dir, nil
}

// citeMessage names the message an attachment came from, in the terms a
// citation uses: the page, then who sent it, when, and about what. The fields
// are labelled — an unlabelled "— Survey" at the end of a row is not something
// a reader should have to infer the meaning of.
func citeMessage(page int, p emailPart) string {
	out := fmt.Sprintf("p. %d", page)
	if f := strings.TrimSpace(p.from); f != "" {
		out += ", from " + f
	}
	if d := strings.TrimSpace(p.date); d != "" {
		out += ", " + d
	}
	if s := strings.TrimSpace(p.subject); s != "" {
		out += `, Subject: "` + s + `"`
	}
	return out
}

func declaredName(a emailAttachment) string {
	if a.Name == "" {
		return "_(none declared)_"
	}
	return a.Name
}

// mdCell keeps a header value from breaking the manifest table. Pipes and
// newlines occur in real Subject lines.
func mdCell(s string) string {
	s = strings.NewReplacer("|", "\\|", "\r", " ", "\n", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// safeName turns a declared filename into one that is safe to create and that
// ClassifyDoc can still route.
//
// A declared name is attacker-controlled text: "../../.ssh/authorized_keys" and
// "C:\Windows\x.pdf" are both things a message can claim. Only the base name
// survives, and only characters that cannot mean anything to a shell or a path.
// The EXTENSION is preserved deliberately — it is what decides whether the
// extracted file is later read as a PDF or as bytes.
func safeName(name, mimeType string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(collapseDashes(b.String()), "-.")
	ext := filepath.Ext(out)
	stem := strings.TrimSuffix(out, ext)
	if stem == "" {
		stem = "unnamed"
		if ext == "" {
			// No name and no extension: ExtForContentType is already the map from
			// a media type to the extension the extractors detect by.
			if ext = ExtForContentType(mimeType); ext == "" {
				ext = ".bin"
			}
		}
	}
	// Cap the stem, not the whole name — truncating away the extension would
	// change how the file is later read.
	if len(stem) > 96 {
		stem = stem[:96]
	}
	if len(ext) > 16 {
		ext = ext[:16]
	}
	return stem + ext
}

func collapseDashes(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// extractAttachmentsForDoc decides the sidecar for ONE archive, the same way
// writebackForDoc decides the transcription: whether to write next to a
// document is a property of that document's project, and the daemon — running
// from its own home with a namespaced index name — never sees that project's
// config. The document's own path can find it.
func extractAttachmentsForDoc(docPath string, fallback bool) bool {
	return projectFlagForDoc(docPath, func(f projectFlags) *bool { return f.ExtractEmailAttachments }, fallback)
}

// archiveCorpusPath is the filesystem path a job URL names, or "" when the job
// has no place beside it to write — an http(s) target, or a directory that is
// gone. Extraction is skipped rather than guessed at: inventing a location for
// a fetched archive's attachments is how a file ends up somewhere with no chain
// back to where it came from.
func archiveCorpusPath(url string) string {
	p := url
	if rest, ok := strings.CutPrefix(p, "file://"); ok {
		p = rest
	}
	if !filepath.IsAbs(p) {
		return ""
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil || !fi.IsDir() {
		return ""
	}
	return p
}

// projectFlags are the per-document booleans a project's own config decides for
// its own documents. A nil field means the project has no opinion and the
// store's setting stands.
type projectFlags struct {
	Writeback               *bool `json:"writeback_transcription_md"`
	ExtractEmailAttachments *bool `json:"extract_email_attachments"`
}

// projectFlagForDoc walks up from a document for the nearest project config and
// lets it decide, falling back to the store's setting when there is none. See
// writebackForDoc for why the document's path, and not the daemon's config, is
// the right place to ask.
// ProjectDirForDoc finds the project a document belongs to, by walking up from
// the document itself until it meets a .raglit/ directory.
//
// The daemon needs this and cannot use ProjectDir(): it runs from its own home
// and its working directory has nothing to do with the corpus. What it always
// has is the document's absolute path, and the corpus layout answers the rest —
// the same walk that already decides per-project writeback settings.
//
// Returns "" when the document sits under no project, which is a real state (an
// ad-hoc file ingested by path) and not an error.
func ProjectDirForDoc(docPath string) string {
	dir := filepath.Dir(docPath)
	for i := 0; i < 12 && dir != "" && dir != "/"; i++ {
		if fi, err := os.Stat(filepath.Join(dir, ProjectHomeName)); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func projectFlagForDoc(docPath string, pick func(projectFlags) *bool, fallback bool) bool {
	dir := filepath.Dir(docPath)
	for i := 0; i < 12 && dir != "" && dir != "/"; i++ {
		p := filepath.Join(dir, ProjectHomeName, "config.json")
		if b, err := os.ReadFile(p); err == nil {
			var cfg projectFlags
			if json.Unmarshal(b, &cfg) == nil {
				if v := pick(cfg); v != nil {
					return *v // the project has an opinion; it wins
				}
			}
			return fallback // a project config with no opinion defers
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return fallback
}
