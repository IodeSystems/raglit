package raglit

import (
	"os"
	"runtime"
	"sort"
	"strings"
)

// What the external extractors look like FROM HERE — in this process, with this
// process's PATH.
//
// The whole point is that "here" differs between the shell and the daemon, and
// the difference is invisible until an ingest fails. Measured 2026-08-09: every
// .docx attachment extracted from a corpus failed with "pandoc not installed"
// while `raglit doctor`, run in a shell, printed "✓ pandoc". Both were telling
// the truth. pandoc was in ~/local/bin; the shell had it on PATH and the systemd
// user service did not, because systemd --user does not inherit a login shell's
// environment.
//
// A readiness check that probes the WRONG PROCESS is worse than none: it
// converts a missing tool into a confident green tick, and the operator then
// looks for the fault somewhere it cannot be. So the probe is a value that can
// be computed in one process and rendered in another, and every report of it
// says whose environment it describes.

// ToolStatus is one external tool as this process sees it.
type ToolStatus struct {
	// Name is what the router looks for ("pandoc"), not the package that ships it.
	Name string `json:"name"`
	// Found is whether it resolved on PATH.
	Found bool `json:"found"`
	// Path is where it resolved, empty when not found. Reported because two
	// processes can both find a tool and find DIFFERENT ONES.
	Path string `json:"path,omitempty"`
	// Purpose is what stops working without it, in the operator's terms.
	Purpose string `json:"purpose"`
	// Optional marks a tool whose absence degrades rather than breaks.
	Optional bool `json:"optional"`
	// Install is the hint printed when it is missing.
	Install string `json:"install,omitempty"`
}

// ToolEnv is the whole external-tool picture for one process.
type ToolEnv struct {
	// Who describes the process this was measured in — "shell" or "daemon (pid
	// 1234)". Set by whoever renders it; a probe cannot know what it will be
	// called.
	Who string `json:"who"`
	// PATH as this process has it. The single most useful line when two probes
	// disagree, and the reason a tool "is not installed" when it is.
	PATH string `json:"path_env"`
	// Tools, in a stable order so two environments can be diffed line by line.
	Tools []ToolStatus `json:"tools"`
	// GOOS, because an install hint for apt is noise on a mac.
	GOOS string `json:"goos"`
}

// probeSpec is the fixed list. Kept beside ClassifyDoc's router: a tool belongs
// here exactly when some document kind cannot be read without it.
var probeSpec = []struct {
	name, purpose, install string
	optional               bool
	// alts are equivalent tools; the first one found wins. antiword/catdoc both
	// read legacy .doc, and reporting "antiword missing" when catdoc is present
	// would be a false alarm.
	alts []string
}{
	{name: "pdftotext", purpose: "PDF text layer", install: "sudo apt-get install poppler-utils"},
	{name: "pdftoppm", purpose: "PDF page rasterization", install: "sudo apt-get install poppler-utils"},
	{name: "pandoc", purpose: "docx/odt/epub/html/pptx → text", install: "sudo apt-get install pandoc", optional: true},
	{name: "antiword", purpose: "legacy binary .doc → text", install: "sudo apt-get install antiword", optional: true, alts: []string{"antiword", "catdoc"}},
	{name: "magick", purpose: "HEIC/HEIF (iPhone photos) → PNG", install: "sudo apt-get install imagemagick", optional: true, alts: []string{"magick", "convert"}},
	{name: "xls2csv", purpose: "legacy binary .xls → text", install: "sudo apt-get install catdoc", optional: true},
}

// ProbeTools measures this process's view of the external extractors.
//
// Deliberately does NOT run each tool's --version: this is called on an HTTP
// path that an operator may poll, LookPath is a stat and a version probe is a
// fork, and "which binary would we run" is the question that was actually being
// got wrong.
func ProbeTools() ToolEnv {
	env := ToolEnv{PATH: os.Getenv("PATH"), GOOS: runtime.GOOS}
	for _, s := range probeSpec {
		st := ToolStatus{Name: s.name, Purpose: s.purpose, Optional: s.optional, Install: s.install}
		cands := s.alts
		if len(cands) == 0 {
			cands = []string{s.name}
		}
		for _, c := range cands {
			if p := toolPath(c); p != "" {
				st.Found, st.Path = true, p
				break
			}
		}
		env.Tools = append(env.Tools, st)
	}
	return env
}

// Disagreements lists the tools two environments see differently — missing in
// one, or resolving to different binaries.
//
// Both halves matter. "Present here, absent there" is the failure that started
// this. "Present in both, different paths" is quieter and worse: two pandocs of
// different vintages read the same .docx into different text, and nothing in an
// index records which one ran.
func (e ToolEnv) Disagreements(other ToolEnv) []string {
	mine := map[string]ToolStatus{}
	for _, t := range e.Tools {
		mine[t.Name] = t
	}
	var out []string
	for _, t := range other.Tools {
		m, ok := mine[t.Name]
		if !ok {
			continue
		}
		switch {
		case m.Found && !t.Found:
			out = append(out, t.Name+": present for "+e.Who+" ("+m.Path+"), MISSING for "+other.Who)
		case !m.Found && t.Found:
			out = append(out, t.Name+": MISSING for "+e.Who+", present for "+other.Who+" ("+t.Path+")")
		case m.Found && t.Found && m.Path != t.Path:
			out = append(out, t.Name+": different binaries — "+e.Who+" "+m.Path+", "+other.Who+" "+t.Path)
		}
	}
	sort.Strings(out)
	return out
}

// PathDiff reports PATH entries one environment has and the other does not,
// which is nearly always the CAUSE when Disagreements is non-empty.
func (e ToolEnv) PathDiff(other ToolEnv) (onlyMine, onlyOther []string) {
	set := func(p string) map[string]bool {
		m := map[string]bool{}
		for _, d := range strings.Split(p, string(os.PathListSeparator)) {
			if d = strings.TrimSpace(d); d != "" {
				m[d] = true
			}
		}
		return m
	}
	a, b := set(e.PATH), set(other.PATH)
	for d := range a {
		if !b[d] {
			onlyMine = append(onlyMine, d)
		}
	}
	for d := range b {
		if !a[d] {
			onlyOther = append(onlyOther, d)
		}
	}
	sort.Strings(onlyMine)
	sort.Strings(onlyOther)
	return onlyMine, onlyOther
}
