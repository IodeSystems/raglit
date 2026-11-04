package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real minisign output (from github.com/jedisct1/go-minisign's tests): the
// verifier agrees with the minisign CLI on both signature kinds.
func TestMinisign_RealVectors(t *testing.T) {
	pk, err := ParsePublicKey("RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3")
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []string{
		"untrusted comment: signature from minisign secret key\nRWQf6LRCGA9i59SLOFxz6NxvASXDJeRtuZykwQepbDEGt87ig1BNpWaVWuNrm73YiIiJbq71Wi+dP9eKL8OC351vwIasSSbXxwA=\ntrusted comment: timestamp:1635442742\tfile:test\n0YteLgV960ia80vnA/fHbvkyjl/IoP/HNOCaZfrF0CdhAlp7ok+Tpkya+VpWPX5C/Is3q8a/kEDSY7fBmmgJCg==\n",
		"untrusted comment: signature from minisign secret key\nRUQf6LRCGA9i559r3g7V1qNyJDApGip8MfqcadIgT9CuhV3EMhHoN1mGTkUidF/z7SrlQgXdy8ofjb7bNJJylDOocrCo8KLzZwo=\ntrusted comment: timestamp:1635443258\tfile:test\thashed\n/cj37GK60vryibFn+ftOgbCvW9NKhKYgjVpFFQUcWPAnjO23wrvVDTt7cloNC06maoBli9q6qwZDXXoaxweICQ==\n",
	} {
		if _, err := VerifyAny([]byte("test"), sig, []PublicKey{pk}); err != nil {
			t.Errorf("real signature refused: %v", err)
		}
		if _, err := VerifyAny([]byte("tesT"), sig, []PublicKey{pk}); err == nil {
			t.Error("a signature over other bytes verified")
		}
		// The trusted comment is covered by the global signature.
		forged := strings.Replace(sig, "timestamp:16354", "timestamp:26354", 1)
		if _, err := VerifyAny([]byte("test"), forged, []PublicKey{pk}); err == nil {
			t.Error("a changed trusted comment verified")
		}
	}
	if pk.IDString() != "E7620F1842B4E81F" {
		t.Errorf("key id %s", pk.IDString())
	}
}

// A placeholder is no key: a release build carrying it cannot verify
// anything. The committed cmd/raglit/minisign.pub is one real key, dun's.
func TestParseKeys_Placeholder(t *testing.T) {
	if _, err := ParseKeys("untrusted comment: PLACEHOLDER - not a key.\nPLACEHOLDER\n"); err == nil {
		t.Fatal("the placeholder parsed as a key")
	}
	b, err := os.ReadFile("../../cmd/raglit/minisign.pub")
	if err != nil {
		t.Fatal(err)
	}
	if keys, err := ParseKeys(string(b)); err != nil || len(keys) != 1 {
		t.Fatalf("cmd/raglit/minisign.pub: %d keys, %v", len(keys), err)
	}
	k := newKey(t)
	keys, err := ParseKeys("untrusted comment: minisign public key\n" + k.pub.Line() + "\n")
	if err != nil || len(keys) != 1 || keys[0].ID != k.pub.ID {
		t.Fatalf("a real key file: %v %v", keys, err)
	}
}

type testKey struct {
	pub  PublicKey
	priv ed25519.PrivateKey
}

func newKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	return testKey{PublicKey{ID: id, Key: pub}, priv}
}

func (k testKey) sign(msg []byte) string {
	return Sign(k.priv, k.pub.ID, msg, "timestamp:1\tfile:raglit-manifest.json\thashed")
}

// tarball is a tar.gz with the given entries (name → content); a name
// starting "link:" is a symlink.
func tarball(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if n, ok := strings.CutPrefix(name, "link:"); ok {
			hdr = &tar.Header{Name: n, Linkname: body, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func fileFor(url string, b []byte) File {
	h := sha256.Sum256(b)
	return File{URL: url, SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
}

// server serves path → body; it counts requests.
type server struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newServer(t *testing.T) *server {
	s := &server{files: map[string][]byte{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		b, ok := s.files[r.URL.Path]
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) put(path string, b []byte) string {
	s.mu.Lock()
	s.files[path] = b
	s.mu.Unlock()
	return s.URL + path
}

var fetch = &Fetcher{AllowHTTP: true}

func TestInstall(t *testing.T) {
	srv := newServer(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "mcpshell")
	good := tarball(t, map[string]string{"mcpshell": "#!/bin/sh\necho hi\n", "LICENSE": "MIT"})
	f := fileFor(srv.put("/good.tar.gz", good), good)

	if _, err := fetch.Install(context.Background(), f, "mcpshell", dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	st, _ := os.Stat(dst)
	if err != nil || string(b) != "#!/bin/sh\necho hi\n" || st.Mode().Perm() != 0o755 {
		t.Fatalf("installed %q %v %v", b, st.Mode(), err)
	}

	bad := func(name string, f File) {
		t.Helper()
		os.WriteFile(dst, []byte("old"), 0o755)
		_, err := fetch.Install(context.Background(), f, "mcpshell", dst)
		if err == nil || !IsVerify(err) {
			t.Errorf("%s: want a refused check, got %v", name, err)
		}
		if b, _ := os.ReadFile(dst); string(b) != "old" {
			t.Errorf("%s: the target changed", name)
		}
		left, _ := os.ReadDir(dir)
		if len(left) != 1 {
			t.Errorf("%s: temp files left: %v", name, left)
		}
	}
	wrong := f
	wrong.SHA256 = strings.Repeat("0", 64)
	bad("wrong sha256", wrong)
	trunc := good[:len(good)-10]
	short := fileFor(srv.put("/short.tar.gz", trunc), good) // the manifest's size and hash, a short body
	bad("truncated download", short)
	long := f
	long.Size--
	bad("longer than the manifest", long)
	for name, entries := range map[string]map[string]string{
		"path traversal":  {"../mcpshell": "x"},
		"absolute path":   {"/usr/bin/mcpshell": "x"},
		"nested traverse": {"a/../../mcpshell": "x"},
		"symlink":         {"link:mcpshell": "/bin/sh"},
		"missing":         {"other": "x"},
	} {
		tb := tarball(t, entries)
		bad(name, fileFor(srv.put("/"+strings.ReplaceAll(name, " ", "-"), tb), tb))
	}
	// Plain HTTP is refused outside tests.
	if _, err := (&Fetcher{}).Install(context.Background(), f, "mcpshell", dst); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http download: %v", err)
	}
	// A binary in one directory (name_os_arch/mcpshell) is found.
	nested := tarball(t, map[string]string{"mcpshell_linux_amd64/mcpshell": "nested"})
	if _, err := fetch.Install(context.Background(), fileFor(srv.put("/nested", nested), nested), "mcpshell", dst); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"v0.3.0", "v0.4.0", -1}, {"v1.10.0", "v1.9.9", 1}, {"v1.0.0", "v1.0.0", 0},
		{"v1.0.0-rc1", "v1.0.0", -1}, {"v1.0.0+meta", "v1.0.0", 0},
	} {
		a, _ := ParseVersion(c.a)
		b, _ := ParseVersion(c.b)
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s = %d", c.a, c.b, got)
		}
	}
	for _, s := range []string{"dev", "0.3.0", "v0.3", "v01.2.3"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

// release is a published release on the test server: its archive and a
// signed manifest at /latest/raglit-manifest.json.
type release struct {
	srv     *server
	key     testKey
	version string
	body    string // the raglit binary's content
	mut     func(*Manifest)
}

func (r release) publish(t *testing.T) []byte {
	t.Helper()
	tb := tarball(t, map[string]string{"raglit": r.body})
	url := r.srv.put("/"+r.version+"/raglit_linux_amd64.tar.gz", tb)
	m := Manifest{Schema: Schema, Version: r.version, Published: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Summary: "faster startup", Artifacts: map[string]Artifact{"raglit": {Version: r.version, Files: map[string]File{"linux_amd64": fileFor(url, tb)}}}}
	if r.mut != nil {
		r.mut(&m)
	}
	b, _ := json.Marshal(m)
	r.srv.put("/latest/raglit-manifest.json", b)
	r.srv.put("/latest/raglit-manifest.json.minisig", []byte(r.key.sign(b)))
	return b
}

func newUpdater(srv *server, keys []PublicKey, current, exe, home string) *Updater {
	return &Updater{ManifestURL: srv.URL + "/latest/raglit-manifest.json", Name: "raglit", Keys: keys, Current: current,
		Exe: exe, Home: home, Platform: "linux_amd64", Fetch: fetch}
}

func setup(t *testing.T) (*server, testKey, string, string) {
	srv := newServer(t)
	dir, home := t.TempDir(), t.TempDir()
	exe := filepath.Join(dir, "raglit")
	os.WriteFile(exe, []byte("v0.3.0 binary"), 0o755)
	return srv, newKey(t), exe, home
}

func TestUpdater_StagesANewBuild(t *testing.T) {
	srv, key, exe, home := setup(t)
	release{srv: srv, key: key, version: "v0.4.0", body: "v0.4.0 binary"}.publish(t)
	u := newUpdater(srv, []PublicKey{key.pub}, "v0.3.0", exe, home)
	res, err := u.Run(context.Background())
	if err != nil || !res.Staged || res.To != "v0.4.0" || res.From != "v0.3.0" || res.Summary != "faster startup" {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "v0.4.0 binary" {
		t.Fatalf("exe is %q", b)
	}
	// At most daily: a second run within the day fetches nothing.
	hits := srv.hits["/latest/raglit-manifest.json"]
	if _, err := u.Run(context.Background()); err != ErrNotDue || srv.hits["/latest/raglit-manifest.json"] != hits {
		t.Fatalf("second run: %v, %d fetches", err, srv.hits["/latest/raglit-manifest.json"]-hits)
	}
	// A day later it checks again.
	u.Now = func() time.Time { return time.Now().Add(Interval + time.Minute) }
	u.Current = "v0.4.0"
	if res, err := u.Run(context.Background()); err != nil || res.Staged || !res.Checked {
		t.Fatalf("next day: %+v %v", res, err)
	}
}

func TestUpdater_Refuses(t *testing.T) {
	for _, c := range []struct {
		name    string
		version string // the manifest's
		current string
		seen    State
		sign    func(r *release, other testKey)
		want    string
	}{
		{name: "bad signature", version: "v0.4.0", current: "v0.3.0", sign: func(r *release, other testKey) { r.key = other }, want: "does not trust"},
		{name: "downgrade", version: "v0.2.0", current: "v0.3.0", want: "older than this build"},
		{name: "replay of an older manifest", version: "v0.4.0", current: "v0.3.0", seen: State{Version: "v0.5.0"}, want: "older than v0.5.0 seen before"},
		{name: "replay by date", version: "v0.4.0", current: "v0.3.0", seen: State{Published: "2026-12-01T00:00:00Z"}, want: "before one seen"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, key, exe, home := setup(t)
			r := release{srv: srv, key: key, version: c.version, body: "new"}
			if c.sign != nil {
				c.sign(&r, newKey(t))
			}
			r.publish(t)
			if err := saveState(StateFile(home), c.seen); err != nil {
				t.Fatal(err)
			}
			_, err := newUpdater(srv, []PublicKey{key.pub}, c.current, exe, home).Run(context.Background())
			if err == nil || !IsVerify(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
			if b, _ := os.ReadFile(exe); string(b) != "v0.3.0 binary" {
				t.Fatalf("exe replaced: %q", b)
			}
		})
	}
	// A tampered manifest body under a good signature.
	srv, key, exe, home := setup(t)
	b := release{srv: srv, key: key, version: "v0.4.0", body: "new"}.publish(t)
	srv.put("/latest/raglit-manifest.json", bytes.Replace(b, []byte("v0.4.0"), []byte("v0.9.0"), 1))
	if _, err := newUpdater(srv, []PublicKey{key.pub}, "v0.3.0", exe, home).Run(context.Background()); err == nil || !IsVerify(err) {
		t.Fatalf("tampered manifest: %v", err)
	}
	// The manifest's raglit older than this build, in a newer manifest.
	srv, key, exe, home = setup(t)
	release{srv: srv, key: key, version: "v0.4.0", body: "new", mut: func(m *Manifest) {
		a := m.Artifacts["raglit"]
		a.Version = "v0.2.0"
		m.Artifacts["raglit"] = a
	}}.publish(t)
	if _, err := newUpdater(srv, []PublicKey{key.pub}, "v0.3.0", exe, home).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("downgrade: %v", err)
	}
	// No key (the placeholder): nothing is fetched as trusted.
	if _, err := newUpdater(srv, nil, "v0.3.0", exe, t.TempDir()).Run(context.Background()); err == nil {
		t.Fatal("no key: verified")
	}
}

// Key rotation: release N, signed by K1, endorses K2; release N+1 is signed
// by K2 alone and verifies for a client built with K1 that saw release N. A
// key_next that K1 did not sign is refused.
func TestUpdater_KeyRotation(t *testing.T) {
	srv, k1, exe, home := setup(t)
	k2 := newKey(t)
	release{srv: srv, key: k1, version: "v0.4.0", body: "v0.4.0", mut: func(m *Manifest) {
		m.KeyNext = &Endorsement{Key: k2.pub.Line(), Signature: k1.sign([]byte(k2.pub.Line()))}
	}}.publish(t)
	u := newUpdater(srv, []PublicKey{k1.pub}, "v0.3.0", exe, home)
	if res, err := u.Run(context.Background()); err != nil || !res.Staged {
		t.Fatalf("release N: %+v %v", res, err)
	}
	if st := loadState(StateFile(home)); len(st.Keys) != 1 || st.Keys[0] != k2.pub.Line() {
		t.Fatalf("next key not kept: %+v", st)
	}
	release{srv: srv, key: k2, version: "v0.5.0", body: "v0.5.0", mut: func(m *Manifest) {
		m.Published = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}}.publish(t)
	u.Current, u.Now = "v0.4.0", func() time.Time { return time.Now().Add(Interval + time.Minute) }
	if res, err := u.Run(context.Background()); err != nil || !res.Staged || res.To != "v0.5.0" {
		t.Fatalf("release N+1 signed by the next key: %+v %v", res, err)
	}
	// A client that never saw the endorsement does not trust K2.
	if _, err := newUpdater(srv, []PublicKey{k1.pub}, "v0.4.0", exe, t.TempDir()).Run(context.Background()); err == nil {
		t.Fatal("an unendorsed key verified")
	}
	// An endorsement by another key is refused, the whole manifest with it.
	srv2, k1b, exe2, home2 := setup(t)
	rogue := newKey(t)
	release{srv: srv2, key: k1b, version: "v0.4.0", body: "x", mut: func(m *Manifest) {
		m.KeyNext = &Endorsement{Key: rogue.pub.Line(), Signature: rogue.sign([]byte(rogue.pub.Line()))}
	}}.publish(t)
	if _, err := newUpdater(srv2, []PublicKey{k1b.pub}, "v0.3.0", exe2, home2).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "key_next") {
		t.Fatalf("rogue key_next: %v", err)
	}
}
