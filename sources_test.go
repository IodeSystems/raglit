package raglit

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func writeTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func basenames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	sort.Strings(out)
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPlanSources_Rules(t *testing.T) {
	proj := t.TempDir()
	writeTree(t, proj,
		"main.go", "util.go", "main_test.go", "README.md", "config.json",
		".secret/x.md", "vendor/v.go", "docs/guide.md", "docs/guide.pdf",
	)
	no := false
	cfg := Config{
		Gitignore: &no, // walk mode — no git needed, deterministic
		Indexes: map[string]IndexConfig{
			"code": {
				Roots:   []Root{{Path: "."}},
				Include: []string{"*.go", "*.md"},
				Ignore:  []string{"*_test.go", "docs/**"},
			},
			"docs": {
				Roots: []Root{{Path: "docs", Include: []string{"*.pdf"}}}, // per-root include override
			},
		},
	}
	plan, err := PlanSources(cfg, proj)
	if err != nil {
		t.Fatal(err)
	}
	// code: *.go/*.md minus tests, dot-dir, vendor, docs/**, and non-matching json.
	if got, want := basenames(plan["code"]), []string{"README.md", "main.go", "util.go"}; !eqStrings(got, want) {
		t.Fatalf("code = %v, want %v", got, want)
	}
	// docs: only the pdf (per-root include), guide.md excluded.
	if got, want := basenames(plan["docs"]), []string{"guide.pdf"}; !eqStrings(got, want) {
		t.Fatalf("docs = %v, want %v", got, want)
	}
}

func TestPlanSources_HonorsGitignore(t *testing.T) {
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git not available")
	}
	proj := t.TempDir()
	writeTree(t, proj, "main.go", "build/out.go", ".gitignore")
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", proj, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	cfg := Config{Indexes: map[string]IndexConfig{
		"code": {Roots: []Root{{Path: "."}}, Include: []string{"*.go"}},
	}}
	plan, err := PlanSources(cfg, proj)
	if err != nil {
		t.Fatal(err)
	}
	// build/out.go is .gitignored → excluded; main.go stays.
	if got, want := basenames(plan["code"]), []string{"main.go"}; !eqStrings(got, want) {
		t.Fatalf("with .gitignore: code = %v, want %v", got, want)
	}
}
