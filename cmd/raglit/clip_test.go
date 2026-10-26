package main

import (
	"testing"
	"unicode/utf8"
)

// A hit snippet is clipped for the terminal; cutting inside a multi-byte
// character printed invalid UTF-8, which a caller decoding the output as text
// rejects outright.
func TestClipKeepsUTF8Valid(t *testing.T) {
	s := "naïve café — résumé"
	for n := 0; n <= len(s); n++ {
		if got := clip(s, n); !utf8.ValidString(got) {
			t.Fatalf("clip(%q, %d) = %q: not valid UTF-8", s, n, got)
		}
	}
}
