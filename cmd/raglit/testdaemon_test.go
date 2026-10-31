package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// A test daemon: the real handler and auth, in-process, on a fresh scoped
// root. Commands under test reach it the way they reach any daemon, over HTTP
// with a token; the test arranges data through the registry directly.

// testToken is every test daemon's admin token. One value, because the client
// transport reads $RAGLIT_TOKEN once per process.
const testToken = "raglit-test-admin-token"

var testAuthOnce sync.Once

// testProject is the project test commands run as.
const testProject = "t"

// testDaemon starts a daemon and returns its base URL, its registry, and the
// flags that point a command at it and at the index "t__default".
func testDaemon(t *testing.T) (base string, reg *raglit.Registry, flags []string) {
	t.Helper()
	testAuthOnce.Do(func() {
		os.Setenv("RAGLIT_TOKEN", testToken)
		installDaemonAuth()
	})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, adminTokenFile), []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mw, _, err := daemonAuth(ctx, reg, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mw(h))
	t.Cleanup(func() { srv.Close(); reg.Close() })
	home := t.TempDir() // an empty project home: no config to pick up
	return srv.URL, reg, []string{"--daemon", srv.URL, "--project", testProject, "--home", home}
}

// testIndex is the store behind testDaemon's default target, for arranging.
func testIndex(t *testing.T, reg *raglit.Registry) *raglit.Store {
	t.Helper()
	st, err := reg.Get(nsIndex(testProject, "default"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// testDB is an index behind a test daemon: path opens it for arranging and
// asserting, flags point a command at it.
type testDB struct {
	path  string
	flags []string
}

func newTestDB(t *testing.T) testDB {
	t.Helper()
	_, reg, flags := testDaemon(t)
	return testDB{path: testIndex(t, reg).Path(), flags: flags}
}

// with is the command line: the target flags, then args.
func (d testDB) with(args ...string) []string {
	return append(append([]string{}, d.flags...), args...)
}
