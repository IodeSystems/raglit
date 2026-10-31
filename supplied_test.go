package raglit

import (
	"context"
	"strings"
	"testing"
)

// A caption supplied with a document (a session's fold summary) is recorded
// as "supplied" when the ingest commits, cut to the summary cap with no
// minimum, and owes nothing to the model: no identity job is queued even
// where a model is configured.
func TestSupplyIdentity_IsRecordedAndOwesNoModelCall(t *testing.T) {
	ctx := context.Background()
	st, err := OpenHome(Home(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetIdentifier(&Identifier{}) // a model is configured; it must not be asked

	ingest := func(name, text string, sup *DocIdentity) string {
		t.Helper()
		p, err := st.SaveUpload(name, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		url := UploadURL(p)
		if sup != nil {
			must(t, st.SupplyIdentity(url, *sup, "key#s1"))
		}
		if _, err := st.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		w := &Worker{Store: st}
		for range 5 {
			if did, err := w.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			} else if !did {
				break
			}
		}
		return url
	}
	long := strings.Repeat("a summary that runs on. ", 200)
	folded := ingest("sessions/fold-1.md", "walrus transcript of the folded turns",
		&DocIdentity{Name: "Fix the login redirect", Summary: long, Kind: "fold"})
	got, err := st.DocumentIdentity(folded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != IdentitySupplied || got.Model != "key#s1" || got.Name != "Fix the login redirect" {
		t.Fatalf("identity = %+v, want supplied by key#s1", got)
	}
	if n := len([]rune(got.Summary)); n != identityMaxSummaryChars {
		t.Fatalf("summary is %d chars, want cut to %d", n, identityMaxSummaryChars)
	}
	q, _ := st.IdentityQueue()
	if q.Pending != 0 {
		t.Fatalf("a supplied caption queued %d identity job(s)", q.Pending)
	}
	// The contrast: the same ingest with nothing supplied is owed a caption.
	ingest("notes/plain.md", "walrus plain note", nil)
	if q, _ = st.IdentityQueue(); q.Pending != 1 {
		t.Fatalf("an unsupplied document queued %d identity job(s), want 1", q.Pending)
	}
	if err := st.SupplyIdentity("x", DocIdentity{Summary: "no name"}, "k"); err == nil {
		t.Fatal("a supplied identity without a name was accepted")
	}
}
