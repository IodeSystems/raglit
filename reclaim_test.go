package raglit

import (
	"os"
	"path/filepath"
	"testing"
)

// A 'running' row outlives the process that owned it. Reopening the store must
// abort the rows whose owner is gone, and must NOT touch a row this process owns
// — otherwise a daemon reopening its own index would kill its own live work.
func TestReclaimOrphanedJobsAbortsOnlyDeadOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// A pid that cannot be alive, one this process owns, and pid 0 — the
	// pre-column state, whose owner is unknown but provably not us.
	const deadPID = 0x7FFFFFFE
	for _, tc := range []struct {
		url string
		pid int
	}{
		{"file:///dead.pdf", deadPID},
		{"file:///mine.pdf", os.Getpid()},
		{"file:///legacy.pdf", 0},
	} {
		if _, err := st.db.Exec(
			`INSERT INTO ingest_jobs(url, state, enqueued_at, owner_pid) VALUES(?, 'running', 0, ?)`,
			tc.url, tc.pid); err != nil {
			t.Fatal(err)
		}
	}
	st.db.Close()

	st2, err := Open(path) // reopening is what reclaims
	if err != nil {
		t.Fatal(err)
	}
	defer st2.db.Close()

	got := map[string]string{}
	rows, err := st2.db.Query(`SELECT url, state FROM ingest_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var url, state string
		if err := rows.Scan(&url, &state); err != nil {
			t.Fatal(err)
		}
		got[url] = state
	}
	want := map[string]string{
		"file:///dead.pdf":   "error",
		"file:///mine.pdf":   "running",
		"file:///legacy.pdf": "error",
	}
	for url, w := range want {
		if got[url] != w {
			t.Errorf("%s: state = %q, want %q", url, got[url], w)
		}
	}
}
