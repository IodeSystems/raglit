package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A unit written before the flags went POSIX: doctor finds the single-dash
// flags and renders the unit install would write, keeping exec and environment.
func TestDoctorFindsAStaleUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "[Service]\nExecStartPre=/opt/r daemon -check -addr 127.0.0.1:7420,.76 -embed\n" +
		"ExecStart=\"/opt/a b/raglit\" daemon -addr 127.0.0.1:7420,.76 -embed --note \"50%% \\\"x\\\"\"\n" +
		"Environment=RAGLIT_NO_AUTOBUILD=1\n"
	if err := os.WriteFile(filepath.Join(dir, "raglit.service"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	c, ok, err := checkUnit("raglit")
	if !ok || err != nil {
		t.Fatalf("checkUnit: ok=%v err=%v", ok, err)
	}
	if len(c.reasons) == 0 || !strings.Contains(c.reasons[0], "single-dash") {
		t.Errorf("reasons = %q, want the single-dash flags named", c.reasons)
	}
	want := renderUnit("/opt/a b/raglit", []string{"--addr", "127.0.0.1:7420,.76", "--embed", "--note", `50% "x"`},
		[]string{"RAGLIT_NO_AUTOBUILD=1"})
	if c.want != want {
		t.Errorf("rewrite =\n%s\nwant\n%s", c.want, want)
	}

	// The rewrite is stable: a unit install wrote checks clean.
	if err := os.WriteFile(c.path, []byte(c.want), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := checkUnit("raglit"); len(c.reasons) != 0 {
		t.Errorf("a freshly rendered unit is stale: %q", c.reasons)
	}
}

func TestSplitUnitWordsUndoesQuoteUnit(t *testing.T) {
	in := []string{"/opt/a b/raglit", "daemon", "--x", `a"b\c`, "100%", "plain"}
	var q []string
	for _, w := range in {
		q = append(q, quoteUnit(w))
	}
	got, err := splitUnitWords(strings.Join(q, " "))
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Errorf("splitUnitWords = %q, %v; want %q", got, err, in)
	}
}
