package raglit

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// b64 is what a mail client writes for an attachment payload.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// archiveWithAttachments builds a two-message archive: message 1 carries a
// survey, message 2 carries the same survey again plus a plat.
func archiveWithAttachments() string {
	return "From: Larry <larry@example.com>\r\nDate: Sat, 15 May 2021 21:07:00 -0700\r\n" +
		"Subject: Survey\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nSurvey attached.\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"survey 2019.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"survey 2019.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + b64("%PDF-1.4 THE SURVEY") + "\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: Marta <marta@example.com>\r\nDate: Sun, 16 May 2021 09:00:00 -0700\r\n" +
		"Subject: Re: Survey\r\nContent-Type: multipart/mixed; boundary=C\r\n\r\n" +
		"--C\r\nContent-Type: text/plain\r\n\r\nAnd the plat.\r\n" +
		"--C\r\nContent-Type: application/pdf; name=\"survey 2019.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"survey 2019.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + b64("%PDF-1.4 THE SURVEY") + "\r\n" +
		"--C\r\nContent-Type: application/pdf; name=\"plat.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"plat.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + b64("%PDF-1.4 THE PLAT") + "\r\n" +
		"--C--\r\n" +
		"--B--\r\n"
}

func writeArchive(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brokerlog.eml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// The whole point: an attachment named in a transcription and nowhere on disk
// is evidence you can see and cannot read. Extraction makes the file an
// ordinary file, which is what makes the next sync index it with no queue
// plumbing at all.
func TestAttachmentsAreExtractedBesideTheArchive(t *testing.T) {
	arc := writeArchive(t, archiveWithAttachments())
	n, dir, err := ExtractEmailAttachments(arc, arc)
	if err != nil {
		t.Fatal(err)
	}
	if dir != arc+".raglit-attachments" {
		t.Errorf("sidecar at %s, want beside the archive and named after it", dir)
	}
	if n != 2 {
		t.Errorf("wrote %d file(s), want 2 (the survey once, the plat once)", n)
	}
	names := dirNames(t, dir)
	if len(names) != 3 { // two files + MANIFEST.md
		t.Fatalf("directory holds %v, want two attachments and a manifest", names)
	}
	// The bytes are the DECODED part, copied — not the base64, not a conversion.
	got, err := os.ReadFile(filepath.Join(dir, "p01-01-survey-2019.pdf"))
	if err != nil {
		t.Fatalf("survey not extracted under the name that names its message: %v (have %v)", err, names)
	}
	if string(got) != "%PDF-1.4 THE SURVEY" {
		t.Errorf("extracted bytes are not the decoded part: %q", got)
	}
}

// The filename IS part of the chain: p07-02-x.pdf is the second attachment of
// message 7, and message 7 is `## Page 7` of the archive's transcription. A
// name that does not say which message it came from is a second original.
func TestExtractedNameCarriesItsMessage(t *testing.T) {
	arc := writeArchive(t, archiveWithAttachments())
	_, dir, err := ExtractEmailAttachments(arc, arc)
	if err != nil {
		t.Fatal(err)
	}
	names := dirNames(t, dir)
	var plat string
	for _, n := range names {
		if strings.Contains(n, "plat") {
			plat = n
		}
	}
	// The plat is the SECOND attachment of the SECOND message (the enclosure).
	if plat != "p02-02-plat.pdf" {
		t.Errorf("plat extracted as %q, want p02-02-plat.pdf (message 2, attachment 2)", plat)
	}
}

// A PDF attached to five messages in a thread is one file on disk and five
// citations in the manifest. Bounding the disk cost and recording that it was
// sent repeatedly fall out of the same dedup.
func TestIdenticalAttachmentIsExtractedOnceAndCitedTwice(t *testing.T) {
	arc := writeArchive(t, archiveWithAttachments())
	n, dir, err := ExtractEmailAttachments(arc, arc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("wrote %d files, want 2 — the duplicate survey was written twice", n)
	}
	man, err := os.ReadFile(filepath.Join(dir, "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	// One row for the survey, naming both messages that carried it.
	if strings.Count(string(man), "p01-01-survey-2019.pdf") != 1 {
		t.Errorf("survey has more than one manifest row:\n%s", man)
	}
	for _, want := range []string{"p. 1", "p. 2"} {
		if !strings.Contains(string(man), want) {
			t.Errorf("manifest does not cite %s as having carried the survey:\n%s", want, man)
		}
	}
}

// The manifest is the chain of custody. Without it the extracted files are a
// pile of PDFs with no provenance — the "second original" failure the whole
// design is arranged to avoid.
func TestManifestRecordsTheChain(t *testing.T) {
	arc := writeArchive(t, archiveWithAttachments())
	_, dir, err := ExtractEmailAttachments(arc, arc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "MANIFEST.md"))
	if err != nil {
		t.Fatal(err)
	}
	man := string(b)
	survey := "%PDF-1.4 THE SURVEY"
	for what, want := range map[string]string{
		"the archive it came from":      "brokerlog.eml",
		"the message page":              "p. 1",
		"who sent that message":         "Larry <larry@example.com>",
		"when":                          "15 May 2021",
		"what it was about":             `Subject: "Survey"`,
		"what the message called it":    "survey 2019.pdf",
		"the declared media type":       "application/pdf",
		"the byte count":                "| " + strconv.Itoa(len(survey)) + " |",
		"the hash of what was received": HashHex([]byte(survey)),
	} {
		if !strings.Contains(man, want) {
			t.Errorf("manifest does not record %s (%q) — the chain is incomplete:\n%s", what, want, man)
		}
	}
	// And it says the copies are copies, so nobody cites one as an original.
	for _, want := range []string{"byte-for-byte copy", "Cite the ARCHIVE"} {
		if !strings.Contains(man, want) {
			t.Errorf("manifest does not say %q:\n%s", want, man)
		}
	}
}

// Re-running must produce identical bytes. A manifest that reshuffles on every
// ingest would make the sidecar itself churn the index it is meant to feed.
func TestExtractionIsDeterministic(t *testing.T) {
	arc := writeArchive(t, archiveWithAttachments())
	first := func() string {
		_, dir, err := ExtractEmailAttachments(arc, arc)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "MANIFEST.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if a, b := first(), first(); a != b {
		t.Errorf("manifest is not stable across runs:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
}

// A declared filename is attacker-controlled text. "../../.ssh/authorized_keys"
// is a thing a message can claim, and an extractor that honours it writes
// outside the corpus.
func TestDeclaredFilenameCannotEscapeTheDirectory(t *testing.T) {
	for _, declared := range []string{
		"../../escaped.pdf",
		`..\..\escaped.pdf`,
		"/etc/passwd",
		"....//....//escaped.pdf",
	} {
		eml := "From: a@example.com\r\nSubject: X\r\n" +
			"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
			"--B\r\nContent-Type: application/pdf\r\n" +
			"Content-Disposition: attachment; filename=\"" + declared + "\"\r\n" +
			"Content-Transfer-Encoding: base64\r\n\r\n" + b64("payload") + "\r\n" +
			"--B--\r\n"
		arc := writeArchive(t, eml)
		_, dir, err := ExtractEmailAttachments(arc, arc)
		if err != nil {
			t.Fatalf("%s: %v", declared, err)
		}
		for _, n := range dirNames(t, dir) {
			if strings.ContainsAny(n, `/\`) || strings.Contains(n, "..") {
				t.Errorf("declared %q produced the path-bearing name %q", declared, n)
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(arc), "escaped.pdf")); err == nil {
			t.Errorf("declared %q escaped the sidecar directory", declared)
		}
	}
}

// The extension decides how the extracted file is later read, so it survives
// sanitising; a part that declared no name at all still gets one that
// ClassifyDoc can route.
func TestSafeNameKeepsTheExtensionAndInventsOneWhenItMust(t *testing.T) {
	for _, tc := range []struct{ in, mime, want string }{
		{"survey 2019.pdf", "application/pdf", "survey-2019.pdf"},
		{"Amendment #3 (signed).PDF", "application/pdf", "Amendment-3-signed-.PDF"},
		{"", "application/pdf", "unnamed.pdf"},
		{"", "", "unnamed.bin"},
		{"...", "image/png", "unnamed.png"},
		{"noext", "application/pdf", "noext"},
	} {
		if got := safeName(tc.in, tc.mime); got != tc.want {
			t.Errorf("safeName(%q, %q) = %q, want %q", tc.in, tc.mime, got, tc.want)
		}
	}
	// A pathological name must not produce a pathological filename.
	if got := safeName(strings.Repeat("x", 900)+".pdf", ""); len(got) > 120 {
		t.Errorf("safeName did not cap a 900-character name: %d chars", len(got))
	}
	if got := safeName(strings.Repeat("x", 900)+".pdf", ""); !strings.HasSuffix(got, ".pdf") {
		t.Errorf("capping ate the extension: %q", got)
	}
}

// An archive with nothing attached must not leave an empty directory sitting in
// the corpus.
func TestArchiveWithNoAttachmentsLeavesNoDirectory(t *testing.T) {
	arc := writeArchive(t, "From: a@example.com\r\nSubject: X\r\n\r\nJust a note.\r\n")
	n, dir, err := ExtractEmailAttachments(arc, arc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("extracted %d files from an archive with none", n)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("an empty sidecar directory was created at %s", dir)
	}
}

// The sidecar goes beside the document's place in the CORPUS, not beside the
// temp file the worker fetched into — which is deleted moments later.
func TestSidecarFollowsTheCorpusPathNotTheSourceBytes(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "raglit-9999.eml")
	if err := os.WriteFile(tmp, []byte(archiveWithAttachments()), 0o644); err != nil {
		t.Fatal(err)
	}
	corpus := filepath.Join(t.TempDir(), "case", "brokerlog.eml")
	if err := os.MkdirAll(filepath.Dir(corpus), 0o755); err != nil {
		t.Fatal(err)
	}
	n, dir, err := ExtractEmailAttachments(tmp, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("wrote %d files, want 2", n)
	}
	if !strings.HasPrefix(dir, filepath.Dir(corpus)) {
		t.Errorf("sidecar at %s, want under %s", dir, filepath.Dir(corpus))
	}
	if _, err := os.Stat(filepath.Join(dir, "MANIFEST.md")); err != nil {
		t.Errorf("no manifest beside the corpus copy: %v", err)
	}
}

// An http(s) archive has no place beside it to write, and inventing one is how
// a file ends up somewhere with no chain back to where it came from.
func TestNoCorpusPathMeansNoExtraction(t *testing.T) {
	for _, u := range []string{"https://example.com/log.eml", "relative/log.eml", ""} {
		if got := archiveCorpusPath(u); got != "" {
			t.Errorf("archiveCorpusPath(%q) = %q, want \"\" (nowhere to write)", u, got)
		}
	}
	local := writeArchive(t, archiveWithAttachments())
	if got := archiveCorpusPath("file://" + local); got != local {
		t.Errorf("archiveCorpusPath(file://%s) = %q, want the path", local, got)
	}
	if got := archiveCorpusPath(local); got != local {
		t.Errorf("archiveCorpusPath(%s) = %q, want the path", local, got)
	}
}

// Extraction is opt-in and OFF by default: an archive can carry 69 files and
// putting them in somebody's corpus uninvited is not an indexer's call.
func TestAttachmentExtractionIsOffUnlessAskedFor(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "brokerlog.eml")
	cfgDir := filepath.Join(dir, ProjectHomeName)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// No config anywhere: the store's setting stands, and that defaults off.
	if extractAttachmentsForDoc(filepath.Join(t.TempDir(), "x.eml"), false) {
		t.Error("extraction on with no config and a false fallback")
	}
	// The document's OWN project decides, in both directions.
	write(`{"project":"m","extract_email_attachments":true}`)
	if !extractAttachmentsForDoc(doc, false) {
		t.Error("the project asked for extraction and was ignored")
	}
	write(`{"project":"m","extract_email_attachments":false}`)
	if extractAttachmentsForDoc(doc, true) {
		t.Error("the project refused extraction and was overridden")
	}
	// A config with no opinion defers to the store, and does not disturb the
	// transcription flag it shares a file with.
	write(`{"project":"m","writeback_transcription_md":true}`)
	for _, fb := range []bool{true, false} {
		if got := extractAttachmentsForDoc(doc, fb); got != fb {
			t.Errorf("a config with no opinion returned %v, want the fallback %v", got, fb)
		}
	}
	if !writebackForDoc(doc, false) {
		t.Error("sharing the walk broke the transcription writeback flag")
	}
}

// The config must reach the Store, per index or project-wide. A flag that is
// read from JSON and never consulted is the same silent gap that made three
// earlier fixes no-ops.
func TestAttachmentConfigReachesTheStore(t *testing.T) {
	for _, tc := range []struct {
		name, cfg string
		want      bool
	}{
		{"unset", `{"project":"p"}`, false},
		{"project-wide", `{"project":"p","extract_email_attachments":true}`, true},
		{"per-index", `{"project":"p","indexes":{"default":{"extract_email_attachments":true}}}`, true},
		{"other index only", `{"project":"p","indexes":{"other":{"extract_email_attachments":true}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := OpenIndex(Home(dir), "default")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.extractEmailAttachments != tc.want {
				t.Errorf("extractEmailAttachments = %v, want %v — config did not reach the store",
					s.extractEmailAttachments, tc.want)
			}
		})
	}
}

// End to end on the real ingest path. The worker fetches into a temp file and
// deletes it; the sidecar must land beside the archive in the CORPUS, the
// archive must still index as pages, and none of it may happen unless asked.
func TestWorkerExtractsAttachmentsOnlyWhenConfigured(t *testing.T) {
	run := func(t *testing.T, on bool) (string, *Store, string) {
		t.Helper()
		dir := t.TempDir()
		arc := filepath.Join(dir, "brokerlog.eml")
		if err := os.WriteFile(arc, []byte(archiveWithAttachments()), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := OpenHome(Home(filepath.Join(dir, "home")))
		if err != nil {
			t.Fatal(err)
		}
		s.SetExtractEmailAttachments(on)
		url := "file://" + arc
		if _, err := s.Enqueue(url, ""); err != nil {
			t.Fatal(err)
		}
		if did, err := (&Worker{Store: s}).ProcessOne(context.Background()); err != nil || !did {
			t.Fatalf("ProcessOne: did=%v err=%v", did, err)
		}
		return arc, s, url
	}

	t.Run("off", func(t *testing.T) {
		arc, s, _ := run(t, false)
		defer s.Close()
		if _, err := os.Stat(AttachmentDir(arc)); err == nil {
			t.Error("attachments extracted into the corpus without being asked for")
		}
	})

	t.Run("on", func(t *testing.T) {
		arc, s, url := run(t, true)
		defer s.Close()
		names := dirNames(t, AttachmentDir(arc))
		if len(names) != 3 {
			t.Fatalf("sidecar holds %v, want two attachments and a manifest", names)
		}
		// The archive itself still indexes, as pages, under its own URL.
		st, _ := s.IndexStatus()
		if st.Documents != 1 || st.Done != 1 {
			t.Fatalf("archive did not index alongside extraction: %+v", st)
		}
		if hits, _ := s.Search("plat", 5); len(hits) == 0 || hits[0].Path != url {
			t.Fatalf("archive not searchable under its URL: %+v", hits)
		}
		// The extracted files must be files DISCOVERY will pick up — that is the
		// whole mechanism by which they become reachable, and it is silent if
		// ClassifyDoc does not recognise what came out.
		for _, n := range names {
			if n == manifestName {
				continue
			}
			if k := ClassifyDoc(filepath.Join(AttachmentDir(arc), n), ""); k == KindUnknown {
				t.Errorf("extracted %q classifies as KindUnknown, so no sync will index it", n)
			}
		}
	})
}
