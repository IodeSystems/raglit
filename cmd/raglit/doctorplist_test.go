package main

import (
	"runtime"
	"strings"
	"testing"
)

// TestDoctorPlistCheck is the composition test for the plist check: a plist
// written before the flags went POSIX is reported the way a stale unit is
// (parse → posixArgs → renderPlist → compare), a freshly rendered one is clean,
// and the darwin gate / no-plist case is not a failure.
func TestDoctorPlistCheck(t *testing.T) {
	// A plist written with single-dash long flags: doctor finds them and
	// renders the plist install would write, keeping exec, environment and settings.
	stale := renderPlist("raglit", "/opt/a b/raglit",
		[]string{"-addr", "127.0.0.1:7420,.76", "-embed", "--note", `50% "x"`},
		[]string{"RAGLIT_NO_AUTOBUILD=1"}, true, "Library/Logs/raglit/raglit.log")
	want, reasons, err := plistCheckCore(stale)
	if err != nil {
		t.Fatalf("plistCheckCore: %v", err)
	}
	if len(reasons) == 0 || !strings.Contains(reasons[0], "single-dash") {
		t.Errorf("reasons = %q, want the single-dash flags named", reasons)
	}
	got := renderPlist("raglit", "/opt/a b/raglit",
		[]string{"--addr", "127.0.0.1:7420,.76", "--embed", "--note", `50% "x"`},
		[]string{"RAGLIT_NO_AUTOBUILD=1"}, true, "Library/Logs/raglit/raglit.log")
	if want != got {
		t.Errorf("rewrite =\n%s\nwant\n%s", want, got)
	}

	// The rewrite is stable: a plist install wrote checks clean.
	if _, reasons, err := plistCheckCore(want); err != nil || len(reasons) != 0 {
		t.Errorf("a freshly rendered plist is stale: %q (err %v)", reasons, err)
	}
}

// TestDoctorPlistCheckGate covers the OS gate and the no-plist case: on a
// machine that is not darwin there is no plist to check, and that is not a
// failure — the same shape as checkUnit's non-Linux return.
func TestDoctorPlistCheckGate(t *testing.T) {
	c, ok, err := checkPlist(serviceUnitName)
	if runtime.GOOS == "darwin" {
		// On darwin a missing plist is ok=false, err=nil. A present plist is
		// ok=true. Either way, no plist is not an error.
		if err != nil {
			t.Fatalf("checkPlist: %v", err)
		}
		_ = c
		return
	}
	// Not darwin: the gate closes before any file is read.
	if ok || err != nil {
		t.Errorf("checkPlist on %s = ok %v, err %v; want ok false, err nil", runtime.GOOS, ok, err)
	}
}
