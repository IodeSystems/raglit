package raglit

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// API tokens and session keys (USER 2026-10-30; docs/design/2026-10-30-always-daemon.md).
//
// Two kinds in one store, kept as hashes in <root>/keys.json (0600):
//
//   - An API token is a long-lived credential for a principal that is not a
//     person, such as "aw4". The admin mints it. It acts as that principal
//     through the ACL, like an OIDC user, and never expires.
//   - A session key is minted by a principal for one session. It acts as its
//     own principal, "key#<id>", so what it creates (its session branches) is
//     its own. It reads what its issuer may read, narrowed by its Read
//     patterns, and writes only names its Write patterns allow. It expires
//     KeyIdleTTL after its last use.
//
// A session key can mint a CHILD key for one branch (a sub-agent). The child
// owns that branch; its ancestors can still read it and revoke it, and
// revoking a key revokes every key under it.
//
// A token is "rk_<id>_<secret>". Only sha256(token) is stored, so the file
// grants nothing to whoever reads it.

// KeyIdleTTL is how long a session key lives past its last use: the same 7
// days a session branch outlives its last write.
const KeyIdleTTL = 7 * 24 * time.Hour

// KeyPrincipalPrefix marks a session key's own principal.
const KeyPrincipalPrefix = "key#"

// GlobalPattern in a Read list matches every index with no project
// namespace ("code", not "proj__code").
const GlobalPattern = ":global"

const (
	KeyAPI     = "api"
	KeySession = "session"
)

// Key is one stored credential. Hash is never returned to a caller.
type Key struct {
	ID        string   `json:"id"`
	Hash      string   `json:"hash,omitempty"`
	Kind      string   `json:"kind"`
	Principal string   `json:"principal"`
	Issuer    string   `json:"issuer"`
	Parent    string   `json:"parent,omitempty"`
	Read      []string `json:"read,omitempty"`
	Write     []string `json:"write,omitempty"`
	Label     string   `json:"label,omitempty"`
	CreatedAt int64    `json:"created_at"`
	LastUsed  int64    `json:"last_used,omitempty"`
	Revoked   bool     `json:"revoked,omitempty"`
}

// ErrBadKey is a token that is unknown, revoked, expired or malformed.
var ErrBadKey = errors.New("raglit: unknown, revoked or expired key")

// KeyStore holds the keys. Safe for concurrent use.
type KeyStore struct {
	path string

	mu    sync.Mutex
	keys  map[string]*Key // by ID
	dirty time.Time       // last LastUsed change not yet written
	saved time.Time
}

// OpenKeyStore loads (or starts) the store at path.
func OpenKeyStore(p string) (*KeyStore, error) {
	ks := &KeyStore{path: p, keys: map[string]*Key{}}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return ks, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Key
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("raglit: %s: %w", p, err)
	}
	for _, k := range list {
		ks.keys[k.ID] = k
	}
	return ks, nil
}

// saveLocked writes the store; the caller holds ks.mu.
func (ks *KeyStore) saveLocked() error {
	list := make([]*Key, 0, len(ks.keys))
	for _, k := range ks.keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ks.path), 0o755); err != nil {
		return err
	}
	tmp := ks.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	ks.saved, ks.dirty = time.Now(), time.Time{}
	return os.Rename(tmp, ks.path)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}

// Mint stores k (ID, Hash and CreatedAt are filled in) and returns its token,
// the only time the token exists in the clear.
func (ks *KeyStore) Mint(k Key) (string, Key, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k.ID = randomHex(8)
	token := "rk_" + k.ID + "_" + randomHex(24)
	k.Hash = hashToken(token)
	k.CreatedAt = time.Now().Unix()
	if k.Kind == KeySession {
		k.Principal = KeyPrincipalPrefix + k.ID
	}
	ks.keys[k.ID] = &k
	if err := ks.saveLocked(); err != nil {
		delete(ks.keys, k.ID)
		return "", Key{}, err
	}
	out := k
	out.Hash = ""
	return token, out, nil
}

// Authenticate returns the key a token belongs to, refusing one that is
// revoked (itself or any ancestor) or, for a session key, idle past
// KeyIdleTTL. Its last use is recorded.
func (ks *KeyStore) Authenticate(token string, now time.Time) (Key, error) {
	rest, ok := strings.CutPrefix(token, "rk_")
	id, _, ok2 := strings.Cut(rest, "_")
	if !ok || !ok2 {
		return Key{}, ErrBadKey
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k := ks.keys[id]
	if k == nil || subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(k.Hash)) != 1 {
		return Key{}, ErrBadKey
	}
	if !ks.liveLocked(k, now) {
		return Key{}, ErrBadKey
	}
	k.LastUsed = now.Unix()
	if ks.dirty.IsZero() {
		ks.dirty = now
	}
	// Last use is written at most once a minute: it decides expiry in days,
	// and a write per request would be a write per search.
	if now.Sub(ks.saved) > time.Minute {
		_ = ks.saveLocked()
	}
	out := *k
	out.Hash = ""
	return out, nil
}

// liveLocked reports whether k and every ancestor are unrevoked and, for a
// session key, within KeyIdleTTL of last use (or of minting).
func (ks *KeyStore) liveLocked(k *Key, now time.Time) bool {
	for depth := 0; k != nil && depth < 32; depth++ {
		if k.Revoked {
			return false
		}
		if k.Kind == KeySession {
			last := k.LastUsed
			if last == 0 {
				last = k.CreatedAt
			}
			if now.Sub(time.Unix(last, 0)) > KeyIdleTTL {
				return false
			}
		}
		if k.Parent == "" {
			return true
		}
		k = ks.keys[k.Parent]
	}
	return false
}

// ByPrincipal returns the live session key acting as p ("key#<id>").
func (ks *KeyStore) ByPrincipal(p string) (Key, bool) {
	id, ok := strings.CutPrefix(p, KeyPrincipalPrefix)
	if !ok || ks == nil {
		return Key{}, false
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k := ks.keys[id]
	if k == nil || !ks.liveLocked(k, time.Now()) {
		return Key{}, false
	}
	return *k, true
}

// IsAncestor reports whether principal anc is a session key above key
// principal p (its parent, its parent's parent, ...).
func (ks *KeyStore) IsAncestor(anc, p string) bool {
	if ks == nil || !strings.HasPrefix(anc, KeyPrincipalPrefix) {
		return false
	}
	id, ok := strings.CutPrefix(p, KeyPrincipalPrefix)
	if !ok {
		return false
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k := ks.keys[id]
	for depth := 0; k != nil && k.Parent != "" && depth < 32; depth++ {
		if KeyPrincipalPrefix+k.Parent == anc {
			return true
		}
		k = ks.keys[k.Parent]
	}
	return false
}

// Revoke revokes key id and, through Authenticate's ancestor check, every
// key under it. by must be the admin, the key's issuer, or an ancestor key.
func (ks *KeyStore) Revoke(by, admin, id string) error {
	ks.mu.Lock()
	k := ks.keys[id]
	if k == nil {
		ks.mu.Unlock()
		return ErrBadKey
	}
	issuer, principal := k.Issuer, k.Principal
	ks.mu.Unlock()
	if by == "" || (by != admin && by != issuer && !ks.IsAncestor(by, principal)) {
		return ErrForbidden
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k.Revoked = true
	return ks.saveLocked()
}

// List returns the keys by may see: every key for the admin, else the keys it
// issued and the keys under them. Hashes are left out.
func (ks *KeyStore) List(by, admin string) []Key {
	ks.mu.Lock()
	all := make([]Key, 0, len(ks.keys))
	for _, k := range ks.keys {
		c := *k
		c.Hash = ""
		all = append(all, c)
	}
	ks.mu.Unlock()
	var out []Key
	for _, k := range all {
		if by == admin || k.Issuer == by || ks.IsAncestor(by, k.Principal) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Flush writes any pending last-use times.
func (ks *KeyStore) Flush() error {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if ks.dirty.IsZero() {
		return nil
	}
	return ks.saveLocked()
}

// AllowsRead reports whether k's Read patterns admit index name (no patterns:
// every index its issuer may read).
func (k Key) AllowsRead(name string) bool { return keyPatternsMatch(k.Read, name) }

// AllowsWrite reports whether k's Write patterns admit index name (no
// patterns: whatever the ACL lets its principal write).
func (k Key) AllowsWrite(name string) bool { return keyPatternsMatch(k.Write, name) }

func keyPatternsMatch(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	name = normalizeIndexName(name)
	for _, p := range patterns {
		if p == GlobalPattern {
			if !strings.Contains(name, "__") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}
