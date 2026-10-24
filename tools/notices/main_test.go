package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A module under an allowed license passes and keeps its NOTICE; copyleft and
// unrecognised text fail.
func TestGoModule(t *testing.T) {
	mod := func(files map[string]string) string {
		d := t.TempDir()
		for n, body := range files {
			if err := os.WriteFile(filepath.Join(d, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	p, probs := goModule("ex/ok", "v1", mod(map[string]string{
		"LICENSE": "Apache License, Version 2.0, January 2004",
		"NOTICE":  "Copyright Example",
	}))
	if len(probs) != 0 || p.License != "Apache-2.0" || len(p.Texts) != 2 {
		t.Fatalf("allowed module: %+v %v", p, probs)
	}
	for name, body := range map[string]string{
		"gpl":     "GNU GENERAL PUBLIC LICENSE Version 3",
		"unknown": "All rights reserved.",
	} {
		if _, probs := goModule("ex/"+name, "v1", mod(map[string]string{"LICENSE": body})); len(probs) != 1 {
			t.Errorf("%s: problems %v, want one", name, probs)
		}
	}
	if _, probs := goModule("ex/none", "v1", mod(nil)); len(probs) != 1 || !strings.Contains(probs[0], "no license file") {
		t.Errorf("no license file: %v", probs)
	}
}

func TestNPMAllowed(t *testing.T) {
	for expr, want := range map[string]bool{
		"MIT": true, "(MIT OR GPL-3.0)": true, "MIT AND BSD-3-Clause": true,
		"GPL-3.0": false, "MIT AND GPL-3.0": false, "": false, "SEE LICENSE IN LICENSE": false,
	} {
		if got := npmAllowed(expr); got != want {
			t.Errorf("npmAllowed(%q) = %v, want %v", expr, got, want)
		}
	}
}
