package attest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

// account is a host identity: a fixed principal and a permission set, standing
// in for whatever a real host resolves out of a session.
type account struct {
	principal string
	perms     map[Permission]bool
}

func (a account) Principal(context.Context) (string, error) { return a.principal, nil }

func (a account) Can(_ context.Context, _ string, p Permission) (bool, error) {
	return a.perms[p], nil
}

func full(p ...Permission) map[Permission]bool {
	m := map[Permission]bool{}
	for _, x := range p {
		m[x] = true
	}
	return m
}

// pngEvidence renders a fixed body whose digest matches what the fixture
// recorded, so the verified/unverified paths can both be exercised.
type pngEvidence struct {
	body   []byte
	humane []byte
}

func (e pngEvidence) Render(context.Context, Asset, Unit) (Artifact, error) {
	return Artifact{MIME: "image/png", Body: e.body, Digest: SHA256Hex(e.body)}, nil
}

func (e pngEvidence) Humane(context.Context, Asset, Unit) (Artifact, error) {
	return Artifact{MIME: "image/png", Body: e.humane, Digest: SHA256Hex(e.humane)}, nil
}

// cropOnly implements Evidence and nothing else, for asserting that an
// unsupported rendering is refused rather than silently substituted.
type cropOnly struct{ body []byte }

func (c cropOnly) Render(context.Context, Asset, Unit) (Artifact, error) {
	return Artifact{MIME: "image/png", Body: c.body, Digest: SHA256Hex(c.body)}, nil
}

type fixture struct {
	t    *testing.T
	root string
	srv  *httptest.Server
	svc  *Service
	ids  []string
}

func newFixture(t *testing.T, id Identity, ev Evidence) *fixture {
	t.Helper()
	root := t.TempDir()
	asset := filepath.Join(root, "hearing.wav")
	if err := os.WriteFile(asset, []byte("not really audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Reading{
		Asset:    Asset{ID: "hearing.wav", Kind: KindAudio},
		Producer: "oidio/parakeet",
		Units:    []Unit{turn(0, 1, "A", "one"), turn(1, 2, "B", "two"), turn(2, 3, "B", "three")},
	}
	if _, err := WriteReading(asset, r); err != nil {
		t.Fatal(err)
	}

	svc := &Service{Root: root, Ident: id, Ev: ev, Now: func() string { return "2026-07-29T10:00:00Z" }}
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("attest-test", "0"))
	if err := svc.Register(api, "/api"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	f := &fixture{t: t, root: root, srv: srv, svc: svc}
	for _, u := range r.Units {
		f.ids = append(f.ids, u.ID)
	}
	return f
}

func (f *fixture) post(path string, body any) (int, map[string]any) {
	f.t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(f.srv.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (f *fixture) get(path string) (int, map[string]any) {
	f.t.Helper()
	res, err := http.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestVerdictRecordsBothNames(t *testing.T) {
	f := newFixture(t, account{principal: "acct:jdoe", perms: full(PermRead, PermAttest)}, nil)

	code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed",
		"by": "R. Alvarez (paralegal)",
	})
	if code != 200 {
		t.Fatalf("verdict: %d", code)
	}

	log, err := ReadLog(filepath.Join(f.root, "hearing.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatalf("log has %d entries", len(log))
	}
	// The delegation case: the attorney's account, the paralegal's hands.
	if log[0].By != "R. Alvarez (paralegal)" {
		t.Errorf("author = %q; the person who did the work must survive", log[0].By)
	}
	if log[0].Auth != "acct:jdoe" {
		t.Errorf("principal = %q; the account it was authorized under must survive", log[0].Auth)
	}
	if log[0].At != "2026-07-29T10:00:00Z" {
		t.Errorf("timestamp = %q", log[0].At)
	}
}

// A caller must not be able to set the account. Signing a verdict with someone
// else's authorization is the one forgery this record is supposed to exclude.
//
// Two layers, tested separately because either alone would be brittle: the
// schema has no such property, and the write path overwrites it regardless.
func TestPrincipalCannotBeSetByTheCaller(t *testing.T) {
	f := newFixture(t, account{principal: "acct:jdoe", perms: full(PermRead, PermAttest)}, nil)

	code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed",
		"by": "carl", "auth": "acct:someone-else",
	})
	if code != 422 {
		t.Errorf("a request naming its own account was not rejected: %d", code)
	}
	if log, _ := ReadLog(filepath.Join(f.root, "hearing.wav")); len(log) != 0 {
		t.Fatalf("the rejected request still wrote %d entries", len(log))
	}

	// And if the schema ever loosened, the write path still resolves the
	// principal from the host and ignores whatever arrived.
	forged := Entry{Kind: Confirmed, Unit: f.ids[0], By: "someone", Auth: "acct:someone-else"}
	if err := f.svc.write(t.Context(), filepath.Join(f.root, "hearing.wav"), "carl", PermAttest, forged); err != nil {
		t.Fatal(err)
	}
	log, _ := ReadLog(filepath.Join(f.root, "hearing.wav"))
	if len(log) != 1 || log[0].Auth != "acct:jdoe" || log[0].By != "carl" {
		t.Fatalf("write path did not overwrite the caller's names: %+v", log)
	}
}

func TestAuthorIsRequiredOverTheAPI(t *testing.T) {
	f := newFixture(t, account{principal: "acct:jdoe", perms: full(PermRead, PermAttest)}, nil)
	for _, by := range []string{"", "   "} {
		code, _ := f.post("/api/verdict", map[string]any{
			"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed", "by": by,
		})
		if code == 200 {
			t.Errorf("accepted an unsigned verdict (by=%q)", by)
		}
	}
}

// The CLI's stance: everything permitted, and still no anonymous rulings.
func TestGuestMayDoAnythingButMustSayWhoItIs(t *testing.T) {
	f := newFixture(t, Guest{}, nil)

	if code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed", "by": "carl",
	}); code != 200 {
		t.Fatalf("guest verdict: %d", code)
	}
	if code, _ := f.post("/api/resegment", map[string]any{
		"asset": "hearing.wav", "supersedes": []string{f.ids[1]},
		"units": []UnitDraft{draft(1, 1.5, "B", "two"), draft(1.5, 2, "B", "and a half")},
		"by":    "carl",
	}); code != 200 {
		t.Fatalf("guest resegment: %d", code)
	}
	if code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed",
	}); code == 200 {
		t.Fatal("guest recorded an anonymous ruling")
	}

	log, _ := ReadLog(filepath.Join(f.root, "hearing.wav"))
	if log[0].Auth != "guest" {
		t.Errorf("auth = %q; a guest ruling must say plainly that it rests on access alone", log[0].Auth)
	}
}

func TestPermissionsAreEnforcedPerOperation(t *testing.T) {
	// The public-serving case: read the unattested transcript, rule on nothing.
	f := newFixture(t, ReadOnly{account{principal: "public", perms: full(PermRead)}}, nil)

	if code, _ := f.get("/api/state?asset=hearing.wav"); code != 200 {
		t.Fatalf("a read-only caller must still be able to read: %d", code)
	}
	if code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "confirmed", "by": "carl",
	}); code != 403 {
		t.Errorf("read-only caller recorded a verdict: %d", code)
	}
	if code, _ := f.post("/api/sweep", map[string]any{"asset": "hearing.wav", "by": "carl"}); code != 403 {
		t.Errorf("read-only caller swept: %d", code)
	}

	// Ruling on claims does not carry the right to re-cut them.
	g := newFixture(t, account{principal: "acct:x", perms: full(PermRead, PermAttest)}, nil)
	if code, _ := g.post("/api/resegment", map[string]any{
		"asset": "hearing.wav", "supersedes": []string{g.ids[0]}, "by": "carl",
	}); code != 403 {
		t.Errorf("attest permission was enough to re-cut the units: %d", code)
	}
}

// An unauthorized reader must not learn that the asset exists.
func TestUnreadableAssetIsNotFound(t *testing.T) {
	f := newFixture(t, account{principal: "nobody", perms: map[Permission]bool{}}, nil)
	if code, _ := f.get("/api/state?asset=hearing.wav"); code != 404 {
		t.Errorf("existence disclosed to an unauthorized reader: %d", code)
	}
	code, body := f.get("/api/assets")
	if code != 200 {
		t.Fatalf("assets: %d", code)
	}
	if assets, _ := body["assets"].([]any); len(assets) != 0 {
		t.Errorf("listing disclosed %d assets to a caller who may read none", len(assets))
	}
}

func TestRootIsABoundary(t *testing.T) {
	f := newFixture(t, Guest{}, nil)
	// A sibling of the root, reachable only by climbing out of it.
	outside := filepath.Join(filepath.Dir(f.root), "elsewhere.wav")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err == nil {
		defer os.Remove(outside)
	}
	for _, a := range []string{
		"../elsewhere.wav",
		"../../etc/passwd",
		"sub/../../elsewhere.wav",
		"/etc/passwd",
	} {
		if code, _ := f.get("/api/state?asset=" + a); code == 200 {
			t.Errorf("reached outside the root with %q", a)
		}
	}
	// The sidecars are not assets.
	for _, a := range []string{"hearing.wav.reading.json", "hearing.wav.attest.jsonl"} {
		if code, _ := f.get("/api/state?asset=" + a); code == 200 {
			t.Errorf("served attest's own sidecar as an asset: %q", a)
		}
	}
}

func TestSweepAndUndo(t *testing.T) {
	f := newFixture(t, Guest{}, nil)
	asset := filepath.Join(f.root, "hearing.wav")

	if code, _ := f.post("/api/verdict", map[string]any{
		"asset": "hearing.wav", "unit": f.ids[0], "kind": "unclear", "by": "carl", "note": "crosstalk",
	}); code != 200 {
		t.Fatal("verdict")
	}
	if code, _ := f.post("/api/sweep", map[string]any{"asset": "hearing.wav", "by": "carl"}); code != 200 {
		t.Fatal("sweep")
	}
	st, err := Load(asset)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stats.Complete() || st.Stats.Affirmed != 2 || st.Stats.Unclear != 1 {
		t.Fatalf("after sweep: %+v", st.Stats)
	}
	if st.Stats.Confirmed != 0 {
		t.Error("a sweep reported as individually confirmed")
	}

	// Withdrawing the sweep must leave the individually-judged unit alone.
	if code, _ := f.post("/api/sweep", map[string]any{
		"asset": "hearing.wav", "by": "carl", "undo": true,
	}); code != 200 {
		t.Fatal("undo")
	}
	st, err = Load(asset)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stats.Affirmed != 0 || st.Stats.Untouched != 2 {
		t.Fatalf("after undo: %+v", st.Stats)
	}
	if st.Stats.Unclear != 1 {
		t.Error("withdrawing a sweep destroyed a ruling somebody actually made")
	}
	if st.Stats.SweptBy != "" {
		t.Error("withdrawn sweep still claims provenance")
	}
}

func TestEvidenceLabelsWhatItServed(t *testing.T) {
	crop := []byte("the exact crop")
	humane := []byte("levelled for a person")

	// The fixture's units digest their evidence as "pcm-<label>", which is not
	// the crop's digest — so this stands in for a producer whose renderer has
	// drifted from what it recorded at read time.
	f := newFixture(t, Guest{}, pngEvidence{body: crop, humane: humane})
	res, err := http.Get(f.srv.URL + "/api/evidence?asset=hearing.wav&unit=" + f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("evidence: %d", res.StatusCode)
	}
	if got := res.Header.Get("X-Attest-Rendering"); got != "crop" {
		t.Errorf("rendering header = %q, want crop (the default is the attestation image)", got)
	}
	if got := res.Header.Get("X-Attest-Verified"); got != "no" {
		t.Errorf("verified = %q; a rendering that does not match the recorded digest must say so", got)
	}
	if got := res.Header.Get("X-Attest-Digest"); got != SHA256Hex(crop) {
		t.Errorf("digest header = %q", got)
	}

	// The humane rendering can never verify: it is not the artifact.
	res2, err := http.Get(f.srv.URL + "/api/evidence?asset=hearing.wav&as=humane&unit=" + f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if got := res2.Header.Get("X-Attest-Rendering"); got != "humane" {
		t.Errorf("rendering header = %q, want humane", got)
	}
	if got := res2.Header.Get("X-Attest-Verified"); got != "no" {
		t.Errorf("a legibility-first rendering reported itself as the artifact: %q", got)
	}
}

// Asking for a rendering a producer does not offer must fail, not quietly
// hand back the crop under the wrong label.
func TestUnsupportedRenderingIsRefusedNotSubstituted(t *testing.T) {
	f := newFixture(t, Guest{}, cropOnly{body: []byte("crop")})
	for _, as := range []string{"humane", "seen"} {
		code, _ := f.get("/api/evidence?asset=hearing.wav&as=" + as + "&unit=" + f.ids[0])
		if code == 200 {
			t.Errorf("%s was silently substituted with the crop", as)
		}
	}
}

func TestEvidenceWithoutAProviderSaysSo(t *testing.T) {
	f := newFixture(t, Guest{}, nil)
	if code, _ := f.get("/api/evidence?asset=hearing.wav&unit=" + f.ids[0]); code != 501 {
		t.Errorf("a mount with no renderer should say so plainly: %d", code)
	}
}

func TestStateCarriesProvenance(t *testing.T) {
	f := newFixture(t, Guest{}, nil)
	code, body := f.get("/api/state?asset=hearing.wav")
	if code != 200 {
		t.Fatalf("state: %d", code)
	}
	p, _ := body["provenance"].(string)
	if !strings.Contains(p, "INCOMPLETE") {
		t.Errorf("an unreviewed reading must announce itself as unreviewed: %q", p)
	}
	if !strings.Contains(p, "WORDS are unverified") {
		t.Errorf("provenance dropped the wording axis: %q", p)
	}
}

func TestRegisterRefusesToGuessAnIdentity(t *testing.T) {
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("t", "0"))
	if err := (&Service{Root: t.TempDir()}).Register(api, "/api"); err == nil {
		t.Fatal("mounted with no Identity — permit-all is not a default attest may choose for a host")
	}
	if err := (&Service{Ident: Guest{}}).Register(api, "/api"); err == nil {
		t.Fatal("mounted with no Root")
	}
}

func draft(start, end float64, label, text string) UnitDraft {
	return UnitDraft{Locator: Locator{Time: &TimeSpan{Start: start, End: end}}, Label: label, Text: text}
}

// A re-cut unit is described by the person cutting it, and there are exactly two
// things they may not say about it. Both are refused by the SHAPE of the
// request, which is stronger than validating them away.
func TestResegmentRefusesIDAndEvidence(t *testing.T) {
	f := newFixture(t, Guest{}, nil)

	// The id is the digest OF the claim. A caller-chosen one files a unit under
	// an address describing something else, and every verdict keyed to that
	// address then rules on the wrong thing.
	if code, _ := f.post("/api/resegment", map[string]any{
		"asset": "hearing.wav", "supersedes": []string{f.ids[1]}, "by": "carl",
		"units": []map[string]any{{"locator": map[string]any{"time": map[string]any{"start": 1, "end": 2}},
			"text": "two", "id": "t0.00-forged00000000"}},
	}); code != 422 {
		t.Errorf("a caller-supplied unit id was not refused: %d", code)
	}

	// An evidence digest asserts "this is the artifact the text was read from".
	// Nothing read a unit a person just cut, so a caller offering one is
	// claiming a machine reading that never happened.
	if code, _ := f.post("/api/resegment", map[string]any{
		"asset": "hearing.wav", "supersedes": []string{f.ids[1]}, "by": "carl",
		"units": []map[string]any{{"locator": map[string]any{"time": map[string]any{"start": 1, "end": 2}},
			"text": "two", "evidence": "pcm-B"}},
	}); code != 422 {
		t.Errorf("a caller-supplied evidence digest was not refused: %d", code)
	}

	if log, _ := ReadLog(filepath.Join(f.root, "hearing.wav")); len(log) != 0 {
		t.Fatalf("a rejected resegment still wrote %d entries", len(log))
	}
}

func TestResegmentAssignsIDsAndLeavesNoArtifact(t *testing.T) {
	f := newFixture(t, Guest{}, nil)
	if code, _ := f.post("/api/resegment", map[string]any{
		"asset": "hearing.wav", "supersedes": []string{f.ids[1]}, "by": "carl",
		"units": []UnitDraft{draft(1, 1.5, "B", "two"), draft(1.5, 2, "B", "and a half")},
	}); code != 200 {
		t.Fatal("resegment")
	}
	st, err := Load(filepath.Join(f.root, "hearing.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Stats.Authored != 2 || st.Stats.Total != 4 {
		t.Fatalf("stats = %+v", st.Stats)
	}
	for _, u := range st.Units {
		if !u.Authored {
			continue
		}
		if u.Unit.ID == "" {
			t.Error("an authored unit got no content address")
		}
		if u.Unit.Evidence != "" {
			t.Errorf("a unit a person cut carries an evidence digest %q — nothing read it", u.Unit.Evidence)
		}
		if u.Kind != "" {
			t.Error("cutting in the right place is not the same as ruling on who spoke")
		}
	}
}
