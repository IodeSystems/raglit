package raglit

import (
	"context"
	"fmt"
	"time"

	gen "github.com/iodesystems/raglit/internal/db"
)

// What a document is a READING OF.
//
// The corpus holds several accounts of the same thing and had no way to say so.
// A hearing recording is transcribed by oidio and indexed; later a person rules
// on the speaker attribution and that verified transcript is indexed too — two
// documents, 40,000 characters each, appearing separately in search with nothing
// to say which one may be quoted. A bundle of SMS screenshots and the PDF
// compiled from them are the same conversation read twice. A scanned sheet read
// whole and read again by region descent is one sheet.
//
// Three facts are needed and they come from different places:
//
//   - WHAT was read — the source, identified by its CONTENT. attest's rule, and
//     the right one: a re-encoded recording is a different asset wearing the same
//     filename, and rulings against the old one do not transfer.
//   - HOW it was read — the method, and whether a person has been through it.
//     Produced by whatever did the reading; recomputable, so it lives in the index.
//   - WHICH reading governs — a ruling. Not recomputable, so it does NOT live
//     here. See AuthoritativeReading.

// Reading levels, in the order authority accrues.
const (
	// ReadingMachine — a model's output, unreviewed. Every ingest starts here.
	ReadingMachine = "machine"
	// ReadingAdapted — a machine reading a person has changed: a corrected page,
	// a re-attributed speaker turn. Better than machine and not attested, because
	// nobody has said the whole of it is right.
	ReadingAdapted = "adapted"
	// ReadingAttested — a person has ruled on it. What may be quoted.
	ReadingAttested = "attested"
)

// Reading methods — how the text was produced.
const (
	MethodASR       = "oidio-asr"
	MethodVision    = "vision-ocr"
	MethodTextLayer = "text-layer"
	MethodRegion    = "region"
	MethodPandoc    = "pandoc"
	MethodEmail     = "email-mime"
	MethodCompiled  = "compiled"
	MethodVerbatim  = "verbatim"
)

// Reading is one account of one source.
type Reading struct {
	SourceSHA256 string `json:"source_sha256,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	DocPath      string `json:"doc_path"`
	Method       string `json:"method,omitempty"`
	Level        string `json:"level"`
	ProducedBy   string `json:"produced_by,omitempty"`
	RuledBy      string `json:"ruled_by,omitempty"`
	At           int64  `json:"at,omitempty"`
	// Text is the reading itself — what gets indexed when this reading governs.
	Text string `json:"text,omitempty"`
	// Data is the structured form the text was rendered from, as an attest.Reading:
	// units, each with a locator into the asset.
	//
	// ONE shape for every kind, which is attest's whole design — a Unit is "one
	// machine claim about one piece of the asset", and the locator differs by
	// kind rather than the model differing. A diarized recording's units are
	// turns located by TIME; a scanned sheet's are page or region reads located
	// by AREA; a mail archive's are messages located by SPAN. So an audio
	// transcript and a page transcription are attestable in the same way, by the
	// same log, and a correction to either is an attestation rather than an edit
	// to what the machine said.
	//
	// Empty is normal — a plain text extraction has no structure worth keeping
	// beyond its text.
	Data string `json:"data,omitempty"`
}

// RecordReading records what a document is a reading of. Idempotent per document
// — a re-ingest replaces the row, because it is a new reading of the same thing.
//
// An empty SourceSHA256 is accepted and stored. A producer that cannot say what
// it read is a fact worth keeping: oidio's diarized.json names no media and
// carries no digest, so every transcript derived from one arrives here unable to
// point at its recording. Refusing the row would hide that; storing it makes the
// gap countable and fixable.
func (s *Store) RecordReading(r Reading) error {
	if r.DocPath == "" {
		return fmt.Errorf("raglit: a reading needs the document it belongs to")
	}
	if r.Level == "" {
		r.Level = ReadingMachine
	}
	if r.At == 0 {
		r.At = time.Now().UnixNano()
	}
	return s.q.UpsertReading(context.Background(), gen.UpsertReadingParams{
		SourceSha256: r.SourceSHA256, SourcePath: r.SourcePath, DocPath: r.DocPath,
		Method: r.Method, Level: r.Level, ProducedBy: r.ProducedBy, RuledBy: r.RuledBy, At: r.At,
		Text: r.Text, Data: r.Data,
	})
}

// ReadingFor returns what this document is a reading of, if anything is recorded.
func (s *Store) ReadingFor(docPath string) (Reading, bool, error) {
	row, err := s.q.GetReadingForDoc(context.Background(), docPath)
	if err != nil {
		return Reading{}, false, nil //nolint:nilerr // absent is not an error
	}
	return readingFrom(row), true, nil
}

// ReadingsOfSource returns every reading of one source, oldest first.
//
// Empty for an empty digest, deliberately: readings that could not name their
// source are not all readings of each other, and grouping them would assert a
// relationship nobody established.
func (s *Store) ReadingsOfSource(sha string) ([]Reading, error) {
	if sha == "" {
		return nil, nil
	}
	rows, err := s.q.ListReadingsOfSource(context.Background(), sha)
	if err != nil {
		return nil, err
	}
	out := make([]Reading, 0, len(rows))
	for _, r := range rows {
		out = append(out, readingFrom(r))
	}
	return out, nil
}

// SiblingReadings returns the OTHER readings of whatever this document reads —
// the answer to "show me the raw one" from a verified transcript, and the
// reverse.
func (s *Store) SiblingReadings(docPath string) ([]Reading, error) {
	me, ok, err := s.ReadingFor(docPath)
	if err != nil || !ok || me.SourceSHA256 == "" {
		return nil, err
	}
	all, err := s.ReadingsOfSource(me.SourceSHA256)
	if err != nil {
		return nil, err
	}
	out := make([]Reading, 0, len(all))
	for _, r := range all {
		if r.DocPath != docPath {
			out = append(out, r)
		}
	}
	return out, nil
}

func readingFrom(r gen.Reading) Reading {
	return Reading{
		SourceSHA256: r.SourceSha256, SourcePath: r.SourcePath, DocPath: r.DocPath,
		Method: r.Method, Level: r.Level, ProducedBy: r.ProducedBy, RuledBy: r.RuledBy, At: r.At,
		Text: r.Text, Data: r.Data,
	}
}

// levelRank orders the reading levels. Used only as the DEFAULT when nobody has
// ruled — a person's ruling always wins, which is why it is not stored.
func levelRank(level string) int {
	switch level {
	case ReadingAttested:
		return 30
	case ReadingAdapted:
		return 20
	case ReadingMachine:
		return 10
	}
	return 0
}
