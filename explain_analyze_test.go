package raglit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestExplain_WritesNothing proves Explain computes the full pre-flight report
// and leaves the store exactly as it found it: no new document, fragment, job,
// job stage, or identity queue item. The report is complete (it made the
// decisions the real ingest would make) and the index is untouched.
func TestExplain_WritesNothing(t *testing.T) {
	s := storeWithDoc(t)
	const path = "/corpus/0428_001.pdf"
	if err := s.SetDocumentHash(path, "stored-hash"); err != nil {
		t.Fatal(err)
	}
	reg := &Registry{} // no embedder, no identifier configured

	counts := func() (docs, frags, jobs, stages, idq int) {
		for _, sc := range []struct {
			table string
			n     *int
		}{
			{"documents", &docs}, {"fragments", &frags}, {"ingest_jobs", &jobs},
			{"job_stages", &stages}, {"identity_jobs", &idq},
		} {
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + sc.table).Scan(sc.n); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	bDocs, bFrags, bJobs, bStages, bIDQ := counts()

	rep, err := Explain(context.Background(), reg, s, ExplainInput{URL: path, Index: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// The report made the decisions: kind, lane, fragmenter, doc identity.
	if rep.Kind == "" || rep.Lane == "" || rep.Fragmenter == "" || rep.DocIdentity != path {
		t.Fatalf("incomplete report: %+v", rep)
	}
	if !rep.DocExists {
		t.Error("report does not see the stored document's identity")
	}
	// And it wrote nothing.
	aDocs, aFrags, aJobs, aStages, aIDQ := counts()
	if aDocs != bDocs || aFrags != bFrags || aJobs != bJobs || aStages != bStages || aIDQ != bIDQ {
		t.Fatalf("Explain changed the store: docs %d→%d frags %d→%d jobs %d→%d stages %d→%d idq %d→%d",
			bDocs, aDocs, bFrags, aFrags, bJobs, aJobs, bStages, aStages, bIDQ, aIDQ)
	}
}

// TestExplain_HashShortCircuits proves the report says when a stored document's
// content hash matches its source file: the real ingest would skip it.
func TestExplain_HashShortCircuits(t *testing.T) {
	reg := &Registry{}
	f := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(f, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Ingest(context.Background(), Document{
		Path: f, Fragments: []Fragment{{Page: 1, Ord: 0, Text: "content"}},
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("content"))
	if err := s.SetDocumentHash(f, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	rep, err := Explain(context.Background(), reg, s, ExplainInput{URL: f, Index: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DocExists {
		t.Error("DocExists false for a stored document")
	}
	if !rep.HashShortCircuits {
		t.Error("HashShortCircuits false when the stored hash matches the source file")
	}
}

// TestAnalyze_DumpsFragmentsAsStored proves that Analyze returns the
// document's own fragments with their stored fields (text, ord, page,
// page_spans, origin) intact, and that the identity fragment is identified
// separately.
func TestAnalyze_DumpsFragmentsAsStored(t *testing.T) {
	s := storeWithDoc(t)
	ctx := context.Background()
	const path = "/corpus/0428_001.pdf"

	// Add the identity so there is one to dump.
	if err := s.SetDocumentIdentity(ctx, path, machineIdentity()); err != nil {
		t.Fatal(err)
	}

	rep, err := Analyze(ctx, s, path)
	if err != nil {
		t.Fatal(err)
	}

	// Fragments as stored: the two body fragments, in page/ord order.
	if len(rep.Fragments) != 2 {
		t.Fatalf("fragments = %d, want 2", len(rep.Fragments))
	}
	if rep.Fragments[0].Page != 1 || rep.Fragments[0].Ord != 0 {
		t.Errorf("frag[0] page/ord = %d/%d, want 1/0", rep.Fragments[0].Page, rep.Fragments[0].Ord)
	}
	if rep.Fragments[0].Text != psaText {
		t.Errorf("frag[0] text = %q, want psaText", rep.Fragments[0].Text[:40])
	}
	if rep.Fragments[1].Page != 2 || rep.Fragments[1].Ord != 0 {
		t.Errorf("frag[1] page/ord = %d/%d, want 2/0", rep.Fragments[1].Page, rep.Fragments[1].Ord)
	}
	if rep.Fragments[1].Text != "Signature page." {
		t.Errorf("frag[1] text = %q, want %q", rep.Fragments[1].Text, "Signature page.")
	}

	// Identity caption and summary are surfaced.
	if rep.Identity.Name == "" || rep.Identity.Summary == "" {
		t.Errorf("identity not surfaced: %+v", rep.Identity)
	}
	if rep.Identity.Name != machineIdentity().Name {
		t.Errorf("identity name = %q, want %q", rep.Identity.Name, machineIdentity().Name)
	}

	// The origin='identity' fragment is identified.
	if rep.IdentityFragment == nil {
		t.Fatal("identity fragment not identified")
	}
	if rep.IdentityFragment.Origin != fragOriginIdentity {
		t.Errorf("identity fragment origin = %q, want %q", rep.IdentityFragment.Origin, fragOriginIdentity)
	}
	// It is page 0, ord 0 (the convention for identity fragments).
	if rep.IdentityFragment.Page != 0 || rep.IdentityFragment.Ord != 0 {
		t.Errorf("identity fragment page/ord = %d/%d, want 0/0", rep.IdentityFragment.Page, rep.IdentityFragment.Ord)
	}
	// Identity fragments are deliberately not embedded.
	if rep.IdentityEmbedded {
		t.Error("identity fragment marked as embedded; it should not be")
	}

	// Doc identity is the path.
	if rep.DocIdentity != path {
		t.Errorf("doc identity = %q, want %q", rep.DocIdentity, path)
	}
}

// TestAnalyze_MissingDocumentErrors proves that Analyze returns an error when
// the document is not in the index.
func TestAnalyze_MissingDocumentErrors(t *testing.T) {
	s := storeWithDoc(t)
	_, err := Analyze(context.Background(), s, "/corpus/nope.pdf")
	if err == nil {
		t.Fatal("want an error for a missing document, got nil")
	}
}
