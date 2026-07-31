package main

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// Build identity, for telling two raglits apart.
//
// A client and the daemon it calls are separate processes started at separate
// times from separate binaries, and nothing forces them to agree. The failure
// that motivates this is not hypothetical: a daemon launched at boot from an
// older install kept answering for a `raglit` on PATH that was six hours newer,
// so `.eml` files the client's code could read were silently not indexed —
// there was no error anywhere, only a capability the daemon did not have.
//
// `const version` cannot detect that. It is a literal that has read "0.1.0"
// across every build ever made, so two binaries months apart compare equal.
// What actually distinguishes them is already in the binary: Go stamps
// vcs.revision, vcs.time and vcs.modified into every build made from a git
// checkout, with no ldflags and no build tags. vcs.time is the commit time, so
// it ORDERS two builds — which is the whole question a mismatch warning has to
// answer. "They differ" is not useful on its own; "the daemon is older, restart
// it" is.
type buildID struct {
	Revision string    // vcs.revision, full 40-char sha ("" if unstamped)
	Time     time.Time // vcs.time — commit time; the only orderable field
	Modified bool      // vcs.modified — built from a tree with uncommitted edits
}

// thisBuild is this process's own identity, read once. The health handler is on
// every client's hot path, so it answers from here rather than re-reading the
// build info per request.
var thisBuild = currentBuild()

// currentBuild reads this process's own stamps.
func currentBuild() buildID {
	var b buildID
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.time":
			b.Time, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// known reports whether there is enough here to compare against another build.
// An unstamped binary (`go build -buildvcs=false`, or a build from a tarball
// rather than a checkout) yields nothing, and guessing from a zero value would
// report every peer as newer.
func known(b buildID) bool { return b.Revision != "" && !b.Time.IsZero() }

// String renders a build for a human: short sha, commit date, dirty marker.
func (b buildID) String() string {
	if b.Revision == "" && b.Time.IsZero() {
		return "unknown build"
	}
	var parts []string
	if r := b.Revision; r != "" {
		if len(r) > 7 {
			r = r[:7]
		}
		parts = append(parts, r)
	}
	if !b.Time.IsZero() {
		parts = append(parts, b.Time.UTC().Format("2006-01-02 15:04Z"))
	}
	s := strings.Join(parts, " ")
	if b.Modified {
		s += " (dirty)"
	}
	return s
}

// sameBuild reports whether two builds are the same code. Two dirty builds of
// one revision are NOT the same code — the uncommitted part is invisible here —
// so a dirty build on either side never compares equal, and the caller falls
// through to the warning.
func sameBuild(a, b buildID) bool {
	return a.Revision == b.Revision && !a.Modified && !b.Modified
}

// skewNote describes a client/daemon build mismatch, or "" when there is
// nothing worth saying. Callers print it; it does not decide where it goes.
//
// Silent when either side is unstamped, because "older" would be a guess. Also
// silent when the revisions match and neither tree was dirty — the common case
// is a client and daemon from one install, and a warning on every command would
// train the reader to ignore it.
func skewNote(client, daemon buildID, addr string) string {
	if !known(client) || !known(daemon) {
		return ""
	}
	if sameBuild(client, daemon) {
		return ""
	}
	head := fmt.Sprintf("raglit: version mismatch with the daemon at %s\n"+
		"  this client: %s\n"+
		"  the daemon:  %s\n", addr, client, daemon)

	switch {
	case daemon.Time.Before(client.Time):
		return head + fmt.Sprintf(
			"  the DAEMON is older by %s — it may lack features this client expects.\n"+
				"  restart it on this binary: raglit daemon --restart\n", roughly(client.Time.Sub(daemon.Time)))
	case client.Time.Before(daemon.Time):
		return head + fmt.Sprintf(
			"  this CLIENT is older by %s — the daemon may behave in ways this client does not expect.\n"+
				"  rebuild and reinstall this binary.\n", roughly(daemon.Time.Sub(client.Time)))
	default:
		// Equal commit times, different revisions or a dirty tree: real skew,
		// but nothing here orders it. Say so rather than picking a side.
		return head + "  the two builds differ but neither is clearly newer — rebuild both from the same checkout.\n"
	}
}

// roughly renders a duration at the coarsest unit that still says something,
// because "6h41m3.2s" reads as precision this measurement does not have.
func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
