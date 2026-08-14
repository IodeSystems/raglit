package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/raglit/attest"
)

// A response oidio actually produces: verbose_json with segments[] and
// speakers[]. Shaped from internal/server/diarize.go rather than invented.
const oidioBody = `{
  "task": "transcribe", "language": "en", "duration": 92.5,
  "text": "I went over there. That is not what happened.",
  "segments": [
    {"id":0,"start":1.5,"end":12.25,"text":"I went over there.","speaker":"uuid-larry"},
    {"id":1,"start":12.25,"end":30.0,"text":"That is not what happened.","speaker":"uuid-becky","overlap":true},
    {"id":2,"start":30.0,"end":31.0,"text":"  ","speaker":"uuid-larry"}
  ],
  "speakers": [
    {"uuid":"uuid-becky","total_seconds":17.75,"clean_seconds":17.75},
    {"uuid":"uuid-larry","total_seconds":11.75,"clean_seconds":2.0,"blended":true}
  ]
}`

// got is what the server saw. Filled INSIDE the handler: a *http.Request
// captured for later reads is nil until the handler runs and its multipart form
// is cleaned up after, so the assertions have to be made against values lifted
// out at request time.
type got struct {
	Model, Format, Filename string
	Upload                  []byte
}

func sttServer(t *testing.T, status int, body string) (*STT, *got) {
	t.Helper()
	var g got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(8 << 20)
		g.Model, g.Format = r.FormValue("model"), r.FormValue("response_format")
		if f, hdr, err := r.FormFile("file"); err == nil {
			g.Filename = hdr.Filename
			g.Upload, _ = io.ReadAll(f)
			f.Close()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &STT{BaseURL: srv.URL + "/v1", Model: "stt-diarize", HTTP: srv.Client()}, &g
}

// raglit uploads the ORIGINAL bytes. oidio's DecodePCM handles any
// ffmpeg-readable container and spools to a seekable temp file so an mp4's
// trailing moov atom decodes; a normalise step on this side would duplicate that
// and add an ffmpeg dependency to a path that does not need one.
func TestSTT_UploadsOriginalBytesAndAsksForStructure(t *testing.T) {
	s, g := sttServer(t, 200, oidioBody)
	raw := []byte("\x00\x00\x00\x1cftypisom-original-container-bytes")
	if _, err := s.Transcribe(context.Background(), "/corpus/H22-139-hearing.mp4", raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g.Upload, raw) {
		t.Errorf("the container must be uploaded untouched, got %q", g.Upload)
	}
	if g.Model != "stt-diarize" {
		t.Errorf("model = %q, want stt-diarize", g.Model)
	}
	// Plain json would carry segments too, but verbose_json is the documented
	// OpenAI shape for "structure as well as text" and is what a reader expects.
	if g.Format != "verbose_json" {
		t.Errorf("response_format = %q, want verbose_json", g.Format)
	}
	// The filename rides along: oidio's decoder uses the extension as a hint, and
	// an upload with no extension is a container it has to guess at. The BASENAME
	// only — the corpus path is not the server's business.
	if g.Filename != "H22-139-hearing.mp4" {
		t.Errorf("filename = %q, want H22-139-hearing.mp4", g.Filename)
	}
}

// A 200 carrying no words is a failure that reports success — the same
// silent-empty-decode class oidio guards against on its own side. Indexing it
// would create a document whose text is the empty string.
func TestSTT_RefusesAnEmptyTranscription(t *testing.T) {
	s, _ := sttServer(t, 200, `{"task":"transcribe","text":"   ","segments":[],"speakers":[]}`)
	_, err := s.Transcribe(context.Background(), "silent.mp4", []byte("x"))
	if err == nil {
		t.Fatal("a transcription with no speech must be an error, not an empty document")
	}
	if !strings.Contains(err.Error(), "transcribed to nothing") {
		t.Errorf("the message should say what happened, got %v", err)
	}
}

// An unreachable endpoint is the most likely failure in a fresh setup, so it
// names the cause rather than surfacing a bare dial error.
func TestSTT_SaysWhenOidioIsNotRunning(t *testing.T) {
	s := &STT{BaseURL: "http://127.0.0.1:1/v1", Model: "stt-diarize"}
	_, err := s.Transcribe(context.Background(), "a.mp4", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "is oidio running?") {
		t.Errorf("want a message naming the likely cause, got %v", err)
	}
}

func TestSTT_ReportsAServerError(t *testing.T) {
	s, _ := sttServer(t, 500, "model stt-diarize is not loaded")
	_, err := s.Transcribe(context.Background(), "a.mp4", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Errorf("the server's own words must survive, got %v", err)
	}
}

// Labels are ordered by how much each voice SPOKE, so the same recording labels
// the same way every time and the most talkative voice — usually the one a
// reader is looking for — is speaker-1. UUIDs are stable and meaningless; a
// transcript reading "uuid-larry: I went over there" is not a transcript.
func TestSpeakerLabels_OrderedBySpeakingTimeNotMapOrder(t *testing.T) {
	var r sttResponse
	if err := json.Unmarshal([]byte(oidioBody), &r); err != nil {
		t.Fatal(err)
	}
	// Run repeatedly: a map-order dependency shows up as a flake, not a failure.
	for i := 0; i < 50; i++ {
		l := speakerLabels(r.Speakers)
		if l["uuid-becky"] != "speaker-1" || l["uuid-larry"] != "speaker-2" {
			t.Fatalf("labels are not ordered by speaking time: %v", l)
		}
	}
}

func TestTranscriptText_CarriesWhatASearchHitNeedsToBeTakenBack(t *testing.T) {
	var r sttResponse
	if err := json.Unmarshal([]byte(oidioBody), &r); err != nil {
		t.Fatal(err)
	}
	out := TranscriptText("/corpus/H22-139-hearing.mp4", &r)

	// A timestamp per turn is the whole reason indexing a hearing is worth doing:
	// a hit has to lead back to a position in the recording.
	if !strings.Contains(out, "[0:02] speaker-2:") {
		t.Errorf("want a timestamped, attributed turn:\n%s", out)
	}
	// Hours matter — a court recording runs long and "4512.3s" is not scrubbable.
	if got := hms(4512.3); got != "1:15:12" {
		t.Errorf("hms(4512.3) = %s, want 1:15:12", got)
	}
	// Says a machine wrote it. The same distinction identity.go draws for
	// generated captions: a reading is not the record until a person affirms it.
	if !strings.Contains(out, "not verified by a person") {
		t.Error("the transcript must not read as the record")
	}
	// A blended voiceprint describes more than one voice; a reader deciding
	// whether to trust an attribution should see that where they are reading.
	if !strings.Contains(out, "blended, attribution uncertain") {
		t.Error("blended attribution must be visible in the transcript")
	}
	if !strings.Contains(out, "*(crosstalk)*") {
		t.Error("an overlapping turn must be marked")
	}
	// A whitespace-only segment is not a turn.
	if strings.Contains(out, "speaker-2:**  \n") || strings.Count(out, "speaker-") < 2 {
		t.Errorf("empty segments must be dropped:\n%s", out)
	}
}

// The reading is what attest reviews. Speaker is a LABEL, not part of the text:
// a reviewer can rule the words right and the speaker wrong, and attest's own
// comment says conflating those is what made oidio's earlier workbench painful.
func TestSTTReading_IsReviewableByAttest(t *testing.T) {
	var r sttResponse
	if err := json.Unmarshal([]byte(oidioBody), &r); err != nil {
		t.Fatal(err)
	}
	rd, err := STTReading("/corpus/H22-139-hearing.mp4", "abc123", "oidio/stt-diarize", &r)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Asset.Kind != attest.KindAudio {
		t.Errorf("kind = %q, want audio", rd.Asset.Kind)
	}
	// The digest is over the recording's bytes: every guarantee a reading makes
	// is about pieces of THAT byte sequence, so a re-encode must not match.
	if rd.Asset.SHA256 != "abc123" {
		t.Errorf("sha256 = %q", rd.Asset.SHA256)
	}
	// The asset id is the document's path, so a reading and an indexed document
	// match without a second mapping.
	if rd.Asset.ID != "/corpus/H22-139-hearing.mp4" {
		t.Errorf("asset id = %q, want the document path", rd.Asset.ID)
	}
	if len(rd.Units) != 2 { // the whitespace-only segment is not a unit
		t.Fatalf("want 2 units, got %d", len(rd.Units))
	}
	u := rd.Units[0]
	if u.Locator.Time == nil || u.Locator.Time.Start != 1.5 || u.Locator.Time.End != 12.25 {
		t.Errorf("an audio unit must be located by time, got %+v", u.Locator)
	}
	if u.Text != "I went over there." || u.Label != "speaker-2" {
		t.Errorf("speaker belongs in Label, text in Text: %+v", u)
	}
	// Seal replaces the producer's t0/t1 handles with content ids, so two
	// readings of the same asset are comparable.
	if u.ID == "t0" || u.ID == "" {
		t.Errorf("the reading must be sealed, got id %q", u.ID)
	}
	if !rd.Units[1].HasFlag("crosstalk") {
		t.Error("an overlapping turn must carry the flag onto the unit")
	}
	if !u.HasFlag("blended-voiceprint") {
		t.Error("doubt about the voiceprint belongs on the unit being ruled on")
	}
}

// End to end through the real Worker: a recording in a corpus becomes a
// searchable transcript, with the reading kept for review and the corpus left
// untouched. The stub stands in for oidio so the test needs no models.
func TestWorker_IngestsARecordingAsItsTranscript(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus")
	if err := os.MkdirAll(corpus, 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(corpus, "H22-139-hearing.mp4")
	if err := os.WriteFile(media, []byte("\x00\x00\x00\x1cftypisom\x00\x00\x02\x00isomiso2mp41"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	stt, _ := sttServer(t, 200, oidioBody)
	w := &Worker{Store: s, STT: stt}
	if _, err := s.Enqueue("file://"+media, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne returned infra error: %v", err)
	}

	st, _ := s.IndexStatus()
	if st.Failed != 0 || st.Documents != 1 {
		t.Fatalf("a recording should ingest cleanly: %+v", st)
	}
	// The document's identity is the RECORDING's path, not a derived file — a
	// hit has to point at the .mp4 the words were spoken in.
	hits, _ := s.Search("went over there", 5)
	if len(hits) == 0 {
		t.Fatal("the transcript is not searchable")
	}
	if !strings.HasSuffix(hits[0].Path, "H22-139-hearing.mp4") {
		t.Errorf("the document should be the recording, got %s", hits[0].Path)
	}
	// The reading is kept where attest can review it, in raglit's home.
	rd, ok, err := attest.ReadReading(filepath.Join(s.TranscriptDirFor(media), filepath.Base(media)))
	if err != nil || !ok {
		t.Fatalf("no reading was written for review: ok=%v err=%v", ok, err)
	}
	if rd.Producer != "oidio/stt-diarize" {
		t.Errorf("producer = %q — a transcript whose author is unrecorded cannot be told from another model's", rd.Producer)
	}
	if len(rd.Units) != 2 {
		t.Errorf("want 2 reviewable turns, got %d", len(rd.Units))
	}
	// Nothing was written into the corpus. That is the whole point of choosing
	// raglit's home over a sidecar.
	ents, _ := os.ReadDir(corpus)
	if len(ents) != 1 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("the corpus must be left alone, now holds: %v", names)
	}
}

// Without a transcriber an audio job fails with a message that says what to do,
// the same way a PDF job does without a vision model. It must not crash the
// worker and must not fall through to the text reader.
func TestWorker_AudioWithoutATranscriberFailsClearly(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "hearing.mp3")
	if err := os.WriteFile(media, append([]byte{'I', 'D', '3', 3, 0, 0}, make([]byte, 400)...), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenHome(Home(filepath.Join(dir, "home")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Enqueue("file://"+media, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Worker{Store: s}).ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne returned infra error: %v", err)
	}
	st, _ := s.IndexStatus()
	if st.Failed != 1 || st.Documents != 0 {
		t.Fatalf("audio with no transcriber must fail the job, not index bytes: %+v", st)
	}
}

// Where a transcript LIVES is raglit's business, not the corpus's — the
// conclusion the attachments migration reached for the same class of file.
func TestHome_TranscriptStaysOutOfTheCorpus(t *testing.T) {
	h := Home(t.TempDir())
	dir := h.TranscriptDir("/corpus/court/hearings/H22-139-hearing.mp4")
	if !strings.HasPrefix(dir, string(h)) {
		t.Errorf("a transcript must live in raglit's home, got %s", dir)
	}
	if strings.Contains(dir, "/corpus/") {
		t.Errorf("nothing may be written into the evidence tree, got %s", dir)
	}
	// Keyed by path like OriginalPath and PageDir, so nothing has to record it.
	if dir != h.TranscriptDir("/corpus/court/hearings/H22-139-hearing.mp4") {
		t.Error("the location must be deterministic")
	}
	if dir == h.TranscriptDir("/elsewhere/H22-139-hearing.mp4") {
		t.Error("same basename in two corpora must not collide")
	}
}

// corrallm re-exports oidio's backends under a prefix, so the same reader is
// "stt-diarize" direct and "oidio-stt-diarize" through the broker. Both must
// name the same producer: attest records one so two readings of an asset can be
// compared, and a name that changes with the route between them defeats that.
func TestSTTProducer_IsTheSameWhicheverRouteReachedIt(t *testing.T) {
	for _, model := range []string{"stt-diarize", "oidio-stt-diarize"} {
		if got := sttProducer(model); got != "oidio/stt-diarize" {
			t.Errorf("sttProducer(%q) = %q, want oidio/stt-diarize", model, got)
		}
	}
	if got := sttProducer("oidio-stt"); got != "oidio/stt" {
		t.Errorf("sttProducer(oidio-stt) = %q", got)
	}
}

// A 429 is "not now", not "no". Backpressure carries how long to wait, and
// discarding a 40-minute upload over a condition that names its own expiry
// throws away the expensive half of the work. Measured: four hearings queued
// together, corrallm answered two with
// {"capacity":2,"in_flight":2,"retry_after":9} and both failed outright.
func TestSTT_WaitsOutBackpressureInsteadOfFailing(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"capacity":2,"in_flight":2,"retry_after":0.01,` +
				`"type":"backpressure","message":"backend at capacity; retry after backoff"}}`))
			return
		}
		_, _ = w.Write([]byte(oidioBody))
	}))
	defer srv.Close()

	s := &STT{BaseURL: srv.URL + "/v1", Model: "oidio-stt-diarize", HTTP: srv.Client()}
	out, err := s.Transcribe(context.Background(), "hearing.mp4", []byte("x"))
	if err != nil {
		t.Fatalf("a retryable refusal must not fail the job: %v", err)
	}
	if attempts != 2 {
		t.Errorf("want one retry, got %d attempts", attempts)
	}
	if len(out.Segments) == 0 {
		t.Error("the retry must return the real transcription")
	}
}

// Attempts are bounded: a queue that is genuinely full should put the job back
// where it is visible rather than hold a worker slot indefinitely.
func TestSTT_GivesUpAfterBoundedAttempts(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"retry_after":0.01,"type":"backpressure"}}`))
	}))
	defer srv.Close()

	s := &STT{BaseURL: srv.URL + "/v1", Model: "m", HTTP: srv.Client()}
	if _, err := s.Transcribe(context.Background(), "a.mp4", []byte("x")); err == nil {
		t.Fatal("persistent backpressure must eventually fail the job")
	}
	if attempts != sttMaxAttempts {
		t.Errorf("attempts = %d, want %d", attempts, sttMaxAttempts)
	}
}

// The wait comes from the server, because it is the only thing that knows.
func TestRetryAfterOf_PrefersTheServersOwnNumber(t *testing.T) {
	body := []byte(`{"error":{"retry_after":9,"capacity":2}}`)
	if got := retryAfterOf(body, "3"); got != 9*time.Second {
		t.Errorf("body must win over the header, got %v", got)
	}
	if got := retryAfterOf([]byte(`{}`), "4"); got != 4*time.Second {
		t.Errorf("the header is the fallback, got %v", got)
	}
	// A refusal with no number still waits — 429 means "not now" either way.
	if got := retryAfterOf([]byte(`nonsense`), ""); got != sttDefaultBackoff {
		t.Errorf("a bare 429 must still back off, got %v", got)
	}
	// A hostile number cannot park a worker for an afternoon.
	if got := retryAfterOf([]byte(`{"error":{"retry_after":86400}}`), ""); got != sttMaxBackoff {
		t.Errorf("the wait must be capped, got %v", got)
	}
}
