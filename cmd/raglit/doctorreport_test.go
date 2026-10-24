package main

import (
	"encoding/json"
	"testing"

	"github.com/iodesystems/raglit"
)

// With no models, keyword search is on and every model capability says what
// would turn it on. A missing tool with a package-manager hint carries an
// install block; one whose hint is advice does not.
func TestSetupSummaryNoModels(t *testing.T) {
	env := raglit.ToolEnv{Who: "test", Tools: []raglit.ToolStatus{
		{Name: "pdftotext", Found: true, Path: "/usr/bin/pdftotext", Purpose: "PDF text layer"},
		{Name: "pdftoppm", Found: true, Path: "/usr/bin/pdftoppm", Purpose: "PDF page rasterization"},
		{Name: "tesseract", Optional: true, Purpose: "scans", Install: "sudo apt-get install tesseract-ocr"},
		{Name: "xls2csv", Optional: true, Purpose: ".xls", Install: "no Homebrew formula (catdoc is not packaged)"},
	}}
	s := buildSetupSummary(raglit.Config{}, env)
	on := map[string]capability{}
	for _, c := range s.Capabilities {
		on[c.Name] = c
	}
	if c := on["keyword search over text, markdown and code"]; !c.On {
		t.Errorf("keyword search off with no models")
	}
	if c := on["PDF text"]; !c.On || c.Needs != "" {
		t.Errorf("PDF text = %+v", c)
	}
	for _, name := range []string{"semantic and hybrid search", "scanned pages and photos", "captions, summaries and document kinds"} {
		if c := on[name]; c.On || c.Needs == "" || c.How == "" || c.Via != "" {
			t.Errorf("%s = %+v, want off with needs and how", name, c)
		}
	}
	ex := map[string]extractor{}
	for _, e := range s.Extractors {
		ex[e.Name] = e
	}
	if in := ex["tesseract"].Install; in == nil || in.Cmd != "sudo apt-get install tesseract-ocr" || len(in.Needs) != 1 || in.Needs[0] != "system" {
		t.Errorf("tesseract install = %+v", in)
	}
	if ex["xls2csv"].Install != nil || ex["pdftotext"].Install != nil {
		t.Errorf("install block on advice or on a found tool")
	}
	if len(s.Roles) != 5 || s.Roles[3].Role != "identity" || s.Roles[3].FallsBackTo != "segment" {
		t.Errorf("roles = %+v", s.Roles)
	}
}

// The top-level keys dun reads.
func TestDoctorReportKeys(t *testing.T) {
	b, err := json.Marshal(doctorReport{setupSummary: setupSummary{Capabilities: []capability{}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	must(t, json.Unmarshal(b, &m))
	for _, k := range []string{"binary", "build", "platform", "home", "configured", "daemon", "capabilities", "roles", "extractors", "extractorsSeenBy"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%s missing in %s", k, b)
		}
	}
	if _, ok := m["update"]; ok {
		t.Errorf("update present with nothing known: %s", b)
	}
}

// The notification warns only when a core capability is off, and counts every
// capability that is.
func TestSetupNotificationLevel(t *testing.T) {
	sum := func(off ...string) setupSummary {
		s := setupSummary{}
		for _, n := range []string{"semantic and hybrid search", "PDF text", "audio and video transcripts"} {
			c := capability{Name: n, On: true}
			for _, o := range off {
				if o == n {
					c.On = false
				}
			}
			s.Capabilities = append(s.Capabilities, c)
		}
		return s
	}
	for _, c := range []struct {
		off     []string
		level   string
		missing int
	}{
		{nil, "info", 0},
		{[]string{"audio and video transcripts"}, "info", 1},
		{[]string{"semantic and hybrid search", "audio and video transcripts"}, "warning", 2},
	} {
		n := setupNotification(sum(c.off...))
		if n["level"] != c.level || n["logger"] != "raglit/setup" || n["data"].(setupData).Missing != c.missing {
			t.Errorf("off %v: %v", c.off, n)
		}
	}
}

// The daemon answers /api/setup from its own config.
func TestSetupEndpoint(t *testing.T) {
	srv, _ := settingsServer(t, `{"base_url":"http://x/v1","embed_model":"e1"}`)
	code, body := doAs(t, "GET", srv.URL+"/api/setup", "", "")
	if code != 200 {
		t.Fatalf("GET /api/setup: %d %s", code, body)
	}
	var s setupSummary
	must(t, json.Unmarshal([]byte(body), &s))
	if len(s.Capabilities) < 2 || s.Capabilities[1].Name != "semantic and hybrid search" || !s.Capabilities[1].On || s.Capabilities[1].Via != "e1" {
		t.Fatalf("capabilities = %+v", s.Capabilities)
	}
	if s.ExtractorsSeenBy == "" || len(s.Roles) != 5 {
		t.Fatalf("summary = %+v", s)
	}
}
