package main

import (
	"context"
	"encoding/json"
	flag "github.com/spf13/pflag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/iodesystems/raglit"
)

// mcpTestDaemon is a scoped daemon with real auth and /mcp mounted, holding one
// project index, one other project's index and one index with no project.
func mcpTestDaemon(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	d := newMCPDaemon(t)
	return d.srv, d.admin
}

type mcpDaemon struct {
	srv   *httptest.Server
	admin string
	reg   *raglit.Registry
	pool  *raglit.Pool
	watch *watcher
}

func newMCPDaemon(t *testing.T) *mcpDaemon {
	t.Helper()
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mw, tokenPath, err := daemonAuth(ctx, reg, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	adminTokenPath = tokenPath
	t.Cleanup(func() { adminTokenPath = "" })
	for name, word := range map[string]string{"other__secret": "walrus other", "legacy": "walrus legacy"} {
		st, err := reg.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		must(t, st.Ingest(ctx, raglit.Document{Path: "file:///" + name + ".md", Title: name,
			Fragments: []raglit.Fragment{{Text: word}}}))
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lf := addLLMFlags(fs)
	_ = fs.Parse(nil)
	lf.resolve(raglit.Home(root))
	pool, err := raglit.OpenPool(root)
	if err != nil {
		t.Fatal(err)
	}
	w := newWatcher(reg, root, 0)
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, pool, raglit.GCPolicy{}, w)
	if err != nil {
		t.Fatal(err)
	}
	prev := http.DefaultTransport
	srv := httptest.NewServer(withMCP(reg, 8, h, mw))
	t.Cleanup(func() { srv.Close(); pool.Close(); reg.Close(); http.DefaultTransport = prev })
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	return &mcpDaemon{srv: srv, admin: strings.TrimSpace(string(raw)), reg: reg, pool: pool, watch: w}
}

// seedPooled gives an index a document whose work is in the shared pool.
func seedPooled(t *testing.T, d *mcpDaemon, index, hash string) {
	t.Helper()
	st, err := d.reg.Get(index)
	if err != nil {
		t.Fatal(err)
	}
	p := "file:///" + index + "-" + hash + ".md"
	must(t, st.Ingest(context.Background(), raglit.Document{Path: p, Title: "t", Fragments: []raglit.Fragment{{Text: "seed"}}}))
	must(t, st.SetDocumentHash(p, hash))
	must(t, d.pool.Put("recipe", hash, raglit.PooledDoc{}))
	must(t, os.MkdirAll(d.pool.FileDir(hash), 0o700))
}

func poolHas(t *testing.T, d *mcpDaemon, hash string) bool {
	t.Helper()
	_, ok, err := d.pool.Get("recipe", hash)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func mcpSession(t *testing.T, url, token string) (*mcpclient.Client, error) {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(url,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		return nil, err
	}
	return c, nil
}

// mcpCall returns a tool's text and whether it was a tool error.
func mcpCall(t *testing.T, c *mcpclient.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name, req.Params.Arguments = tool, args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, x := range res.Content {
		if tc, ok := x.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func mintProject(t *testing.T, srv *httptest.Server, admin, name string, create bool, roots ...string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"project": name, "create": create, "roots": roots})
	code, out := doAs(t, "POST", srv.URL+"/api/projects/keys", admin, string(body))
	var r struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	return code, r.Token
}

func TestMCPHTTP_ProjectKeyIsProjectScope(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	code, key := mintProject(t, srv, admin, "acme", true)
	if code != http.StatusOK || key == "" {
		t.Fatalf("create: %d", code)
	}

	c, err := mcpSession(t, srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools")
	}

	// Its own project has nothing yet, and no other project's text shows.
	if out, isErr := mcpCall(t, c, "search", map[string]any{"query": "walrus"}); isErr || strings.Contains(out, "other") || strings.Contains(out, "legacy") {
		t.Errorf("search: err=%v %s", isErr, out)
	}
	if out, _ := mcpCall(t, c, "search", map[string]any{"query": "walrus", "index": "other:secret"}); strings.Contains(out, "walrus") {
		t.Errorf("named another project's index: %s", out)
	}
	if out, _ := mcpCall(t, c, "search", map[string]any{"query": "walrus", "index": ":legacy"}); strings.Contains(out, "walrus") {
		t.Errorf("named a root index: %s", out)
	}
	if out, _ := mcpCall(t, c, "list_indexes", nil); strings.Contains(out, "secret") || strings.Contains(out, "legacy") {
		t.Errorf("list_indexes: %s", out)
	}

	// Another project's URL does not exist for this key, and no key is no entry.
	if _, err := mcpSession(t, srv.URL+"/mcp/other", key); err == nil {
		t.Error("a key opened another project's URL")
	}
	if _, err := mcpSession(t, srv.URL+"/mcp/root", key); err == nil {
		t.Error("a project key opened /mcp/root")
	}
	if _, err := mcpSession(t, srv.URL+"/mcp/acme", "wrong"); err == nil {
		t.Error("a wrong token opened a project")
	}
}

// The handler stats paths before the daemon's admin check, so the refusal has to
// come first or a key could ask which files exist.
func TestMCPHTTP_ProjectKeyCannotProbeHostFiles(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	_, key := mintProject(t, srv, admin, "acme", true)
	c, err := mcpSession(t, srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/passwd", "/no/such/file"} {
		out, isErr := mcpCall(t, c, "ingest", map[string]any{"paths": []string{p}})
		if !isErr || strings.Contains(out, "missing") {
			t.Errorf("ingest %s: err=%v %s", p, isErr, out)
		}
		if out, isErr := mcpCall(t, c, "ocr", map[string]any{"path": p}); !isErr || !strings.Contains(out, "upload") {
			t.Errorf("ocr %s: err=%v %s", p, isErr, out)
		}
	}
}

func TestMCPHTTP_RootIsTheIndexesWithNoProject(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	c, err := mcpSession(t, srv.URL+"/mcp/root", admin)
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := mcpCall(t, c, "search", map[string]any{"query": "walrus"})
	if isErr || !strings.Contains(out, "legacy") || strings.Contains(out, "other") {
		t.Errorf("search all on root, as admin: err=%v %s", isErr, out)
	}
	if out, _ := mcpCall(t, c, "search", map[string]any{"query": "walrus", "index": "other__secret"}); strings.Contains(out, "walrus") {
		t.Errorf("root reached a project index: %s", out)
	}
	if out, _ := mcpCall(t, c, "search", map[string]any{"query": "walrus", "index": "*"}); strings.Contains(out, "walrus") {
		t.Errorf("root wildcard: %s", out)
	}
	if out, _ := mcpCall(t, c, "list_indexes", nil); !strings.Contains(out, "legacy") || strings.Contains(out, "secret") {
		t.Errorf("list_indexes: %s", out)
	}
}

func TestProjectKeys_Rules(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	for _, c := range []struct {
		name   string
		create bool
		want   int
	}{
		{"root", true, http.StatusBadRequest},
		{"a__b", true, http.StatusBadRequest},
		{"other", true, http.StatusConflict}, // has indexes already
		{"nobody", false, http.StatusNotFound},
		{"acme", true, http.StatusOK},
		{"acme", true, http.StatusConflict}, // has a key
		{"acme", false, http.StatusOK},
	} {
		if code, _ := mintProject(t, srv, admin, c.name, c.create); code != c.want {
			t.Errorf("project %q create=%v: %d, want %d", c.name, c.create, code, c.want)
		}
	}
	_, key := mintProject(t, srv, admin, "bee", true)
	if code, _ := mintProject(t, srv, key, "bee", false); code != http.StatusForbidden {
		t.Errorf("a project key minted a key: %d", code)
	}
}

func TestResolveProject_RootIsReserved(t *testing.T) {
	if _, err := resolveProject("root", func() raglit.Home { return raglit.Home(t.TempDir()) }); err == nil {
		t.Error("--project root was accepted")
	}
}

func TestMCPHTTP_CreateUploadForgetDrop(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	_, key := mintProject(t, srv, admin, "acme", true)
	c, err := mcpSession(t, srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "create", "index": "notes", "hint": "RO means repair order.", "caption_profile": "docs"}); isErr {
		t.Fatalf("create: %s", out)
	}
	out, _ := mcpCall(t, c, "list_indexes", nil)
	if !strings.Contains(out, `"notes"`) {
		t.Errorf("created index not listed: %s", out)
	}
	if out, isErr := mcpCall(t, c, "upload", map[string]any{"index": "notes", "name": "a/b.md", "text": "# B\nwalrus"}); isErr || !strings.Contains(out, "job_id") || !strings.Contains(out, `"index":"notes"`) {
		t.Errorf("upload: err=%v %s", isErr, out)
	}
	if out, isErr := mcpCall(t, c, "upload", map[string]any{"index": "../x", "name": "a.md", "text": "x"}); !isErr && strings.Contains(out, "other") {
		t.Errorf("upload escaped: %s", out)
	}
	if out, isErr := mcpCall(t, c, "upload", map[string]any{"index": "other:secret", "name": "a.md", "text": "x"}); !isErr {
		t.Errorf("uploaded into another project: %s", out)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "drop", "index": "notes"}); !isErr {
		t.Errorf("drop without confirm: %s", out)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "drop", "index": "notes", "confirm": true}); isErr {
		t.Errorf("drop: %s", out)
	}
	if out, _ := mcpCall(t, c, "list_indexes", nil); strings.Contains(out, `"notes"`) {
		t.Errorf("dropped index still listed: %s", out)
	}
}

// A key watches and ingests where it was granted and nowhere else, however the
// path is spelled.
func TestMCPHTTP_WatchNeedsARoot(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	granted, outside := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(granted+"/a.md", []byte("# A\nwalrus"), 0o600))
	must(t, os.WriteFile(outside+"/x.md", []byte("# X\nsecret"), 0o600))
	must(t, os.Symlink(outside, granted+"/link"))
	_, key := mintProject(t, srv, admin, "acme", true, granted)
	c, err := mcpSession(t, srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outside, granted + "/link", granted + "/../" + strings.TrimPrefix(outside, "/")} {
		if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "watch", "dir": dir}); !isErr {
			t.Errorf("watched %s: %s", dir, out)
		}
	}
	if out, isErr := mcpCall(t, c, "ingest", map[string]any{"paths": []string{granted + "/link/x.md"}}); !isErr {
		t.Errorf("ingested through a link: %s", out)
	}
	if out, isErr := mcpCall(t, c, "ingest", map[string]any{"url": "http://169.254.169.254/"}); !isErr {
		t.Errorf("a key made the daemon fetch a URL: %s", out)
	}
	if out, isErr := mcpCall(t, c, "ingest", map[string]any{"paths": []string{granted + "/a.md"}}); isErr {
		t.Errorf("ingest under a root: %s", out)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "watch", "dir": granted}); isErr {
		t.Fatalf("watch: %s", out)
	}
	out, _ := mcpCall(t, c, "manage_project", map[string]any{"action": "watches"})
	if !strings.Contains(out, granted) || !strings.Contains(out, `"index":"default"`) {
		t.Errorf("watches: %s", out)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "unwatch", "dir": granted}); isErr {
		t.Errorf("unwatch: %s", out)
	}
	if out, _ := mcpCall(t, c, "manage_project", map[string]any{"action": "watches"}); strings.Contains(out, granted) {
		t.Errorf("still watched: %s", out)
	}
}

// A rotated key reaches what the old key made, and the old key stops.
// The scan runs with no key to check, so the directory itself is the limit.
func TestWatchDir_DoesNotFollowALinkOut(t *testing.T) {
	root := t.TempDir()
	reg, err := raglit.OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	dir, outside := t.TempDir(), t.TempDir()
	must(t, os.WriteFile(dir+"/a.md", []byte("# A"), 0o600))
	must(t, os.WriteFile(outside+"/x.md", []byte("# X"), 0o600))
	must(t, os.Symlink(outside, dir+"/link"))
	must(t, os.Symlink(outside+"/x.md", dir+"/file-link.md"))
	if _, err := reg.Get("notes"); err != nil {
		t.Fatal(err)
	}
	if enq, _ := newWatcher(reg, root, 0).scanDir(dirWatch{Index: "notes", Dir: dir}); enq != 1 {
		t.Errorf("enqueued %d, want only a.md", enq)
	}
}

func TestProjectKey_RotationKeepsTheIndexes(t *testing.T) {
	srv, admin := mcpTestDaemon(t)
	_, first := mintProject(t, srv, admin, "acme", true)
	c, err := mcpSession(t, srv.URL+"/mcp/acme", first)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "create", "index": "notes"}); isErr {
		t.Fatal(out)
	}
	_, second := mintProject(t, srv, admin, "acme", false)
	id := strings.Split(strings.TrimPrefix(first, "rk_"), "_")[0]
	if code, body := doAs(t, "POST", srv.URL+"/api/keys/revoke", admin, `{"id":"`+id+`"}`); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if _, err := mcpSession(t, srv.URL+"/mcp/acme", first); err == nil {
		t.Error("a revoked key still opens the project")
	}
	c2, err := mcpSession(t, srv.URL+"/mcp/acme", second)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := mcpCall(t, c2, "list_indexes", nil); !strings.Contains(out, `"notes"`) {
		t.Errorf("the new key lost the index: %s", out)
	}
}

// `raglit mcp` hands its tools to a model; a lifecycle tool there reaches dun's
// model past the names dun disables.
func TestStdioTools_HaveNoLifecycleTools(t *testing.T) {
	s := server.NewMCPServer("t", "0")
	addRaglitTools(s, daemonToolHandlers("http://x", 8, "p", nil))
	for _, name := range []string{"upload", "manage_project"} {
		if s.GetTool(name) != nil {
			t.Errorf("stdio serves %s", name)
		}
	}
}

// With no project a stdio client reads and refuses writes, --manage or not.
func TestManageTools_NoProjectWritesNothing(t *testing.T) {
	s := server.NewMCPServer("t", "0")
	cv := clientView{cwd: "/somewhere"}
	addManageTools(s, "http://127.0.0.1:1", cv)
	if s.GetTool("upload") == nil || s.GetTool("manage_project") == nil {
		t.Fatal("tools missing")
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "a.md", "text": "x", "action": "create"}
	for name, h := range map[string]server.ToolHandlerFunc{
		"upload": uploadHandler("http://127.0.0.1:1", cv), "manage_project": manageProjectHandler("http://127.0.0.1:1", cv),
	} {
		res, err := h(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(mcp.TextContent).Text
		if !res.IsError || !strings.Contains(text, "no project") {
			t.Errorf("%s with no project: %s", name, text)
		}
	}
}

// `vault close --destroy` has to mean it: the text must not survive in the
// pool, and a file another project also holds must survive for that project.
func TestDestroyProject_LeavesNothingOfItsOwn(t *testing.T) {
	d := newMCPDaemon(t)
	_, key := mintProject(t, d.srv, d.admin, "acme", true, t.TempDir())
	c, err := mcpSession(t, d.srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "create", "index": "notes"}); isErr {
		t.Fatal(out)
	}
	seedPooled(t, d, "acme__notes", "onlyacme")
	seedPooled(t, d, "acme__notes", "shared")
	seedPooled(t, d, "other__secret", "shared")
	must(t, d.watch.AddDir("acme__notes", t.TempDir()))

	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "destroy"}); !isErr {
		t.Errorf("destroy without confirm: %s", out)
	}
	out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "destroy", "confirm": true})
	if isErr || !strings.Contains(out, `"pool_entries":1`) {
		t.Fatalf("destroy: %s", out)
	}
	if poolHas(t, d, "onlyacme") {
		t.Error("the project's own document stayed in the pool")
	}
	if _, err := os.Stat(d.pool.FileDir("onlyacme")); !os.IsNotExist(err) {
		t.Error("its page images stayed")
	}
	if !poolHas(t, d, "shared") {
		t.Error("another project's document left the pool")
	}
	if len(d.watch.Dirs()) != 0 {
		t.Errorf("watches left: %v", d.watch.Dirs())
	}
	for _, n := range d.reg.Names() {
		if strings.HasPrefix(n, "acme__") {
			t.Errorf("index left: %s", n)
		}
	}
	if _, err := mcpSession(t, d.srv.URL+"/mcp/acme", key); err == nil {
		t.Error("the destroyed project's key still works")
	}
	// The name is free again.
	if code, _ := mintProject(t, d.srv, d.admin, "acme", true); code != http.StatusOK {
		t.Errorf("recreate: %d", code)
	}
}

func TestDestroyProject_NeedsThatProjectOrTheAdmin(t *testing.T) {
	d := newMCPDaemon(t)
	_, a := mintProject(t, d.srv, d.admin, "aaa", true)
	mintProject(t, d.srv, d.admin, "bbb", true)
	if code, _ := doAs(t, "DELETE", d.srv.URL+"/api/projects?project=bbb", a, ""); code != http.StatusForbidden {
		t.Errorf("a key destroyed another project: %d", code)
	}
	if code, _ := doAs(t, "DELETE", d.srv.URL+"/api/projects?project=root", d.admin, ""); code != http.StatusBadRequest {
		t.Errorf("root: %d", code)
	}
	if code, _ := doAs(t, "DELETE", d.srv.URL+"/api/projects?project=bbb", d.admin, ""); code != http.StatusOK {
		t.Errorf("admin: %d", code)
	}
}

// A watch left behind would recreate the index on its next scan.
func TestDropIndex_StopsItsWatches(t *testing.T) {
	d := newMCPDaemon(t)
	dir := t.TempDir()
	_, key := mintProject(t, d.srv, d.admin, "acme", true, dir)
	must(t, os.WriteFile(dir+"/a.md", []byte("# A"), 0o600))
	c, err := mcpSession(t, d.srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "watch", "index": "notes", "dir": dir}); isErr {
		t.Fatal(out)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "drop", "index": "notes", "confirm": true}); isErr {
		t.Fatal(out)
	}
	d.watch.scanAll()
	if out, _ := mcpCall(t, c, "list_indexes", nil); strings.Contains(out, `"notes"`) {
		t.Errorf("a dropped index came back: %s", out)
	}
}

// A revoked project stops reading the host, and comes back with a new key.
func TestWatch_StopsWithItsLastKey(t *testing.T) {
	d := newMCPDaemon(t)
	dir := t.TempDir()
	must(t, os.WriteFile(dir+"/a.md", []byte("# A"), 0o600))
	_, key := mintProject(t, d.srv, d.admin, "acme", true, dir)
	c, err := mcpSession(t, d.srv.URL+"/mcp/acme", key)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := mcpCall(t, c, "manage_project", map[string]any{"action": "watch", "dir": dir}); isErr {
		t.Fatal(out)
	}
	id := strings.Split(strings.TrimPrefix(key, "rk_"), "_")[0]
	if code, body := doAs(t, "POST", d.srv.URL+"/api/keys/revoke", d.admin, `{"id":"`+id+`"}`); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	must(t, os.WriteFile(dir+"/b.md", []byte("# B"), 0o600))
	w := dirWatch{Index: "acme__default", Dir: dir}
	if enq, _ := d.watch.scanDir(w); enq != 0 {
		t.Errorf("scanned %d files with no key", enq)
	}
	if code, _ := mintProject(t, d.srv, d.admin, "acme", false, dir); code != http.StatusOK {
		t.Fatalf("recover: %d", code)
	}
	if enq, _ := d.watch.scanDir(w); enq == 0 {
		t.Error("a new key did not resume the watch")
	}
}

// docs/mcp-contract.md. dun and para read these; a failure here is a change to them.
func TestContract(t *testing.T) {
	d := newMCPDaemon(t)
	c, err := mcpSession(t, d.srv.URL+"/mcp/other", d.admin)
	if err != nil {
		t.Fatal(err)
	}
	object := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("not JSON: %s", s)
		}
		return m
	}
	has := func(what string, m map[string]any, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s lacks %q: %v", what, k, m)
			}
		}
	}
	out, _ := mcpCall(t, c, "search", map[string]any{"query": "walrus"})
	hits, _ := object(out)["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("no hits: %s", out)
	}
	hit := hits[0].(map[string]any)
	has("hit", hit, "doc_id", "index", "title", "score", "snippet", "rel", "page", "matched_terms", "query_terms")
	if hit["index"] != "secret" {
		t.Errorf("index not in the client's names: %v", hit["index"])
	}

	out, _ = mcpCall(t, c, "list_indexes", nil)
	rows, _ := object(out)["indexes"].([]any)
	if len(rows) == 0 {
		t.Fatalf("no indexes: %s", out)
	}
	has("index row", rows[0].(map[string]any), "name", "meaning")

	out, _ = mcpCall(t, c, "get_document", map[string]any{"path": hit["doc_id"], "max_chars": 2})
	has("document", object(out), "index", "path", "title", "pages", "text", "truncated")

	out, _ = mcpCall(t, c, "list_documents", nil)
	docs, _ := object(out)["documents"].([]any)
	if len(docs) == 0 {
		t.Fatalf("no documents: %s", out)
	}
	has("document row", docs[0].(map[string]any), "index", "path", "title")
}

// An index a project key created is in everyone's project list: the admin's
// as a readable index, another key's as metadata only, with counts and queue
// but no way in.
func TestListProjects_ProjectIndexIsListedAsMetadataToOthers(t *testing.T) {
	d := newMCPDaemon(t)
	mintProject(t, d.srv, d.admin, "acme", true)
	_, other := mintProject(t, d.srv, d.admin, "bbb", true)
	st, err := d.reg.Write(raglit.WithPrincipal(context.Background(), "project:acme"), "acme__notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("file:///a.md", ""); err != nil {
		t.Fatal(err)
	}

	row := func(tok string) projectIndexRow {
		t.Helper()
		code, out := doAs(t, "GET", d.srv.URL+"/api/projects", tok, "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/projects: %d %s", code, out)
		}
		var got struct{ Projects []projectRow }
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		for _, p := range got.Projects {
			for _, i := range p.Indexes {
				if i.Name == "acme__notes" {
					return i
				}
			}
		}
		t.Fatalf("acme__notes is not listed: %s", out)
		return projectIndexRow{}
	}
	if r := row(d.admin); !r.Readable || r.Owner != "project:acme" || r.Pending != 1 || r.Bytes <= 0 {
		t.Errorf("admin's row: %+v", r)
	}
	if r := row(other); r.Readable || r.Owner != "project:acme" || r.Pending != 1 {
		t.Errorf("another key's row: %+v", r)
	}
	// The admin opens it; the same request from the other key does not.
	if code, out := doAs(t, "GET", d.srv.URL+"/api/documents?index=acme__notes", d.admin, ""); code != http.StatusOK {
		t.Errorf("admin listing the index's documents: %d %s", code, out)
	}
	if code, _ := doAs(t, "GET", d.srv.URL+"/api/documents?index=acme__notes", other, ""); code == http.StatusOK {
		t.Error("another key opened the index it may only see as metadata")
	}
}
