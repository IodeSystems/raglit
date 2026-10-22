package main

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// The plist is well-formed XML, carries every daemon argument as its own
// string (no shell splitting), and escapes what XML needs escaped.
func TestRenderPlist(t *testing.T) {
	out := renderPlist("raglit", "/Users/x/go/bin/raglit",
		[]string{"-addr", "127.0.0.1:7420", "-note", "a & <b>"},
		[]string{"PATH=/opt/homebrew/bin:/usr/bin", "RAGLIT_NO_AUTOBUILD=1"},
		true, "/Users/x/Library/Logs/raglit/raglit.log")

	dec := xml.NewDecoder(strings.NewReader(out))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	var strs []string
	for {
		tok, err := dec.Token()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("plist is not well-formed XML: %v\n%s", err, out)
			}
			break
		}
		if cd, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(cd)) != "" {
			strs = append(strs, string(cd))
		}
	}
	joined := strings.Join(strs, "|")
	for _, want := range []string{
		"com.iodesystems.raglit",
		"/Users/x/go/bin/raglit|daemon|-addr|127.0.0.1:7420|-note|a & <b>",
		"PATH|/opt/homebrew/bin:/usr/bin",
		"RAGLIT_NO_AUTOBUILD|1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plist values %q lack %q", joined, want)
		}
	}
	for _, want := range []string{"<key>RunAtLoad</key>\n\t<true/>", "<key>SuccessfulExit</key>\n\t\t<false/>", "<integer>120</integer>"} {
		if !strings.Contains(out, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(renderPlist("raglit", "/r", nil, nil, false, "/l"), "<true/>") {
		t.Error("without --enable the agent must not run at load")
	}
}

// launchd starts agents without Homebrew on PATH; install records the caller's
// PATH unless --env already set one.
func TestWithPath(t *testing.T) {
	got := withPath([]string{"A=1"}, "/opt/homebrew/bin")
	if strings.Join(got, ",") != "A=1,PATH=/opt/homebrew/bin" {
		t.Errorf("got %v", got)
	}
	got = withPath([]string{"PATH=/mine"}, "/opt/homebrew/bin")
	if strings.Join(got, ",") != "PATH=/mine" {
		t.Errorf("an explicit PATH must win: got %v", got)
	}
}
