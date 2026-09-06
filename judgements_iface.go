package raglit

// The judgement surface, and why it is an interface.
//
// Today's implementation answers every question by walking maps built from
// raglit-audit.jsonl at open. That is right for a trail of a few thousand events
// and wrong at some size nobody has measured yet.
//
// So callers depend on this and never on the implementation. When a measurement
// says the walk is too slow, a cached implementation — a SQLite projection, an
// mmap'd index, whatever the measurement argues for — satisfies the same
// interface and is swapped in at construction. Nothing above changes, and the
// trail stays the record either way.
//
// The write methods are here deliberately. A cache that could be read through
// but written around would drift on the first write that bypassed it.
type Judgements interface {
	// Relations
	PutRelation(m Mark) error
	Relation(a, b string) (Mark, bool, error)
	Relations() ([]Mark, error)
	RelationsFor(doc string) ([]Mark, error)

	// Slices
	PutSlice(sl Slice) error
	DeleteSlice(id string) error
	Slice(id string) (Slice, bool, error)
	Slices() ([]Slice, error)
	SlicesOf(parent string) ([]Slice, error)
	SliceParents() ([]string, error)
	CoverageOf(parent string, pages int) (Coverage, error)

	// Page readings
	PutPageCorrection(c PageCorrection) error
	PageCorrections(doc string) (map[int]PageCorrection, error)
	PageReadings(doc string, page int) []PageCorrection

	// Provenance
	History(kind, subject string) ([]AuditEvent, error)

	// Notification
	OnTranscriptionChange(fn func(TranscriptionChange))

	Close() error
}

// TranscriptionChange reports that a page's ACTIVE reading changed.
//
// The event exists because a correction invalidates things it does not own. The
// .raglit-transcription.md export beside the document still shows the old text;
// the index still holds it in fragments, so a search still returns the reading
// a person just replaced. Neither notices on its own.
//
// Superseded carries what was replaced rather than only what won. A correction
// is an attestation that a better reading exists, not an erasure: the old text
// is what the index held when a document was cited, and it is what a stale
// quotation elsewhere will still match.
type TranscriptionChange struct {
	Doc  string
	Page int
	// Active is the reading now in force.
	Active PageCorrection
	// Superseded is the text it replaced — a machine read, or an earlier
	// correction. Empty when this is the first recorded reading for the page.
	Superseded string
}

// Compile-time proof that the trail-backed store satisfies the surface. When a
// cached implementation arrives, it gets a line here too and the compiler keeps
// them honest.
var _ Judgements = (*JudgementStore)(nil)
