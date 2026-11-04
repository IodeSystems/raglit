// Package update is how a release build of raglit gets its next build: a
// manifest of downloads (URL, sha256 and size per os/arch) signed with
// minisign, an installer that verifies a download before anything runs it,
// and the daily check that stages a new build for the daemon to move to.
//
// Copied from dun's internal/update (same key, same manifest format), which
// another module cannot import. Whether the two copies become one shared
// module is aw4 i_1m6kvp7g4e6dwrxx5bx0x0.
//
// Nothing here knows a download location: those are in the manifest, and
// the manifest's own location is compiled into release builds only
// (cmd/raglit/release_on.go).
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// minisign's formats (https://jedisct1.github.io/minisign/), verified here
// in-process and offline on crypto/ed25519 and BLAKE2b:
//
//	public key:  "Ed" | key id (8) | Ed25519 public key (32), base64
//	signature:   "Ed" or "ED" | key id (8) | Ed25519 signature (64), base64,
//	             over the message ("Ed", legacy) or its BLAKE2b-512 ("ED",
//	             the default since minisign 0.10)
//	global sig:  Ed25519 signature over signature (64) | trusted comment,
//	             so the trusted comment cannot be swapped

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// IDString is the key id as minisign prints it.
func (k PublicKey) IDString() string {
	b := k.ID
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// Line is the key's base64 line, as in a .pub file.
func (k PublicKey) Line() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), k.ID[:]...), k.Key...))
}

// ErrNoKey: a key file with no key in it (release/minisign.pub's placeholder).
var ErrNoKey = errors.New("no minisign public key")

// ParsePublicKey reads one key's base64 line.
func ParsePublicKey(line string) (PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(b) != 42 || string(b[:2]) != "Ed" {
		return PublicKey{}, fmt.Errorf("not a minisign public key: %.20q", line)
	}
	var k PublicKey
	copy(k.ID[:], b[2:10])
	k.Key = ed25519.PublicKey(bytes.Clone(b[10:]))
	return k, nil
}

// ParseKeys reads a key file: comment lines, then one key line per key (more
// than one while a rotation overlaps). A file with none, or with a line that
// is not a key, is ErrNoKey.
func ParseKeys(file string) ([]PublicKey, error) {
	var keys []PublicKey
	for _, line := range strings.Split(file, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "untrusted comment:") {
			continue
		}
		k, err := ParsePublicKey(line)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoKey, err)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	return keys, nil
}

// Signature is a parsed .minisig file.
type Signature struct {
	Alg       string // "Ed" or "ED" (prehashed)
	KeyID     [8]byte
	Sig       []byte
	Trusted   string // the trusted comment, without its prefix
	GlobalSig []byte
}

// ParseSignature reads a .minisig file's four lines.
func ParseSignature(text string) (*Signature, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment:") {
		return nil, errors.New("not a minisign signature (four lines, the first an untrusted comment)")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(b) != 74 || (string(b[:2]) != "Ed" && string(b[:2]) != "ED") {
		return nil, errors.New("not a minisign signature: bad signature line")
	}
	trusted, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return nil, errors.New("not a minisign signature: no trusted comment")
	}
	g, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(g) != ed25519.SignatureSize {
		return nil, errors.New("not a minisign signature: bad global signature")
	}
	s := &Signature{Alg: string(b[:2]), Sig: bytes.Clone(b[10:]), Trusted: trusted, GlobalSig: g}
	copy(s.KeyID[:], b[2:10])
	return s, nil
}

// Verify checks sig over msg with k: the key id, the signature, and the
// global signature over the trusted comment.
func (k PublicKey) Verify(msg []byte, sig *Signature) error {
	if sig.KeyID != k.ID {
		return fmt.Errorf("signed by key %s, not %s", PublicKey{ID: sig.KeyID}.IDString(), k.IDString())
	}
	m := msg
	if sig.Alg == "ED" {
		h := blake2b.Sum512(msg)
		m = h[:]
	}
	if !ed25519.Verify(k.Key, m, sig.Sig) {
		return errors.New("the signature does not match")
	}
	if !ed25519.Verify(k.Key, append(bytes.Clone(sig.Sig), sig.Trusted...), sig.GlobalSig) {
		return errors.New("the trusted comment's signature does not match")
	}
	return nil
}

// VerifyAny checks sig with whichever of keys it names, and returns that key.
func VerifyAny(msg []byte, sigText string, keys []PublicKey) (PublicKey, error) {
	sig, err := ParseSignature(sigText)
	if err != nil {
		return PublicKey{}, err
	}
	for _, k := range keys {
		if k.ID == sig.KeyID {
			return k, k.Verify(msg, sig)
		}
	}
	return PublicKey{}, fmt.Errorf("signed by key %s, which this build does not trust", PublicKey{ID: sig.KeyID}.IDString())
}

// Sign makes a prehashed ("ED") minisign signature of msg, as `minisign -S`
// does. For tests and for endorsing a next key by hand; release manifests
// are signed with the minisign CLI (make release).
func Sign(priv ed25519.PrivateKey, id [8]byte, msg []byte, trusted string) string {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, h[:])
	global := ed25519.Sign(priv, append(bytes.Clone(sig), trusted...))
	line := base64.StdEncoding.EncodeToString(append(append([]byte("ED"), id[:]...), sig...))
	return "untrusted comment: signature from minisign secret key\n" + line +
		"\ntrusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n"
}
