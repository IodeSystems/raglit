package raglit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func keyRegistry(t *testing.T) (*Registry, *KeyStore) {
	t.Helper()
	reg := aclRegistry(t)
	ks, err := OpenKeyStore(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg.SetKeyStore(ks)
	return reg, ks
}

func TestKeyStore_MintAuthenticateRevokeCascade(t *testing.T) {
	_, ks := keyRegistry(t)
	now := time.Now()
	tok, parent, err := ks.Mint(Key{Kind: KeySession, Issuer: "admin", Write: []string{"p__*@s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Principal != KeyPrincipalPrefix+parent.ID || parent.Hash != "" {
		t.Fatalf("minted key = %+v", parent)
	}
	if k, err := ks.Authenticate(tok, now); err != nil || k.ID != parent.ID {
		t.Fatalf("Authenticate = %+v, %v", k, err)
	}
	if _, err := ks.Authenticate(tok+"x", now); !errors.Is(err, ErrBadKey) {
		t.Fatalf("a wrong secret: %v", err)
	}
	ctok, child, err := ks.Mint(Key{Kind: KeySession, Issuer: parent.Principal, Parent: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !ks.IsAncestor(parent.Principal, child.Principal) || ks.IsAncestor(child.Principal, parent.Principal) {
		t.Fatal("IsAncestor wrong way round")
	}
	// The child's issuer may revoke it; a stranger may not.
	if err := ks.Revoke("someone", "admin", child.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger revoking: %v", err)
	}
	// Revoking the parent revokes the child.
	if err := ks.Revoke("admin", "admin", parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Authenticate(ctok, now); !errors.Is(err, ErrBadKey) {
		t.Fatalf("child of a revoked key: %v, want ErrBadKey", err)
	}
	// The store survives a reopen, holding hashes only.
	ks2, err := OpenKeyStore(ks.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ks2.Authenticate(tok, now); !errors.Is(err, ErrBadKey) {
		t.Fatal("a revoked key came back after reopening")
	}
}

func TestKeyStore_SessionKeysExpireWhenIdle(t *testing.T) {
	_, ks := keyRegistry(t)
	tok, _, _ := ks.Mint(Key{Kind: KeySession, Issuer: "admin"})
	if _, err := ks.Authenticate(tok, time.Now().Add(KeyIdleTTL+time.Hour)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("idle session key: %v, want ErrBadKey", err)
	}
	api, _, _ := ks.Mint(Key{Kind: KeyAPI, Issuer: "admin", Principal: "aw4"})
	if k, err := ks.Authenticate(api, time.Now().Add(365*24*time.Hour)); err != nil || k.Principal != "aw4" {
		t.Fatalf("API token after a year: %+v, %v", k, err)
	}
}

// A session key reads what its issuer may, narrowed by its patterns; writes
// only its session's branches; owns what it creates; and its parent reads
// its child's branch while a sibling session does not.
func TestKeys_SessionKeyRightsThroughTheACL(t *testing.T) {
	reg, ks := keyRegistry(t)
	ctx := context.Background()
	// An admin-private index of the project, and one of another project.
	for _, n := range []string{"p__code", "q__secret"} {
		st, err := reg.Write(as("admin"), n)
		if err != nil {
			t.Fatal(err)
		}
		must(t, st.Ingest(ctx, Document{Path: "file:///" + n, Title: n, Fragments: []Fragment{{Text: "walrus " + n}}}))
	}
	_, s1, _ := ks.Mint(Key{Kind: KeySession, Issuer: "admin",
		Read: []string{"p__*", GlobalPattern}, Write: []string{"p__*@s1"}})
	_, s2, _ := ks.Mint(Key{Kind: KeySession, Issuer: "admin",
		Read: []string{"p__*", GlobalPattern}, Write: []string{"p__*@s2"}})
	k1 := as(s1.Principal)

	if !reg.CanRead(k1, "p__code") {
		t.Fatal("a session key cannot read its project's index its issuer may read")
	}
	if reg.CanRead(k1, "q__secret") {
		t.Fatal("a session key reads outside its Read patterns")
	}
	if _, err := reg.Write(k1, "p__code"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session key writing main: %v, want ErrForbidden", err)
	}
	if _, err := reg.Write(k1, "p__code@s2"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session key writing another session's branch: %v, want ErrForbidden", err)
	}
	br, err := reg.Write(k1, "p__code@s1")
	if err != nil {
		t.Fatal(err)
	}
	must(t, br.Ingest(ctx, Document{Path: "file:///note", Title: "N", Fragments: []Fragment{{Text: "walrus note"}}}))
	if reg.ACLOf("p__code@s1").Owner != s1.Principal {
		t.Fatal("the branch is not the key's own")
	}
	st, err := reg.Read(k1, "p__code@s1")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Search("walrus", 5); len(h) != 2 {
		t.Fatalf("own branch over main: %d hits, want 2", len(h))
	}
	if reg.CanRead(as(s2.Principal), "p__code@s1") {
		t.Fatal("a sibling session reads another session's branch")
	}

	// A child key for one branch: it owns that branch; its parent reads it.
	_, child, _ := ks.Mint(Key{Kind: KeySession, Issuer: s1.Principal, Parent: s1.ID,
		Read: s1.Read, Write: []string{"p__sessions@s1"}})
	if _, err := reg.Write(as(child.Principal), "p__sessions@s1"); err != nil {
		t.Fatal(err)
	}
	if !reg.CanRead(k1, "p__sessions@s1") {
		t.Fatal("a parent key cannot read its child's branch")
	}
	if !reg.CanRead(as(child.Principal), "p__code") {
		t.Fatal("a child key cannot read what its parent may")
	}
}

// The principal that minted a session key sees, and may revoke, the child
// keys that session minted; a stranger sees none of them.
func TestKeyStore_IssuerSeesAndRevokesItsSessionsChildren(t *testing.T) {
	_, ks := keyRegistry(t)
	_, s1, _ := ks.Mint(Key{Kind: KeySession, Issuer: "aw4"})
	_, child, _ := ks.Mint(Key{Kind: KeySession, Issuer: s1.Principal, Parent: s1.ID})
	if got := ks.List("aw4", "admin"); len(got) != 2 {
		t.Fatalf("aw4 sees %d keys, want its session key and that key's child", len(got))
	}
	if got := ks.List("bob", "admin"); len(got) != 0 {
		t.Fatalf("a stranger sees %d keys", len(got))
	}
	if err := ks.Revoke("aw4", "admin", child.ID); err != nil {
		t.Fatalf("aw4 revoking its session's child: %v", err)
	}
}
