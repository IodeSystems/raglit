package raglit


// Scheduling lanes — what a job COSTS, decided before it runs.
//
// The queue used to be one serial round-robin: one goroutine, one job at a time,
// one index per turn (cmd/raglit/serve.go). So a forty-minute OCR of a
// twenty-two page scan held every other index's turn, and a markdown file that
// needs three seconds of embedding waited behind it. That is the complaint this
// answers, and it is not a throughput problem — the two kinds of work do not
// even contend for the same resource.
//
// LaneHeavy is vision: a page at a time through a VLM, minutes per document, and
// bounded by ONE GPU admission slot. Running two at once does not go faster; it
// queues inside the endpoint where nothing here can see or resume them. That is
// the same reason the identity queue drains one index at a time, and the reason
// OCR moved to a model on the second card.
//
// LaneLight is everything else: pandoc, a mail parser, a spreadsheet reader, the
// deterministic fragmenter, and embedding — which is a batched call measured in
// seconds. Several at once is fine and is the point.
//
// The split is by RESOURCE, not by speed. A large text file is not fast, and it
// still belongs in light, because what makes heavy serial is the GPU slot rather
// than the wall clock.
type Lane string

const (
	LaneHeavy Lane = "heavy"
	LaneLight Lane = "light"
)

// DefaultLaneSlots is how many jobs each lane runs at once.
//
// Heavy is 1 and should stay 1 unless the vision endpoint genuinely admits more
// than one request: a second concurrent page does not start earlier, it blocks
// inside the server, and its retries and timeouts are then invisible here.
//
// Light is 3 rather than "as many as there are indexes". The ceiling that
// matters is the embedding endpoint, which is one server however many callers
// there are; three keeps it busy across a batch without turning one raglit into
// something that needs its own rate limit.
var DefaultLaneSlots = map[Lane]int{LaneHeavy: 1, LaneLight: 3}

// LaneForKind maps a routed document to the lane that can afford it.
func LaneForKind(k DocKind) Lane {
	switch k {
	case KindPDF, KindImage:
		return LaneHeavy
	}
	return LaneLight
}

// LaneFor guesses a lane from a URL alone, which is all an enqueue has.
//
// A guess, and deliberately so: the real decision needs the BYTES (Worker.route
// sniffs content when the name says nothing), and a queue that fetched every
// document to decide where to queue it would do the expensive half of the work
// twice. So the extension decides, and the worker corrects the row once it
// knows — see Worker.ProcessJob.
//
// Two ways it is wrong, both survivable. A PDF that turns out to carry a text
// layer is read without a model, so it occupied the heavy lane for seconds
// instead of minutes: wasteful, not incorrect. An extensionless file that sniffs
// to a scan runs its OCR in the light lane, which is the case worth naming — one
// light slot is busy for minutes. It is one job, the row is corrected, and a
// retry goes to the right lane.
func LaneFor(url string) Lane {
	// ClassifyDoc is the same router the worker uses, given no content type.
	// Sharing it is the point: a lane assignment that disagreed with the reader
	// would send documents to a lane chosen by a second, drifting opinion.
	kind := ClassifyDoc(url, "")
	if kind == KindUnknown {
		// Nothing in the name said what this is. Light, because it is the lane
		// that can lose a slot to a surprise without stalling the other one —
		// heavy has a single slot, and a text file misfiled into it blocks every
		// scan behind it for as long as it runs.
		return LaneLight
	}
	return LaneForKind(kind)
}
