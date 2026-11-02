package raglit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontmatterLen(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	for _, c := range []struct {
		name, in string
		want     string // the exact prefix that is frontmatter; "" = none
	}{
		{"lf", "---\ntitle: X\n---\n# H\n", "---\ntitle: X\n---\n"},
		{"crlf", "---\r\ntitle: X\r\n---\r\n# H\r\n", "---\r\ntitle: X\r\n---\r\n"},
		{"bom", bom + "---\ntitle: X\n---\n# H", bom + "---\ntitle: X\n---\n"},
		{"dots close", "---\ntags: [a]\n...\nbody", "---\ntags: [a]\n...\n"},
		{"blank before key", "---\n\n# c\n---\n", ""}, // "# c" is not a key
		{"blank then key", "---\n\nname: v\n---\nx", "---\n\nname: v\n---\n"},
		{"close at eof, no newline", "---\na: 1\n---", "---\na: 1\n---"},
		{"horizontal rule", "---\n\nSome prose here.\n\n---\n\nMore.", ""},
		{"rule then key-like prose", "---\nNote that this is prose.\n---\n", ""},
		{"unterminated", "---\ntitle: X\nbody continues\n", ""},
		{"empty block", "---\n---\nbody", ""},
		{"not first line", "\n---\ntitle: X\n---\n", ""},
		{"four dashes", "----\ntitle: X\n----\n", ""},
		{"trailing space opener", "--- \ntitle: X\n---\n", ""},
		{"too far", "---\ntitle: X\n" + strings.Repeat("k: v\n", maxFrontmatterBytes/4) + "---\n", ""},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in[:frontmatterLen(c.in)]; got != c.want {
				t.Fatalf("frontmatter = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMarkdownTitle_Frontmatter(t *testing.T) {
	for _, c := range []struct {
		name, path, in, want string
	}{
		{"skipped", "a.md", "---\ntitle: Meta\n---\n\n# Real Title\n", "Real Title"},
		{"crlf", "a.md", "---\r\ntitle: Meta\r\n---\r\n# Real Title\r\n", "Real Title"},
		{"bom", "a.md", "\xef\xbb\xbf---\ntitle: Meta\n---\n# Real Title\n", "Real Title"},
		{"markdown ext", "a.markdown", "---\nk: v\n---\n# T", "T"},
		{"no heading after", "a.md", "---\nk: v\n---\nprose\n# T", "a.md"},
		{"rule is not frontmatter", "a.md", "---\n\n# T\n", "a.md"},
		{"unterminated", "a.md", "---\nk: v\n# T\n", "a.md"},
		{"not markdown", "a.txt", "---\nk: v\n---\n# T\n", "a.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := markdownTitle(c.path, []byte(c.in), filepath.Base(c.path)); got != c.want {
				t.Fatalf("title %q, want %q", got, c.want)
			}
		})
	}
}

type fmFrag struct {
	text, origin string
	start, end   int
}

func fmFragments(t *testing.T, s *Store, path string) []fmFrag {
	t.Helper()
	rows, err := s.db.Query(`SELECT f.text, f.origin, f.start_off, f.end_off FROM fragments f
		JOIN documents d ON d.id=f.doc_id WHERE d.path=? ORDER BY f.page, f.ord`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []fmFrag
	for rows.Next() {
		var f fmFrag
		if err := rows.Scan(&f.text, &f.origin, &f.start, &f.end); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

func fmBody() string {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = strings.Repeat("alpha ", 8)
	}
	return "# Incident\n\n" + strings.Join(lines, "\n")
}

func TestIngestText_Frontmatter(t *testing.T) {
	const block = "---\r\ntitle: Quarterly\r\nowner: zanzibarquux\r\n---\r\n"
	text := block + fmBody()
	fc := FragConfig{Window: 120, Stride: 80, Floor: 30}
	ctx := context.Background()

	s := openMem(t)
	for pass := 0; pass < 2; pass++ { // second pass: reingest is idempotent
		n, mode, err := s.ingestText(ctx, "doc.md", "Doc", text, fc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if mode != "text-overlap" {
			t.Fatalf("mode %q", mode)
		}
		frags := fmFragments(t, s, "doc.md")
		if len(frags) != n || n < 3 {
			t.Fatalf("pass %d: %d fragments stored, ingest said %d", pass, len(frags), n)
		}
		marked := 0
		for _, f := range frags {
			if f.origin == FragOriginFrontmatter {
				marked++
				if f.text != block || f.start != 0 || f.end != len(block) {
					t.Fatalf("frontmatter fragment %+v", f)
				}
				continue
			}
			if f.origin != "" {
				t.Fatalf("unexpected origin %q", f.origin)
			}
			if strings.Contains(f.text, "zanzibarquux") || strings.Contains(f.text, "title:") {
				t.Fatalf("body fragment holds frontmatter: %q", f.text)
			}
			if f.start < len(block) {
				t.Fatalf("body fragment starts inside frontmatter: %+v", f)
			}
			// Spans index the ORIGINAL file.
			if text[f.start:f.end] != f.text {
				t.Fatalf("span [%d,%d) is not the fragment text", f.start, f.end)
			}
		}
		if marked != 1 {
			t.Fatalf("%d frontmatter fragments, want 1", marked)
		}
		// get_document reassembles the original, frontmatter included.
		doc, err := s.DocText("doc.md", 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Text != text {
			t.Fatalf("reassembly not exact:\n got %q\nwant %q", doc.Text, text)
		}
	}

	// A query on a frontmatter value finds the document via the marked fragment.
	hits, err := s.Search("zanzibarquux", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "doc.md" || hits[0].Origin != FragOriginFrontmatter {
		t.Fatalf("hits %+v", hits)
	}
	if hits[0].IsDescription() || !strings.Contains(hits[0].Caveat(), "frontmatter") {
		t.Fatalf("frontmatter hit: IsDescription=%v caveat=%q", hits[0].IsDescription(), hits[0].Caveat())
	}
}

func TestIngestText_FrontmatterNeverAppliesElsewhere(t *testing.T) {
	fc := FragConfig{Window: 120, Stride: 80, Floor: 30}
	text := "---\ntitle: X\n---\n" + fmBody()
	rule := "---\n\n" + fmBody()
	for _, c := range []struct{ name, path, text string }{
		{"txt", "notes.txt", text},
		{"md horizontal rule", "rule.md", rule},
		{"md unterminated", "open.md", "---\ntitle: X\n" + fmBody()},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := openMem(t)
			if _, _, err := s.ingestText(context.Background(), c.path, "T", c.text, fc, nil); err != nil {
				t.Fatal(err)
			}
			first := true
			for _, f := range fmFragments(t, s, c.path) {
				if f.origin != "" {
					t.Fatalf("origin %q on %s", f.origin, c.path)
				}
				if first && f.start != 0 {
					t.Fatalf("first fragment starts at %d", f.start)
				}
				first = false
			}
			doc, err := s.DocText(c.path, 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Text != c.text {
				t.Fatal("reassembly not exact")
			}
		})
	}
}

// Through the worker: the title comes from the heading after the block.
func TestWorker_MarkdownFrontmatterTitleAndFragment(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plan.md")
	body := "---\nstatus: draft\n---\n# Launch plan\n\nShip on Friday."
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Launch plan" {
		t.Fatalf("title %q", f.Title)
	}
	s := openMem(t)
	w := &Worker{Store: s}
	if _, _, err := w.ingestPlainText(context.Background(), src, f.Title, f.Data, nil); err != nil {
		t.Fatal(err)
	}
	doc, err := s.DocText(src, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != body {
		t.Fatalf("text %q", doc.Text)
	}
	var marked int
	for _, fr := range fmFragments(t, s, src) {
		if fr.origin == FragOriginFrontmatter {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d frontmatter fragments", marked)
	}
}
