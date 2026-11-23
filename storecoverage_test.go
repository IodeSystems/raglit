package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/iodesystems/raglit/attest"
)

func TestCoverageFor_FiveSegments(t *testing.T) {
	s := storeWithDocs(t, 4)
	ctx := context.Background()
	p := func(i int) string { return fmt.Sprintf("/corpus/scan_%03d.pdf", i) }

	// doc 0: captioned + tagged
	if _, err := s.RecordIdentity(ctx, p(0), DocIdentity{
		Name: "A captioned doc", Summary: "A summary.",
		ContentTags: []string{"purchase agreement"}, RoleTags: []string{"reference"},
	}, "test"); err != nil {
		t.Fatal(err)
	}

	// doc 1: typed + extracted (SetDocumentFields sets gen_doc_type)
	if err := s.SetDocumentFields(ctx, p(1), DocFields{
		Type: "agreement", Fields: json.RawMessage(`{"party":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}

	// doc 2: attested
	if err := s.PutAttestations(p(2), []attest.Entry{{Kind: attest.Affirmed, Unit: "page 1"}}); err != nil {
		t.Fatal(err)
	}

	// doc 3: bare — no identity, no fields, no attestation

	c, err := s.CoverageFor("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 4 {
		t.Fatalf("Total = %d, want 4", c.Total)
	}
	if c.Captioned != 1 {
		t.Fatalf("Captioned = %d, want 1", c.Captioned)
	}
	if c.Tagged != 1 {
		t.Fatalf("Tagged = %d, want 1", c.Tagged)
	}
	if c.Typed != 1 {
		t.Fatalf("Typed = %d, want 1", c.Typed)
	}
	if c.Extracted != 1 {
		t.Fatalf("Extracted = %d, want 1", c.Extracted)
	}
	if c.Attested != 1 {
		t.Fatalf("Attested = %d, want 1", c.Attested)
	}

	// Path prefix: all four docs live under /corpus/.
	c, err = s.CoverageFor("/corpus/")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 4 {
		t.Fatalf("prefix /corpus/ Total = %d, want 4", c.Total)
	}
	if c.Captioned != 1 {
		t.Fatalf("prefix /corpus/ Captioned = %d, want 1", c.Captioned)
	}

	// A prefix matching nothing.
	c, err = s.CoverageFor("/nomatch/")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 0 {
		t.Fatalf("prefix /nomatch/ Total = %d, want 0", c.Total)
	}
}

func TestCoverageFor_EmptyIndex(t *testing.T) {
	s := storeWithDocs(t, 0)
	c, err := s.CoverageFor("")
	if err != nil {
		t.Fatal(err)
	}
	want := CorpusCoverage{Total: 0}
	if c != want {
		t.Fatalf("CoverageFor(\"\") = %+v, want zero value %+v", c, want)
	}
}

func TestCoverageFor_BranchOverlay(t *testing.T) {
	fx := newWorktreeFixture(t)
	s := fx.open(t, "branch", fx.wtRoot)
	ctx := context.Background()

	// The parent has 3 docs (a.md, src/b.md, src/c.md), all bare.
	// Change a.md in the worktree and ingest it so the branch has a local row,
	// then set identity on it.
	writeFiles(t, fx.wtRoot, map[string]string{"a.md": "alpha token revised text"})
	drainPaths(t, s, fx.wt("a.md"))
	if _, err := s.RecordIdentity(ctx, fx.wt("a.md"), DocIdentity{
		Name: "Alpha", Summary: "A document in the branch.",
		ContentTags: []string{"alpha"},
	}, "test"); err != nil {
		t.Fatal(err)
	}

	c, err := s.CoverageFor("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 3 {
		t.Fatalf("Total = %d, want 3 (branch overlay sees all parent docs)", c.Total)
	}
	if c.Captioned != 1 {
		t.Fatalf("Captioned = %d, want 1 (only a.md, set in the branch)", c.Captioned)
	}
	if c.Tagged != 1 {
		t.Fatalf("Tagged = %d, want 1", c.Tagged)
	}
	if c.Typed != 0 || c.Extracted != 0 || c.Attested != 0 {
		t.Fatalf("Typed/Extracted/Attested = %d/%d/%d, want 0/0/0", c.Typed, c.Extracted, c.Attested)
	}
}
