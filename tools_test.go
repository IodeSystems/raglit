package raglit

import (
	"os"
	"strings"
	"testing"
)

// The probe must name every tool the router can dispatch to. A tool that can
// fail an ingest and is not in this list is a failure with no readiness check.
func TestProbeCoversEveryRoutedTool(t *testing.T) {
	env := ProbeTools()
	want := []string{"pdftotext", "pdftoppm", "pandoc", "antiword", "magick", "xls2csv"}
	have := map[string]bool{}
	for _, tl := range env.Tools {
		have[tl.Name] = true
		if tl.Purpose == "" {
			t.Errorf("%s has no purpose — an operator cannot act on a bare name", tl.Name)
		}
		if !tl.Found && tl.Install == "" {
			t.Errorf("%s is missing and offers no install hint", tl.Name)
		}
		if tl.Found && tl.Path == "" {
			t.Errorf("%s found but no path recorded — the path IS the finding when two processes disagree", tl.Name)
		}
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("probe does not cover %q", w)
		}
	}
	if env.PATH != os.Getenv("PATH") {
		t.Error("the probe must record the PATH it actually measured with")
	}
}

// The case that started this: a tool present for one process and absent for the
// other. Reported in BOTH directions, because "the daemon has it and the shell
// does not" is equally a misconfiguration and equally invisible.
func TestDisagreementNamesWhoIsMissingIt(t *testing.T) {
	shell := ToolEnv{Who: "this shell", Tools: []ToolStatus{
		{Name: "pandoc", Found: true, Path: "/home/u/local/bin/pandoc"},
		{Name: "pdftotext", Found: true, Path: "/usr/bin/pdftotext"},
	}}
	daemon := ToolEnv{Who: "daemon (pid 1)", Tools: []ToolStatus{
		{Name: "pandoc", Found: false},
		{Name: "pdftotext", Found: true, Path: "/usr/bin/pdftotext"},
	}}
	d := shell.Disagreements(daemon)
	if len(d) != 1 {
		t.Fatalf("want exactly one disagreement, got %d: %v", len(d), d)
	}
	if !strings.Contains(d[0], "pandoc") || !strings.Contains(d[0], "MISSING for daemon (pid 1)") {
		t.Errorf("the line must name the tool and WHICH process lacks it: %q", d[0])
	}
	// Swapping the receiver must not change WHO LACKS THE TOOL. The comparison
	// is a statement about two environments, not about the caller: the daemon is
	// still the one without pandoc, however the question is asked. Only the
	// phrasing moves.
	r := daemon.Disagreements(shell)
	if len(r) != 1 || !strings.Contains(r[0], "MISSING for daemon (pid 1)") {
		t.Errorf("the missing side is a fact, not a function of the receiver: %v", r)
	}
}

// Quieter and worse than a missing tool: both processes find one, and they are
// different binaries. Two pandocs of different vintages read the same .docx into
// different text and nothing in an index records which ran.
func TestDifferentBinariesIsADisagreement(t *testing.T) {
	a := ToolEnv{Who: "shell", Tools: []ToolStatus{{Name: "pandoc", Found: true, Path: "/usr/bin/pandoc"}}}
	b := ToolEnv{Who: "daemon", Tools: []ToolStatus{{Name: "pandoc", Found: true, Path: "/opt/old/pandoc"}}}
	d := a.Disagreements(b)
	if len(d) != 1 || !strings.Contains(d[0], "different binaries") {
		t.Fatalf("two different pandocs must be reported: %v", d)
	}
	if !strings.Contains(d[0], "/usr/bin/pandoc") || !strings.Contains(d[0], "/opt/old/pandoc") {
		t.Errorf("both paths must appear or the operator cannot tell which to fix: %q", d[0])
	}
}

// Agreement is silence. A check that reports on a healthy system trains people
// to ignore it.
func TestAgreementProducesNothing(t *testing.T) {
	a := ToolEnv{Who: "shell", Tools: []ToolStatus{{Name: "pandoc", Found: true, Path: "/usr/bin/pandoc"}}}
	b := ToolEnv{Who: "daemon", Tools: []ToolStatus{{Name: "pandoc", Found: true, Path: "/usr/bin/pandoc"}}}
	if d := a.Disagreements(b); len(d) != 0 {
		t.Errorf("identical environments must disagree about nothing, got %v", d)
	}
	// Absent from BOTH is not a disagreement either — it is one honest report.
	c := ToolEnv{Who: "shell", Tools: []ToolStatus{{Name: "pandoc"}}}
	d := ToolEnv{Who: "daemon", Tools: []ToolStatus{{Name: "pandoc"}}}
	if got := c.Disagreements(d); len(got) != 0 {
		t.Errorf("missing in both is not a disagreement, got %v", got)
	}
}

// The PATH difference is the CAUSE, and printing it is what turns "pandoc not
// installed" into a one-line fix.
func TestPathDiffIsolatesTheMissingDirectory(t *testing.T) {
	sep := string(os.PathListSeparator)
	shell := ToolEnv{PATH: strings.Join([]string{"/home/u/local/bin", "/usr/bin", "/bin"}, sep)}
	daemon := ToolEnv{PATH: strings.Join([]string{"/usr/bin", "/bin"}, sep)}
	onlyShell, onlyDaemon := shell.PathDiff(daemon)
	if len(onlyShell) != 1 || onlyShell[0] != "/home/u/local/bin" {
		t.Errorf("the directory the daemon lacks must be named: %v", onlyShell)
	}
	if len(onlyDaemon) != 0 {
		t.Errorf("the daemon has no extra directories here: %v", onlyDaemon)
	}
	// Empty segments are noise, not entries.
	a := ToolEnv{PATH: "/usr/bin" + sep + sep + "/bin"}
	b := ToolEnv{PATH: "/usr/bin" + sep + "/bin"}
	if x, y := a.PathDiff(b); len(x) != 0 || len(y) != 0 {
		t.Errorf("an empty PATH segment is not a difference: %v %v", x, y)
	}
}

// A tool with alternatives is present when ANY of them is. Reporting "antiword
// missing" on a box that has catdoc is a false alarm, and false alarms are how a
// readiness check stops being read.
func TestAlternativesCountAsFound(t *testing.T) {
	var spec struct{ alts []string }
	for _, s := range probeSpec {
		if s.name == "antiword" {
			spec.alts = s.alts
		}
	}
	if len(spec.alts) < 2 {
		t.Fatal("antiword should declare catdoc as an alternative")
	}
	found := false
	for _, s := range probeSpec {
		if s.name == "magick" && len(s.alts) >= 2 {
			found = true
		}
	}
	if !found {
		t.Error("magick should accept `convert` — ImageMagick 6 ships only that name")
	}
}
