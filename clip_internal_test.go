package raglit

import (
	"testing"
	"unicode/utf8"
)

// finder.go clips a hit line for agentkit; it must not split a character.
func TestFinderClipKeepsUTF8Valid(t *testing.T) {
	s := "naïve café — résumé"
	for n := 0; n <= len(s); n++ {
		if got := clip(s, n); !utf8.ValidString(got) {
			t.Fatalf("clip(%q, %d) = %q: not valid UTF-8", s, n, got)
		}
	}
}
