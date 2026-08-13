package raglit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/raglit/attest"
)

// Reading a recording.
//
// raglit is the CONSUMER here and stays one. oidio transcribes and diarizes and
// emits a reading; raglit indexes the transcript and hands the reading to attest
// so a person can verify and correct it. That division is stated in
// cmd/raglit/attestaudio.go and this file is the other end of it — the ingest
// side of the same contract the review workbench already implements.
//
// It shells out to nothing. oidio is an OpenAI-compatible HTTP server
// (POST /v1/audio/transcriptions, dispatching on a `model` name), which is the
// shape raglit's config already speaks: a base URL, a token and a model id, the
// same as the vision and embed endpoints. A subprocess would have bought a
// second mechanism for no gain.
//
// Nothing here decodes audio. oidio's DecodePCM takes "any ffmpeg-readable
// container/codec (wav, mp3, webm, ogg, flac, m4a, …)" and normalises to 16 kHz
// mono itself — including spooling to a seekable temp file so an .mp4's trailing
// moov atom does not make ffmpeg exit 0 on an empty decode. Putting a second
// normalise step on this side would duplicate that and add an ffmpeg dependency
// to a path that does not otherwise need one, so raglit uploads the original
// bytes and lets the producer produce.

// audioExts route to KindAudio. Video containers are in the list on purpose: a
// hearing recording arrives as .mp4 and what raglit wants from it is the speech.
// oidio takes the whole container and reads the audio stream out of it, so the
// distinction between "an audio file" and "a file with audio in it" is not one
// this side has to make.
var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".m4a": true, ".flac": true, ".ogg": true,
	".opus": true, ".aac": true, ".wma": true,
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".m4v": true,
}

// TranscriptsDir holds transcripts of ingested recordings.
//
// In raglit's storage, beside originals/ and pages/, for the reason the
// attachments migration states: where a generated artifact's bytes live is
// raglit's business, not the corpus's. A transcript is more clearly raglit's own
// output than an extracted attachment ever was — nobody put it in the evidence
// tree and nothing outside raglit refers to it by path.
func (h Home) TranscriptsDir() string { return filepath.Join(string(h), "transcripts") }

// TranscriptDir is where one recording's transcript and reading live, keyed by
// the recording's path the same way OriginalPath and PageDir are keyed.
// Deterministic, so nothing has to record it.
func (h Home) TranscriptDir(mediaPath string) string {
	return filepath.Join(h.TranscriptsDir(), tag(mediaPath))
}

// STT turns a recording into a reading. nil on a Worker → an audio job fails
// with a clear message, the same way a PDF job does without a vision model.
type STT struct {
	// BaseURL is oidio's root (e.g. http://127.0.0.1:8077/v1). Separate from
	// Config.BaseURL because oidio is its own process on its own port; assuming a
	// gateway fronts both would make a working setup unreachable for no benefit.
	BaseURL string
	APIKey  string
	// Model is the oidio model id — "stt-diarize" for speaker-labelled segments,
	// "stt" for plain text. Recorded as the reading's Producer, because a corpus
	// outlives several models and a transcript whose author is unrecorded cannot
	// be told from one a different model produced.
	Model string
	// HTTP overrides the client (tests). nil → a client with Timeout.
	HTTP *http.Client
	// Timeout bounds one transcription. Diarization costs roughly a minute per
	// minute of audio, so this is measured in hours, not the seconds an HTTP
	// default assumes: the 20-minute hearing that prompted this would trip a
	// 30-second timeout every time and look like a broken endpoint.
	Timeout time.Duration
}

// DefaultSTTTimeout bounds one transcription when Timeout is unset. Four hours
// covers a full day's hearing at diarization's roughly-realtime cost with room
// for a loaded box; it is a runaway guard, not a service-level expectation.
const DefaultSTTTimeout = 4 * time.Hour

// sttSegment is one speaker turn as oidio reports it.
type sttSegment struct {
	ID      int     `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker"`
	// Overlap marks a turn oidio found crosstalk in. Carried through to a flag on
	// the unit rather than dropped: a reviewer listening to two people talking at
	// once should be told that is what they are hearing before they rule on it.
	Overlap bool `json:"overlap,omitempty"`
}

// sttSpeaker is one detected voice. The embedding is deliberately NOT carried
// into the reading — it is a 192-float voiceprint per speaker, it says nothing
// about what was said, and attest's units are what a reviewer rules on. Blended
// and the durations ARE carried, because they qualify how much to trust the
// attribution.
type sttSpeaker struct {
	UUID         string  `json:"uuid"`
	CleanSeconds float64 `json:"clean_seconds"`
	TotalSeconds float64 `json:"total_seconds"`
	Blended      bool    `json:"blended,omitempty"`
}

type sttResponse struct {
	Language string       `json:"language"`
	Duration float64      `json:"duration"`
	Text     string       `json:"text"`
	Segments []sttSegment `json:"segments"`
	Speakers []sttSpeaker `json:"speakers"`
}

// Transcribe uploads the recording and returns oidio's reading.
//
// name is the original filename; it rides along in the multipart part because
// oidio's decoder uses the extension as a hint and an upload called "file" with
// no extension is a container it has to guess at.
func (s *STT) Transcribe(ctx context.Context, name string, data []byte) (*sttResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(name))
	if err != nil {
		return nil, fmt.Errorf("raglit: stt: build request: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("raglit: stt: build request: %w", err)
	}
	for k, v := range map[string]string{
		"model": s.Model,
		// verbose_json is what carries segments[] and speakers[]. Plain json
		// carries them too, but verbose_json is the documented OpenAI shape for
		// "give me the structure as well as the text" and is what a reader of this
		// call will expect to see.
		"response_format": "verbose_json",
	} {
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("raglit: stt: build request: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("raglit: stt: build request: %w", err)
	}

	url := strings.TrimSuffix(s.BaseURL, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("raglit: stt: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}

	hc := s.HTTP
	if hc == nil {
		t := s.Timeout
		if t == 0 {
			t = DefaultSTTTimeout
		}
		hc = &http.Client{Timeout: t}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raglit: stt: %s is unreachable — is oidio running? %w", url, err)
	}
	defer resp.Body.Close()
	// Bounded: an error body from a proxy can be a whole HTML page, and a
	// transcription response for a long hearing is large but not unbounded.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("raglit: stt: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("raglit: stt: %s: %s", resp.Status, strings.TrimSpace(truncateForError(string(raw))))
	}
	var out sttResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("raglit: stt: response was not the expected JSON: %w", err)
	}
	// A 200 with no words in it is a failure that reports success — the same
	// class of silent-empty-decode oidio's own DecodePCM guards against on its
	// side. Indexing it would create a document whose text is the empty string.
	if strings.TrimSpace(out.Text) == "" && len(out.Segments) == 0 {
		return nil, fmt.Errorf("raglit: stt: %s transcribed to nothing — no speech found, or the container has no audio stream",
			filepath.Base(name))
	}
	return &out, nil
}

// ingestAudio transcribes a recording and indexes the TRANSCRIPT.
//
// What gets indexed is the transcript, and what gets kept beside it is the
// reading. The document's identity stays the RECORDING's path — a search hit
// points at the .mp4 the words were spoken in, not at a derived file somebody
// would then have to trace back. That is the same choice ingestPDF makes: the
// document is the pdf, not the text pulled out of it.
//
// The reading is written best-effort. A transcript that was produced and
// indexed must not be discarded because a sidecar could not be written — the
// same reasoning the attachment and transcription writebacks use — but the
// failure is recorded as a stage so it is visible rather than assumed.
func (w *Worker) ingestAudio(ctx context.Context, job *Job, f Fetched, title string, sl *StageLog) (int, string, error) {
	res, err := w.STT.Transcribe(ctx, job.URL, f.Data)
	if err != nil {
		sl.Fail("extract", "stt", err)
		return 0, "", err
	}
	sl.Done("extract", "stt", fmt.Sprintf("%s, %d turn(s), %d speaker(s)",
		hms(res.Duration), len(res.Segments), len(res.Speakers)))

	producer := sttProducer(w.STT.Model)
	// Keyed by the CORPUS path, not the job URL. The two differ by a "file://"
	// prefix, and tag() hashes whatever string it is handed — so keying on the
	// URL would put the reading somewhere no reader looking it up by path would
	// find it, and a http:// ingest of the same recording would land somewhere
	// else again. archiveCorpusPath is what the attachment path already uses to
	// make that reduction; empty means the recording has no place on this disk.
	corpus := archiveCorpusPath(job.URL)
	if corpus == "" {
		corpus = job.URL
	}
	// The digest is over the RECORDING's bytes, which is what attest's Asset
	// means by one: every guarantee a reading makes is about pieces of that byte
	// sequence, so a re-encode is a different asset and must not silently match.
	if rd, rerr := STTReading(corpus, sha256hex(f.Data), producer, res); rerr != nil {
		sl.Fail("reading", "attest", rerr)
	} else if dir := w.Store.TranscriptDirFor(corpus); dir == "" {
		sl.Skip("reading", "this index has no home to store the reading in")
	} else if path, werr := writeReadingTo(dir, corpus, rd); werr != nil {
		sl.Fail("reading", "attest", werr)
	} else {
		sl.Done("reading", "attest", fmt.Sprintf("%d unit(s) → %s", len(rd.Units), filepath.Base(path)))
	}

	return w.ingestPlainText(ctx, job.URL, title, []byte(TranscriptText(job.URL, res)), sl)
}

// writeReadingTo puts the reading in raglit's storage rather than beside the
// recording.
//
// attest.WriteReading appends its suffix to the path it is given, so handing it
// <transcript-dir>/<basename> lands <basename>.reading.json inside raglit's
// home. The suffix stays attest's to choose — the review workbench finds a
// reading by that name and nothing here should second-guess it.
func writeReadingTo(dir, mediaPath string, rd *attest.Reading) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("raglit: create %s: %w", dir, err)
	}
	return attest.WriteReading(filepath.Join(dir, filepath.Base(mediaPath)), rd)
}

// sttProducer names what read the recording, from the model id.
//
// The same oidio model has two ids depending on how it is reached: "stt-diarize"
// direct, "oidio-stt-diarize" through corrallm, which re-exports its backends
// under a prefix. Left alone the second spells "oidio/oidio-stt-diarize", and
// worse, the two routes produce DIFFERENT producer strings for the same reader.
// attest's whole reason for recording a producer is that two readings of one
// asset are meant to be comparable; a name that changes with the network path
// between them defeats that.
func sttProducer(model string) string {
	return "oidio/" + strings.TrimPrefix(model, "oidio-")
}

func truncateForError(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// speakerLabels names each voice for a human reader.
//
// oidio returns UUIDs, which are stable and meaningless. A transcript that says
// "3f2a1b… : I went over there" is unreadable, so the UUIDs are mapped to
// speaker-1..N ordered by how much each ACTUALLY SPOKE — the most talkative
// voice in a hearing is usually the one a reader is looking for, and a stable
// ordering means the same recording labels the same way every time.
//
// Deliberately not an attempt to NAME anybody. Who speaker-1 is is a question
// for the person reviewing, and attest's whole purpose is to record that ruling
// rather than have a machine guess it.
func speakerLabels(sp []sttSpeaker) map[string]string {
	ordered := append([]sttSpeaker(nil), sp...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].TotalSeconds != ordered[j].TotalSeconds {
			return ordered[i].TotalSeconds > ordered[j].TotalSeconds
		}
		return ordered[i].UUID < ordered[j].UUID // ties broken stably, not by map order
	})
	out := make(map[string]string, len(ordered))
	for i, s := range ordered {
		out[s.UUID] = fmt.Sprintf("speaker-%d", i+1)
	}
	return out
}

// TranscriptText renders the transcript raglit INDEXES.
//
// Written for a reader and for retrieval, not as a data format — the reading
// sidecar is the data format. Speaker and timestamp lead each turn so a search
// hit can be taken back to the recording, which is the whole reason a
// transcript of a hearing is worth indexing at all.
func TranscriptText(name string, r *sttResponse) string {
	var b strings.Builder
	labels := speakerLabels(r.Speakers)
	fmt.Fprintf(&b, "# Transcript — %s\n\n", filepath.Base(name))
	if r.Duration > 0 {
		fmt.Fprintf(&b, "**Duration:** %s  \n", hms(r.Duration))
	}
	if len(r.Speakers) > 0 {
		parts := make([]string, 0, len(r.Speakers))
		for _, s := range r.Speakers {
			p := fmt.Sprintf("%s (%s)", labels[s.UUID], hms(s.TotalSeconds))
			// A blended voiceprint describes more than one voice. Said here rather
			// than only in the sidecar: a reader deciding whether to trust "speaker-2"
			// should see it in the document they are reading.
			if s.Blended {
				p += " — blended, attribution uncertain"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "**Speakers:** %s  \n", strings.Join(parts, " · "))
	}
	// Says plainly that a machine wrote this and nobody has checked it. The same
	// distinction identity.go draws for generated captions: a reading is not the
	// record until a person has affirmed it.
	b.WriteString("\n*Machine transcript — not verified by a person.*\n\n")

	if len(r.Segments) == 0 {
		b.WriteString(strings.TrimSpace(r.Text) + "\n")
		return b.String()
	}
	for _, sg := range r.Segments {
		text := strings.TrimSpace(sg.Text)
		if text == "" {
			continue
		}
		who := labels[sg.Speaker]
		if who == "" {
			// oidio leaves a fragment-length cluster unattributed rather than
			// inventing a speaker for it. Carried through as the same absence.
			who = "unattributed"
		}
		fmt.Fprintf(&b, "**[%s] %s:** %s", hms(sg.Start), who, text)
		if sg.Overlap {
			b.WriteString("  *(crosstalk)*")
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// hms formats seconds as h:mm:ss, or m:ss under an hour. A court recording runs
// hours and "4512.3s" is not a timestamp anybody can scrub to.
func hms(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec + 0.5)
	h, m, s := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// STTReading converts oidio's response into the reading attest reviews.
//
// One unit per speaker turn, located by TIME — which is the locator attest
// already defines for audio and the reason Locator has a Time arm at all. The
// speaker becomes the unit's Label rather than part of its Text, because they
// are separately correctable: a reviewer can rule that the words are right and
// the speaker is wrong, and attest's own comment says conflating those two is
// what made oidio's earlier workbench painful.
// Sealed before it is returned, so the units carry content ids rather than the
// "t0, t1, …" handles used while building. Seal treats those as the producer's
// own handles and rewrites them, which is exactly what they are.
func STTReading(mediaPath, sha256, producer string, r *sttResponse) (*attest.Reading, error) {
	labels := speakerLabels(r.Speakers)
	units := make([]attest.Unit, 0, len(r.Segments))
	for i, sg := range r.Segments {
		text := strings.TrimSpace(sg.Text)
		if text == "" {
			continue
		}
		u := attest.Unit{
			ID:      fmt.Sprintf("t%d", i),
			Locator: attest.Locator{Time: &attest.TimeSpan{Start: sg.Start, End: sg.End}},
			Text:    text,
			Label:   labels[sg.Speaker],
		}
		if sg.Overlap {
			u.Flags = append(u.Flags, "crosstalk")
		}
		// A turn whose voiceprint was blended carries the doubt onto the unit, so
		// the reviewer sees it on the thing they are ruling on rather than having
		// to cross-reference a speaker table.
		for _, sp := range r.Speakers {
			if sp.UUID == sg.Speaker && sp.Blended {
				u.Flags = append(u.Flags, "blended-voiceprint")
			}
		}
		units = append(units, u)
	}
	rd := &attest.Reading{
		// ID is the handle a consumer refers to the asset by, and for raglit that
		// is the document's path — the same string the index keys on, so a reading
		// and an indexed document can be matched without a second mapping.
		Asset:    attest.Asset{ID: mediaPath, Name: filepath.Base(mediaPath), Kind: attest.KindAudio, SHA256: sha256},
		Units:    units,
		Producer: producer,
	}
	if err := rd.Seal(); err != nil {
		return nil, fmt.Errorf("raglit: stt: sealing reading: %w", err)
	}
	return rd, nil
}
