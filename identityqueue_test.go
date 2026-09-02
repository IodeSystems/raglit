package raglit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/agentkit/llm"
)

// countingChatter answers every call with a valid identity and records the
// HIGH-WATER MARK of concurrent calls — the number the slot budget exists to
// hold down.
type countingChatter struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    int
	delay    time.Duration
}

func (c *countingChatter) ChatStream(_ context.Context, _ []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
	c.inFlight++
	c.calls++
	n := c.calls
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()
	time.Sleep(c.delay) // hold the "slot" long enough for overlap to be observable
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return streamReply(fmt.Sprintf(
		`{"name":"Document number %d, captioned","summary":"A document held in this corpus, summarised for the purpose of this test at sufficient length.","kind":"analysis","content_tags":["purchase agreement","parcel conveyance","closing terms"],"role_tags":["reference"]}`, n)), nil
}

// storeWithDocs is an in-memory index holding n captionable documents.
func storeWithDocs(t *testing.T, n int) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 0; i < n; i++ {
		if err := s.Ingest(context.Background(), Document{
			Path:      fmt.Sprintf("/corpus/scan_%03d.pdf", i),
			Title:     fmt.Sprintf("scan_%03d.pdf", i),
			Fragments: []Fragment{{Page: 1, Ord: 0, Text: psaText}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestIdentityQueue_EnqueueIsIdempotentPerDocument(t *testing.T) {
	s := storeWithDocs(t, 1)
	p := "/corpus/scan_000.pdf"
	first, err := s.EnqueueIdentity(p, false)
	if err != nil || !first {
		t.Fatalf("first enqueue = %v, %v", first, err)
	}
	// A second ask while the first is still pending is not more work — it is the
	// same document, and a duplicate row is a duplicate model call.
	again, err := s.EnqueueIdentity(p, false)
	if err != nil || again {
		t.Fatalf("second enqueue = %v, %v — want it skipped", again, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 1 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	// Once it is finished, asking again is a fresh job.
	job, err := s.claimNextIdentityJob()
	if err != nil || job == nil {
		t.Fatalf("claim = %v, %v", job, err)
	}
	if err := s.finishIdentityJob(job.ID, nil); err != nil {
		t.Fatal(err)
	}
	revived, err := s.EnqueueIdentity(p, true)
	if err != nil || !revived {
		t.Fatalf("re-enqueue after done = %v, %v", revived, err)
	}
}

func TestIdentityWorker_DrainsAtTheSlotBudget(t *testing.T) {
	s := storeWithDocs(t, 8)
	c := &countingChatter{delay: 40 * time.Millisecond}
	s.SetIdentifier(NewIdentifier(c, "m"))
	n, err := s.EnqueueMissingIdentities(false)
	if err != nil || n != 8 {
		t.Fatalf("queued %d, %v", n, err)
	}

	w := &IdentityWorker{Store: s, Slots: 2}
	done, err := w.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done != 8 {
		t.Fatalf("drained %d, want 8", done)
	}
	// The whole point of the queue: never more requests in the model than the
	// endpoint actually serves.
	c.mu.Lock()
	maxSeen := c.maxSeen
	c.mu.Unlock()
	if maxSeen > 2 {
		t.Errorf("%d concurrent model calls — the slot budget is 2", maxSeen)
	}
	if maxSeen < 2 {
		t.Errorf("never reached 2 concurrent calls (max %d) — the slots are not being used", maxSeen)
	}

	q, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q.Pending != 0 || q.Running != 0 || q.Done != 8 || q.Failed != 0 {
		t.Fatalf("queue after drain = %+v", q)
	}
	missing, err := s.DocumentsMissingIdentity()
	if err != nil || len(missing) != 0 {
		t.Fatalf("%d document(s) still uncaptioned: %v", len(missing), err)
	}
}

// An interrupted sweep leaves the rest of the work queued, not lost. This is the
// property the whole table exists for.
func TestIdentityWorker_StoppingLeavesTheRestQueued(t *testing.T) {
	s := storeWithDocs(t, 6)
	c := &countingChatter{delay: 30 * time.Millisecond}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueMissingIdentities(false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &IdentityWorker{Store: s, Slots: 2}
	w.OnDone = func(IdentityJob, DocIdentity, error) { cancel() } // stop after the first
	if _, err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	q, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q.Done+q.Failed == 6 {
		t.Fatal("the whole queue drained; this test cannot say anything about resuming")
	}
	if q.Pending+q.Running == 0 {
		t.Fatalf("nothing left queued after an interrupted sweep: %+v", q)
	}
	// Resuming finishes the rest, and does not redo what was done.
	before := q.Done
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	q2, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q2.Pending != 0 || q2.Done < before {
		t.Fatalf("resume left %+v (was %+v)", q2, q)
	}
}

// A document somebody already answered for costs no model call, and its row
// still closes — otherwise every sweep re-asks the same questions forever.
func TestIdentityWorker_SkipsWhatIsAlreadyAnswered(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	if _, err := s.RecordIdentity(ctx, "/corpus/scan_000.pdf",
		DocIdentity{Name: "Mine", Summary: "A caption a person wrote, long enough to be real.", Kind: "deed", ContentTags: []string{"property transfer", "conveyance", "ownership record"}, RoleTags: []string{"reference"}}, "carl"); err != nil {
		t.Fatal(err)
	}
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	// Queue everything, including the one a person named.
	if _, err := s.EnqueueIdentityFor([]string{"/corpus/scan_000.pdf", "/corpus/scan_001.pdf"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	calls := c.calls
	c.mu.Unlock()
	if calls != 1 {
		t.Errorf("model calls = %d, want 1 — a person's caption must not be re-asked", calls)
	}
	id, err := s.DocumentIdentity("/corpus/scan_000.pdf")
	if err != nil || !id.ByPerson() || id.Name != "Mine" {
		t.Fatalf("person's caption = %+v, %v", id, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 0 || q.Failed != 0 {
		t.Fatalf("queue = %+v, %v — a skip is not a failure and not a retry", q, err)
	}
}

// A row left 'running' by a process that is gone is work nobody is doing.
func TestReclaimIdentityJobs_RequeuesOrphans(t *testing.T) {
	s := storeWithDocs(t, 1)
	if _, err := s.EnqueueIdentity("/corpus/scan_000.pdf", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimNextIdentityJob(); err != nil {
		t.Fatal(err)
	}
	// Re-stamp the claim as a pid that cannot be alive.
	if _, err := s.db.Exec(`UPDATE identity_jobs SET owner_pid = 2147483647 WHERE state='running'`); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReclaimIdentityJobs()
	if err != nil || n != 1 {
		t.Fatalf("reclaimed %d, %v", n, err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 1 || q.Running != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	// Our OWN running rows are not reclaimed out from under us.
	if _, err := s.claimNextIdentityJob(); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReclaimIdentityJobs(); err != nil || n != 0 {
		t.Fatalf("reclaimed a live worker's own row: %d, %v", n, err)
	}
}

// A failure is recorded on the row with its reason, and does not stop the sweep.
func TestIdentityWorker_RecordsAFailureAndCarriesOn(t *testing.T) {
	s := storeWithDocs(t, 2)
	// A model that never returns valid JSON: every document fails identically.
	s.SetIdentifier(NewIdentifier(&identityChatter{replies: []string{"no."}}, "m"))
	if _, err := s.EnqueueMissingIdentities(false); err != nil {
		t.Fatal(err)
	}
	n, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("worked %d jobs, want 2 — a failure must not stop the queue", n)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Failed != 2 || q.Pending != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	jobs, err := s.IdentityJobs("error", 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("error jobs = %d, %v", len(jobs), err)
	}
	if !strings.Contains(jobs[0].Error, "identity") {
		t.Errorf("the row does not say why it failed: %q", jobs[0].Error)
	}
}

// A document with nothing to read is SKIPPED, not failed — and a later sweep
// does not queue it again. Counting it as a failure puts a permanent red number
// on a corpus that is fine, and re-queueing it makes work that can never
// succeed look outstanding forever.
func TestIdentityWorker_NothingToCaptionIsSkippedNotFailed(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Ingest(ctx, Document{Path: "/corpus/blank.pdf", Title: "blank.pdf",
		Fragments: []Fragment{{Page: 1, Ord: 0, Text: "p. 3"}}}); err != nil {
		t.Fatal(err)
	}
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if n, err := s.EnqueueMissingIdentities(false); err != nil || n != 1 {
		t.Fatalf("queued %d, %v", n, err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	calls := c.calls
	c.mu.Unlock()
	if calls != 0 {
		t.Errorf("model calls = %d — an empty document should never reach the model", calls)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Skipped != 1 || q.Failed != 0 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	jobs, err := s.IdentityJobs("skipped", 5)
	if err != nil || len(jobs) != 1 || !strings.Contains(jobs[0].Error, "no transcript to read") {
		t.Fatalf("skipped rows = %+v, %v", jobs, err)
	}
	// A bulk sweep leaves it alone: nothing changed upstream, so asking again
	// would get the same answer.
	if n, err := s.EnqueueMissingIdentities(false); err != nil || n != 0 {
		t.Fatalf("re-queued %d, %v — a skip must not come back on a plain sweep", n, err)
	}
	// --force still reaches it: "nothing to read" stops being true after a re-OCR.
	if n, err := s.EnqueueMissingIdentities(true); err != nil || n != 1 {
		t.Fatalf("--force queued %d, %v", n, err)
	}
}

// The edge that makes this a graph rather than two sweeps: a document that
// acquires a transcript owes a caption, and the commit that gives it one says
// so. Without it, the lead-based-paint disclosure re-read into real text would
// have sat there with the skip a previous run recorded when the page held
// nothing but a signature stamp.
func TestCommitDoc_QueuesTheCaptionItNowOwes(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))

	// First read: a scan whose text layer is a signature overlay. Nothing to
	// caption, and the queue records that.
	if err := s.commitDoc("/corpus/lead-paint.pdf", "lead-paint.pdf", "text-overlap", "r",
		[]stagedFrag{{page: 1, ord: 0, text: "Authentisign ID: 2311E4FA X"}}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.IdentityQueue(); q.Skipped != 1 {
		t.Fatalf("queue after the thin read = %+v, want 1 skipped", q)
	}

	// Re-read: the page goes to OCR and real text lands. The commit re-arms the
	// caption on its own — nobody re-runs `identify`.
	if err := s.commitDoc("/corpus/lead-paint.pdf", "lead-paint.pdf", "llm-seg", "r2",
		[]stagedFrag{{page: 1, ord: 0, text: "Form 22J Lead Based Paint Disclosure, Rev. 3/21, page 2 of 2. Disclosure of information on lead-based paint and lead-based paint hazards, continued. Buyer's acknowledgment: the buyer has received copies of all information listed above, has received the pamphlet Protect Your Family from Lead in Your Home, and has waived the opportunity to conduct a risk assessment."}},
		nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	q, err := s.IdentityQueue()
	if err != nil || q.Pending != 1 {
		t.Fatalf("queue after the re-read = %+v, %v — the commit must re-arm the caption", q, err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.DocumentIdentity("/corpus/lead-paint.pdf")
	if err != nil || id.Empty() {
		t.Fatalf("identity after the re-read = %+v, %v", id, err)
	}

	// And a document that already has a caption is not re-asked on every commit.
	before := c.calls
	if err := s.commitDoc("/corpus/lead-paint.pdf", "lead-paint.pdf", "llm-seg", "r3",
		[]stagedFrag{{page: 1, ord: 0, text: "Form 22J Lead Based Paint Disclosure, re-read again with no change of substance."}},
		nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if c.calls != before {
		t.Errorf("model calls %d → %d: a captioned document was re-asked", before, c.calls)
	}
}

// With no model configured, nothing accumulates: an index that does not caption
// should not carry a queue of work nobody will ever do.
func TestCommitDoc_QueuesNothingWithoutAModel(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.commitDoc("/corpus/x.pdf", "x.pdf", "text-overlap", "r",
		[]stagedFrag{{page: 1, ord: 0, text: "some text"}}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.IdentityQueue(); q.Pending != 0 {
		t.Fatalf("queued %+v with no identity model configured", q)
	}
}

// The edge re-arms when the TEXT changes, not only when a caption is missing.
//
// A re-read replaces a transcript wholesale — a page that was a signature stamp
// becomes an agreement — and the caption written from the old one still
// describes the stamp. "Has no caption" could not detect that; the hash of the
// text a caption was written from can.
func TestCommitDoc_ReArmsWhenTheTextChangesUnderACaption(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := &countingChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	const doc = "/corpus/psa.pdf"

	commit := func(text string) {
		t.Helper()
		if err := s.commitDoc(doc, "psa.pdf", "llm-seg", "r",
			[]stagedFrag{{page: 1, ord: 0, text: text}}, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	drain := func() {
		t.Helper()
		if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}

	commit("Authentisign ID: 2311E4FA. X. " + psaText)
	drain()
	first, err := s.DocumentIdentity(doc)
	if err != nil || first.Empty() || first.TextHash == "" {
		t.Fatalf("first caption = %+v, %v", first, err)
	}
	calls := c.calls

	// Committing the SAME text again owes nothing.
	commit("Authentisign ID: 2311E4FA. X. " + psaText)
	drain()
	if c.calls != calls {
		t.Errorf("an unchanged re-ingest re-captioned (%d → %d calls)", calls, c.calls)
	}

	// A re-read that actually changes the text does.
	commit("EXHIBIT A. Legal description of the parcel, metes and bounds, together with the appurtenant easement recorded under auditor's file number 202107080106, and the signatures of both parties, witnessed and acknowledged before a notary public in and for the State of Washington, residing at Mount Vernon.")
	if q, _ := s.IdentityQueue(); q.Pending != 1 {
		t.Fatalf("queue after a changed transcript = %+v, want 1 pending", q)
	}
	drain()
	if c.calls != calls+1 {
		t.Errorf("a changed transcript did not re-caption (%d → %d calls)", calls, c.calls)
	}
	second, err := s.DocumentIdentity(doc)
	if err != nil || second.TextHash == first.TextHash {
		t.Fatalf("the caption still claims the old text: %+v", second)
	}

	// A person's caption is never re-armed by a re-read.
	if _, err := s.RecordIdentity(ctx, doc, DocIdentity{Name: "Mine, and it stays", ContentTags: []string{"property transfer", "conveyance", "ownership record"}, RoleTags: []string{"reference"}}, "carl"); err != nil {
		t.Fatal(err)
	}
	calls = c.calls
	commit("A third and entirely different transcript of this document, long enough to clear the floor below which there is nothing worth captioning, and different in every word from the two that came before it.")
	drain()
	if c.calls != calls {
		t.Errorf("a re-read re-captioned over a person's ruling (%d → %d calls)", calls, c.calls)
	}
	if got, _ := s.DocumentIdentity(doc); got.Name != "Mine, and it stays" {
		t.Errorf("person's caption = %+v", got)
	}
}

// promptChatter answers like countingChatter and keeps every prompt it was
// sent, under a lock — the worker calls it from every slot at once.
type promptChatter struct {
	mu      sync.Mutex
	prompts []string
}

func (c *promptChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
	for _, m := range msgs {
		for _, p := range m.Parts {
			c.prompts = append(c.prompts, p.Text)
		}
	}
	c.mu.Unlock()
	return streamReply(
		`{"name":"A document captioned for this test","summary":"A document held in this corpus, summarised for the purpose of this test at sufficient length.","kind":"analysis","content_tags":["purchase agreement","parcel conveyance","closing terms"],"role_tags":["reference"]}`), nil
}

// The queue is the path that captions a whole corpus, so it is the path where
// tags drift. It must carry the index's established vocabulary into the prompt
// — a mechanism wired only into the one-off IdentifyDocument would never see
// the hundreds of documents it exists for.
func TestIdentityWorker_CarriesTheIndexVocabularyIntoThePrompt(t *testing.T) {
	s := storeWithDocs(t, 2)
	ctx := context.Background()
	// One document already tagged, by a person, so the worker declines to
	// re-caption it and the only prompt observed is the OTHER document's.
	if _, err := s.RecordIdentity(ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "Mine", Summary: "A caption a person wrote, long enough to be real.", Kind: "deed",
		ContentTags: []string{"lead paint abatement"}, RoleTags: []string{"reference"},
	}, "carl"); err != nil {
		t.Fatal(err)
	}
	c := &promptChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueIdentity("/corpus/scan_001.pdf", false); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.prompts) == 0 {
		t.Fatal("the worker asked nothing")
	}
	for _, p := range c.prompts {
		if !strings.Contains(p, "lead paint abatement") {
			t.Errorf("prompt carries no existing vocabulary:\n%s", p)
		}
	}
}

// tagsChatter answers the TAGS-ONLY ask, and refuses the full one — so a test
// that thinks it is backfilling tags cannot quietly be rewriting captions.
type tagsChatter struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (c *tagsChatter) ChatStream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
	_ *llm.ChatOpts) (<-chan llm.StreamChunk, error) {
	c.mu.Lock()
	c.calls++
	for _, m := range msgs {
		for _, p := range m.Parts {
			c.prompts = append(c.prompts, p.Text)
		}
	}
	c.mu.Unlock()
	return streamReply(
		`{"content_tags":["lead paint","boundary survey","escrow closing"],"role_tags":["report"]}`), nil
}

// The backfill for a corpus captioned before tags existed. What it must NOT do
// is the reason it exists: a full re-identify would rewrite hundreds of names
// that are already right, a person's among them.
func TestIdentityWorker_TagsOnlyLeavesTheCaptionAndItsAuthorAlone(t *testing.T) {
	s := storeWithDocs(t, 1)
	ctx := context.Background()
	doc := "/corpus/scan_000.pdf"
	before, err := s.RecordIdentity(ctx, doc, DocIdentity{
		Name: "Mine", Summary: "A caption a person wrote, long enough to be real.", Kind: "deed",
	}, "carl")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.ContentTags) != 0 {
		t.Fatalf("the fixture starts tagged: %+v", before)
	}

	c := &tagsChatter{}
	s.SetIdentifier(NewIdentifier(c, "m"))
	if _, err := s.EnqueueTags(doc, false); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.DocumentIdentity(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ContentTags) != 3 || got.ContentTags[0] != "lead paint" {
		t.Errorf("content tags = %v", got.ContentTags)
	}
	if len(got.RoleTags) != 1 || got.RoleTags[0] != "report" {
		t.Errorf("role tags = %v", got.RoleTags)
	}
	if got.Name != before.Name || got.Summary != before.Summary || got.Kind != before.Kind {
		t.Errorf("the caption changed: %+v", got)
	}
	// A person's caption stays a person's. If a backfill demoted it to
	// gen_source='machine', the next --force sweep would overwrite it.
	if !got.ByPerson() || got.Model != before.Model || got.At != before.At {
		t.Errorf("authorship changed: %+v (was %+v)", got, before)
	}
	// And the caption still says which text it was written from — stamping it
	// with text no caption was read from would silence the staleness re-arm.
	if got.TextHash != before.TextHash {
		t.Errorf("text hash moved: %q → %q", before.TextHash, got.TextHash)
	}

	// Already tagged: a second sweep is not more work.
	c.mu.Lock()
	first := c.calls
	c.mu.Unlock()
	if _, err := s.EnqueueTags(doc, false); err != nil {
		t.Fatal(err)
	}
	if _, err := (&IdentityWorker{Store: s, Slots: 2}).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls != first {
		t.Errorf("a tagged document was asked again: %d → %d calls", first, c.calls)
	}
}

func TestDocumentsMissingTags_SelectsCaptionedDocumentsOnly(t *testing.T) {
	s := storeWithDocs(t, 3)
	ctx := context.Background()
	// Captioned, untagged — the backfill's work.
	if err := s.SetDocumentIdentity(ctx, "/corpus/scan_000.pdf", DocIdentity{
		Name: "Captioned before tags existed", Summary: "A summary long enough to be a real one.",
		Kind: "deed", Source: IdentityByMachine, Model: "m", At: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Captioned AND tagged — done.
	if err := s.SetDocumentIdentity(ctx, "/corpus/scan_001.pdf", DocIdentity{
		Name: "Captioned and tagged", Summary: "A summary long enough to be a real one.",
		Kind: "deed", Source: IdentityByMachine, Model: "m", At: 1,
		ContentTags: []string{"lead paint"}, RoleTags: []string{"report"},
	}); err != nil {
		t.Fatal(err)
	}
	// scan_002 has no caption at all — that is `identify`'s work, not this.
	paths, err := s.DocumentsMissingTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/corpus/scan_000.pdf" {
		t.Fatalf("missing tags = %v", paths)
	}
	n, err := s.EnqueueMissingTags(false)
	if err != nil || n != 1 {
		t.Fatalf("enqueued %d, %v — want 1", n, err)
	}
}

// A Drain reclaims orphans before it starts, whoever called it.
//
// ReclaimIdentityJobs was correct and was wired into ONE of the four places a
// drain happens — `serve.go`. The CLI paths a person actually types,
// `raglit identify` and `raglit fields`, never called it, so a row left running
// by an interrupted sweep stayed running forever: not pending, so no drain
// claimed it; not terminal, so no report counted it failed.
//
// Measured on the FDA corpus: two interrupted sweeps stranded 6 documents, and a
// later full sweep reported "identified 66 document(s)" while those 6 stayed
// uncaptioned and unmentioned. The queue read as busy with nothing running.
func TestDrain_ReclaimsOrphansBeforeWorking(t *testing.T) {
	s := storeWithDocs(t, 1)
	s.SetIdentifier(NewIdentifier(&capChatter{reply: `{"name":"A deed",` +
		`"summary":"A statutory warranty deed dated 1994 conveying Lot 4 to B. Jones.",` +
		`"kind":"deed","content_tags":["lot 4","warranty deed","brannock plat"],` +
		`"role_tags":["reference"]}`}, "m"))

	if _, err := s.EnqueueMissingIdentities(false); err != nil {
		t.Fatal(err)
	}
	// Strand it exactly the way a killed process does: running, owned by a pid
	// that is not here any more. 1 is init, which is alive and not us — so a pid
	// that cannot be running this test is needed. os.Getpid()+(1<<21) is past
	// any legal pid on Linux.
	dead := int64(os.Getpid()) + (1 << 21)
	if _, err := s.db.Exec(
		`UPDATE identity_jobs SET state='running', owner_pid=?`, dead); err != nil {
		t.Fatal(err)
	}

	var reclaimed int
	w := &IdentityWorker{Store: s, Slots: 1, OnReclaim: func(n int) { reclaimed = n }}
	if _, err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reclaimed != 1 {
		t.Errorf("reclaimed %d, want 1 — the stranded row was not requeued", reclaimed)
	}
	q, err := s.IdentityQueue()
	if err != nil {
		t.Fatal(err)
	}
	if q.Running != 0 || q.Done != 1 {
		t.Errorf("queue = %+v, want the orphan worked to done and nothing left running", q)
	}
}
