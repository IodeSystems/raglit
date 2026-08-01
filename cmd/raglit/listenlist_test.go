package main

import (
	"strings"
	"testing"
)

func TestParseListenListMultiple(t *testing.T) {
	got, err := parseListenList("127.0.0.1:7420,10.4.0.3:7420", "7420")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1:7420", "10.4.0.3:7420"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseListenListDefaultsPortAndDedupes(t *testing.T) {
	got, err := parseListenList("127.0.0.1, 127.0.0.1:7420 ,,", "7420")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "127.0.0.1:7420" {
		t.Fatalf("got %v, want one deduped entry", got)
	}
}

// A suffix that matches nothing must say so, and say what WAS available. Binding
// something unintended because a shorthand missed is the failure this guards.
func TestParseListenListUnknownSuffix(t *testing.T) {
	_, err := parseListenList(".251", "7420")
	if err == nil {
		t.Fatal("expected an error for a suffix matching no interface")
	}
	if !strings.Contains(err.Error(), "no interface address ends .251") {
		t.Errorf("error should name the problem, got: %v", err)
	}
	if !strings.Contains(err.Error(), "this host has") {
		t.Errorf("error should list what was available, got: %v", err)
	}
}

// The shorthand resolves against real interfaces, so it must find one this host
// genuinely holds and never fabricate an address.
func TestParseListenListSuffixResolves(t *testing.T) {
	own, err := ownIPv4()
	if err != nil || len(own) == 0 {
		t.Skip("no global IPv4 on this host")
	}
	last := own[0][strings.LastIndex(own[0], ".")+1:]
	got, err := parseListenList("."+last, "7420")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range got {
		if strings.HasSuffix(g, "."+last+":7420") {
			found = true
		}
	}
	if !found {
		t.Fatalf("resolving .%s gave %v, none of which is an address of this host", last, got)
	}
}

func TestParseListenListEmpty(t *testing.T) {
	if _, err := parseListenList("  , ", "7420"); err == nil {
		t.Fatal("expected an error for an empty list")
	}
}
