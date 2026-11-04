package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The daily check of a release build: fetch the newest release's manifest
// and its signature, verify them with a key this build trusts, refuse one
// older than what was seen before (a replayed manifest) or than this build,
// and when it names a newer Name for this platform, install it at Exe. The
// caller decides what Exe is: raglit's daemon installs to a staging file,
// checks it, and only then moves it over its own binary.

// Interval is how often the check runs at most.
const Interval = 24 * time.Hour

// State is what the check keeps between runs, in StateFile.
type State struct {
	Checked   time.Time `json:"checked"`
	Version   string    `json:"version,omitempty"`   // the newest manifest's version seen
	Published string    `json:"published,omitempty"` // and its publication time
	Keys      []string  `json:"keys,omitempty"`      // next keys endorsed by a trusted one
	Staged    string    `json:"staged,omitempty"`    // the version last installed
	Error     string    `json:"error,omitempty"`     // what the last check ran into
}

// StateFile is the check's state in home.
func StateFile(home string) string { return filepath.Join(home, "update.json") }

// LoadState reads the check's state ("" fields when there is none).
func LoadState(path string) State { return loadState(path) }

func loadState(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Updater is one release build's check.
type Updater struct {
	ManifestURL string
	Name        string      // the artifact in the manifest, and the file in its archive
	Keys        []PublicKey // the keys compiled into this build
	Current     string      // this build's version
	Exe         string      // this binary's file, which the new build replaces
	Home        string      // where StateFile lives
	Platform    string      // Platform(runtime.GOOS, runtime.GOARCH)
	Fetch       *Fetcher
	Now         func() time.Time
}

// Result is what a check did. Staged: a new build is at Exe.
type Result struct {
	Checked bool
	Staged  bool
	From    string
	To      string
	Summary string
}

// ErrNotDue: the last check was less than Interval ago.
var ErrNotDue = errors.New("checked less than a day ago")

// Run checks once, unless the last check was less than Interval ago.
func (u *Updater) Run(ctx context.Context) (res Result, err error) {
	now := time.Now
	if u.Now != nil {
		now = u.Now
	}
	sf := StateFile(u.Home)
	st := loadState(sf)
	if d := now().Sub(st.Checked); d >= 0 && d < Interval {
		return Result{}, ErrNotDue
	}
	cur, err := ParseVersion(u.Current)
	if err != nil {
		return Result{}, fmt.Errorf("this build: %w", err)
	}
	// Record the attempt first: a check that fails (offline, a bad
	// manifest) is not retried on every start.
	st.Checked = now()
	if err := saveState(sf, st); err != nil {
		return Result{}, err
	}
	res = Result{Checked: true, From: u.Current}
	defer func() { // what the last check ran into, for doctor
		st.Error = ""
		if err != nil {
			st.Error = err.Error()
		}
		_ = saveState(sf, st)
	}()

	body, err := u.Fetch.Small(ctx, u.ManifestURL, MaxManifest)
	if err != nil {
		return res, err
	}
	sig, err := u.Fetch.Small(ctx, u.ManifestURL+".minisig", MaxSig)
	if err != nil {
		return res, err
	}
	keys := append([]PublicKey(nil), u.Keys...)
	for _, line := range st.Keys {
		if k, err := ParsePublicKey(line); err == nil {
			keys = append(keys, k)
		}
	}
	m, next, err := VerifyManifest(body, string(sig), keys)
	if err != nil {
		return res, err
	}
	mv, _ := ParseVersion(m.Version)
	// Replay: a signed manifest stays valid for ever, so one older than a
	// manifest seen before, or than this build, is refused.
	if prev, err := ParseVersion(st.Version); err == nil && mv.Compare(prev) < 0 {
		return res, &VerifyError{fmt.Sprintf("the manifest is %s, older than %s seen before (a replay?)", m.Version, st.Version)}
	}
	if p, err := time.Parse(time.RFC3339, st.Published); err == nil && m.PublishedAt().Before(p) {
		return res, &VerifyError{fmt.Sprintf("the manifest was published %s, before one seen %s (a replay?)", m.Published, st.Published)}
	}
	if mv.Compare(cur) < 0 {
		return res, &VerifyError{fmt.Sprintf("the manifest is %s, older than this build %s (a replay?)", m.Version, u.Current)}
	}
	st.Version, st.Published = m.Version, m.Published
	if next != nil && !slices.Contains(st.Keys, next.Line()) {
		st.Keys = append(st.Keys, next.Line())
	}
	if err := saveState(sf, st); err != nil {
		return res, err
	}

	a, file, ok := m.File(u.Name, u.Platform)
	if !ok {
		return res, nil // no build for this platform in this release
	}
	av, err := ParseVersion(a.Version)
	if err != nil {
		return res, &VerifyError{u.Name + "'s version: " + err.Error()}
	}
	switch c := av.Compare(cur); {
	case c == 0:
		return res, nil
	case c < 0:
		return res, &VerifyError{fmt.Sprintf("the manifest offers %s %s, older than this %s: a downgrade is refused", u.Name, a.Version, u.Current)}
	}
	if _, err := u.Fetch.Install(ctx, file, u.Name, u.Exe); err != nil {
		return res, err
	}
	st.Staged = a.Version
	res.Staged, res.To, res.Summary = true, a.Version, m.Summary
	return res, nil
}

// Latest fetches and verifies the newest release's manifest at url, with the
// keys this build trusts and any next key endorsed before (in StateFile), and
// installs nothing and records nothing: what a version check shows as "latest".
func Latest(ctx context.Context, f *Fetcher, url string, keys []PublicKey, home string) (*Manifest, error) {
	body, err := f.Small(ctx, url, MaxManifest)
	if err != nil {
		return nil, err
	}
	sig, err := f.Small(ctx, url+".minisig", MaxSig)
	if err != nil {
		return nil, err
	}
	keys = append([]PublicKey(nil), keys...)
	for _, line := range loadState(StateFile(home)).Keys {
		if k, err := ParsePublicKey(line); err == nil {
			keys = append(keys, k)
		}
	}
	m, _, err := VerifyManifest(body, string(sig), keys)
	return m, err
}
