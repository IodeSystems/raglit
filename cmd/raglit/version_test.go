package main

import (
	"runtime/debug"
	"testing"
)

// The version a binary reports comes from the module version Go stamped into
// it, not a literal: the literal read "0.1.0" in the v0.2.0 release.
func TestVersionFromBuildInfo(t *testing.T) {
	for _, c := range []struct {
		main, want string
	}{
		{"v0.2.1", "0.2.1"},
		{"v0.2.1-0.20261026010203-574d91e00000+dirty", "0.2.1-0.20261026010203-574d91e00000+dirty"},
		{"(devel)", "devel"},
		{"", "devel"},
	} {
		bi := &debug.BuildInfo{Main: debug.Module{Path: "github.com/iodesystems/raglit", Version: c.main}}
		if got := versionFrom(bi); got != c.want {
			t.Errorf("versionFrom(%q) = %q, want %q", c.main, got, c.want)
		}
	}
	if got := versionFrom(nil); got != "devel" {
		t.Errorf("versionFrom(nil) = %q, want devel", got)
	}
}
