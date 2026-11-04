package update

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A manifest is what one release publishes beside its archives
// (raglit-manifest.json, signed as a whole by raglit-manifest.json.minisig):
// the downloads of the release's artifacts. Each file names its URL, sha256
// and size per os_arch:
//
//	{"schema": 1, "version": "v0.4.0", "published": "2026-11-02T10:00:00Z",
//	 "summary": "one line on what changed",
//	 "key_next": {"key": "RWQ…", "signature": "untrusted comment: …"},
//	 "artifacts": {"raglit": {"version": "v0.4.0", "files": {"linux_amd64":
//	   {"url": "https://…/raglit_linux_amd64.tar.gz", "sha256": "…", "size": 123}}}}}
//
// key_next is how the signing key rotates: the next key, endorsed by the key
// that signs this manifest (a minisign signature over the key's base64
// line). A client that verifies such a manifest trusts the next key from
// then on (State.Keys), so a later manifest signed by it verifies.

// Schema is the manifest format this build reads.
const Schema = 1

// Limits on what is read from the network.
const (
	MaxManifest = 1 << 20
	MaxSig      = 4 << 10
	MaxArchive  = 512 << 20
)

type Manifest struct {
	Schema    int                 `json:"schema"`
	Version   string              `json:"version"`
	Published string              `json:"published"`
	Summary   string              `json:"summary,omitempty"`
	KeyNext   *Endorsement        `json:"key_next,omitempty"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

type Artifact struct {
	Version string          `json:"version"`
	Files   map[string]File `json:"files"`
}

type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Endorsement is a key signed by another.
type Endorsement struct {
	Key       string `json:"key"`       // the base64 key line
	Signature string `json:"signature"` // a .minisig over Key's bytes
}

// Platform is a files key: "linux_amd64".
func Platform(goos, goarch string) string { return goos + "_" + goarch }

// ParseManifest reads and checks a manifest's shape. It does not verify a
// signature: VerifyManifest does.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Schema != Schema {
		return nil, fmt.Errorf("manifest: schema %d, this build reads %d", m.Schema, Schema)
	}
	for name, a := range m.Artifacts {
		if _, err := ParseVersion(a.Version); err != nil {
			return nil, fmt.Errorf("manifest: %s: %w", name, err)
		}
		for plat, f := range a.Files {
			if err := f.check(); err != nil {
				return nil, fmt.Errorf("manifest: %s %s: %w", name, plat, err)
			}
		}
	}
	return &m, nil
}

func (f File) check() error {
	u, err := url.Parse(f.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("bad url %q", f.URL)
	}
	if h, err := hex.DecodeString(f.SHA256); err != nil || len(h) != 32 {
		return fmt.Errorf("bad sha256 %q", f.SHA256)
	}
	if f.Size <= 0 || f.Size > MaxArchive {
		return fmt.Errorf("bad size %d", f.Size)
	}
	return nil
}

// PublishedAt is the manifest's publication time (zero when unset).
func (m *Manifest) PublishedAt() time.Time {
	t, _ := time.Parse(time.RFC3339, m.Published)
	return t
}

// File is name's download for plat, if the manifest has one.
func (m *Manifest) File(name, plat string) (Artifact, File, bool) {
	a, ok := m.Artifacts[name]
	if !ok {
		return Artifact{}, File{}, false
	}
	f, ok := a.Files[plat]
	return a, f, ok
}

// VerifyManifest checks body's signature with one of keys, then parses it.
// It returns the manifest, and the next key when key_next is present and
// endorsed by the key that signed the manifest.
func VerifyManifest(body []byte, sig string, keys []PublicKey) (*Manifest, *PublicKey, error) {
	if len(keys) == 0 {
		return nil, nil, ErrNoKey
	}
	signer, err := VerifyAny(body, sig, keys)
	if err != nil {
		return nil, nil, &VerifyError{"the manifest's signature: " + err.Error()}
	}
	m, err := ParseManifest(body)
	if err != nil {
		return nil, nil, &VerifyError{err.Error()}
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return nil, nil, &VerifyError{"manifest version: " + err.Error()}
	}
	if m.PublishedAt().IsZero() {
		return nil, nil, &VerifyError{"manifest: no publication time"}
	}
	if m.KeyNext == nil {
		return m, nil, nil
	}
	next, err := ParsePublicKey(m.KeyNext.Key)
	if err != nil {
		return nil, nil, &VerifyError{"key_next: " + err.Error()}
	}
	if _, err := VerifyAny([]byte(m.KeyNext.Key), m.KeyNext.Signature, []PublicKey{signer}); err != nil {
		return nil, nil, &VerifyError{"key_next is not endorsed by the manifest's key: " + err.Error()}
	}
	return m, &next, nil
}

// VerifyError is a check that failed on what was downloaded — a signature,
// a checksum, an archive — as opposed to a network that did not answer. It
// is said to the person; an unreachable network is not.
type VerifyError struct{ why string }

func (e *VerifyError) Error() string { return e.why }

// IsVerify reports whether err is a failed check.
func IsVerify(err error) bool {
	var v *VerifyError
	return errors.As(err, &v)
}

// Version is a parsed vMAJOR.MINOR.PATCH[-pre][+build].
type Version struct {
	n   [3]int
	pre string
}

// ParseVersion reads a release version; anything else ("dev", a git
// describe with a commit count) is an error.
func ParseVersion(s string) (Version, error) {
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return Version{}, fmt.Errorf("not a release version: %q", s)
	}
	rest, _, _ = strings.Cut(rest, "+")
	core, pre, _ := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("not a release version: %q", s)
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("not a release version: %q", s)
		}
		v.n[i] = n
	}
	v.pre = pre
	return v, nil
}

// Compare is -1, 0 or 1. A pre-release is before its release; two
// pre-releases compare as strings (a simplification of semver's rule).
func (a Version) Compare(b Version) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] < b.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}
