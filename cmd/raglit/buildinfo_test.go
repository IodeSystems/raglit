package main

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The case this whole file exists for: a daemon left running from an older
// install answers for a newer client, and nothing says so. The warning has to
// name WHICH side is behind — "they differ" leaves the reader to guess which
// one to restart.
func TestSkewNoteNamesTheOlderSide(t *testing.T) {
	client := buildID{Revision: "7327842b", Time: at("2026-07-29T22:33:10Z")}
	daemon := buildID{Revision: "884fb703", Time: at("2026-07-29T15:51:57Z"), Modified: true}

	note := skewNote(client, daemon, "http://127.0.0.1:7420")
	if note == "" {
		t.Fatal("a 6h skew produced no warning")
	}
	for _, want := range []string{"DAEMON is older", "6h41m", "raglit daemon --restart", "7327842", "884fb70", "(dirty)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "CLIENT is older") {
		t.Errorf("named the wrong side:\n%s", note)
	}
}

func TestSkewNoteNamesAnOlderClient(t *testing.T) {
	client := buildID{Revision: "aaaaaaa", Time: at("2026-07-01T00:00:00Z")}
	daemon := buildID{Revision: "bbbbbbb", Time: at("2026-07-29T00:00:00Z")}

	note := skewNote(client, daemon, "http://127.0.0.1:7420")
	if !strings.Contains(note, "CLIENT is older") {
		t.Errorf("expected the client named as older:\n%s", note)
	}
	if !strings.Contains(note, "28 days") {
		t.Errorf("expected a coarse day count:\n%s", note)
	}
}

// Silence is the requirement in the ordinary case. A warning that fires on
// every command from a matched pair teaches the reader to skip it, and then it
// is not there when it matters.
func TestSkewNoteSilentWhenBuildsMatch(t *testing.T) {
	b := buildID{Revision: "7327842b", Time: at("2026-07-29T22:33:10Z")}
	if note := skewNote(b, b, "http://127.0.0.1:7420"); note != "" {
		t.Errorf("matched builds warned:\n%s", note)
	}
}

// An unstamped binary (-buildvcs=false, or a build from a tarball) and a daemon
// too old to report a build at all both arrive as a zero buildID. Neither can
// be ordered against anything, so neither may claim the other is older.
func TestSkewNoteSilentWhenEitherSideIsUnknown(t *testing.T) {
	known := buildID{Revision: "7327842b", Time: at("2026-07-29T22:33:10Z")}
	for name, pair := range map[string][2]buildID{
		"daemon unknown": {known, {}},
		"client unknown": {{}, known},
		"both unknown":   {{}, {}},
		"no time":        {known, {Revision: "884fb703"}},
	} {
		if note := skewNote(pair[0], pair[1], "addr"); note != "" {
			t.Errorf("%s: expected silence, got:\n%s", name, note)
		}
	}
}

// Same revision, but one side was built with uncommitted edits: the commit time
// no longer describes what is in the binary, so the two are not the same code
// and neither timestamp orders them.
func TestSkewNoteDirtyTreeIsNotAMatch(t *testing.T) {
	ts := at("2026-07-29T22:33:10Z")
	client := buildID{Revision: "7327842b", Time: ts}
	daemon := buildID{Revision: "7327842b", Time: ts, Modified: true}

	note := skewNote(client, daemon, "addr")
	if note == "" {
		t.Fatal("a dirty daemon at the same revision was treated as a match")
	}
	if !strings.Contains(note, "neither is clearly newer") {
		t.Errorf("equal timestamps should not pick a side:\n%s", note)
	}
}

func TestRoughly(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "under a minute"},
		{45 * time.Minute, "45m"},
		{6*time.Hour + 41*time.Minute, "6h41m"},
		{50 * time.Hour, "2 days"},
	} {
		if got := roughly(c.d); got != c.want {
			t.Errorf("roughly(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
