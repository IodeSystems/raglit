package raglit

import (
	"path/filepath"
	"regexp"
	"strings"
)

// FragOriginFrontmatter marks the one fragment that holds a Markdown file's
// YAML frontmatter block, delimiters included.
//
// Unlike 'identity' and 'fields' this fragment is NOT generated: its text is the
// file's own bytes [0,N), with those offsets. So it stays inside SQLOwnWords —
// get_document reassembles the stored text exactly, and a quote taken from the
// block still verifies against it. The mark exists so a renderer can say this
// passage is document metadata and not part of the body (Hit.Caveat), and so the
// block is not windowed as body text.
const FragOriginFrontmatter = "frontmatter"

// maxFrontmatterBytes bounds how far a closing delimiter is looked for. A file
// that opens with a horizontal rule and has its next `---` far below is a
// document, not a header.
const maxFrontmatterBytes = 32 << 10

const utf8BOM = "\xef\xbb\xbf"

var frontmatterKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*:`)

// isMarkdownPath reports whether path names a Markdown file by extension.
func isMarkdownPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// frontmatterLen returns the byte length of the YAML frontmatter block that
// opens text, through the newline ending its closing line, or 0 when text has
// none. The YAML is not parsed.
//
// A block is: an optional UTF-8 BOM, a line that is exactly `---`, a first
// non-blank line shaped like a YAML key, and a closing line `---` or `...`
// within maxFrontmatterBytes. LF and CRLF both work. The key test is what stops
// a document that merely opens with a horizontal rule from losing its first
// section to the header.
func frontmatterLen(text string) int {
	pos := 0
	if strings.HasPrefix(text, utf8BOM) {
		pos = len(utf8BOM)
	}
	line, next, ok := nextLine(text, pos)
	if !ok || line != "---" {
		return 0
	}
	seenKey := false
	for pos = next; pos < len(text) && pos <= maxFrontmatterBytes; {
		line, next, _ = nextLine(text, pos)
		if line == "---" || line == "..." {
			if !seenKey {
				return 0
			}
			return next
		}
		if !seenKey && strings.TrimSpace(line) != "" {
			if !frontmatterKey.MatchString(line) {
				return 0
			}
			seenKey = true
		}
		pos = next
	}
	return 0
}

// nextLine returns the line starting at pos without its terminator (LF or CRLF),
// and the offset just past that terminator. ok is false at end of text.
func nextLine(text string, pos int) (line string, next int, ok bool) {
	if pos >= len(text) {
		return "", pos, false
	}
	end := strings.IndexByte(text[pos:], '\n')
	if end < 0 {
		return strings.TrimSuffix(text[pos:], "\r"), len(text), true
	}
	return strings.TrimSuffix(text[pos:pos+end], "\r"), pos + end + 1, true
}
