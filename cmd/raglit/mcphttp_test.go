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
	h, err := buildGatHandler(reg, lf, raglit.Home(root), 8, nil, raglit.GCPolicy{}, newWatcher(reg, root, 0))
	if err != nil {
		t.Fatal(err)
	}
	prev := http.DefaultTransport
	srv := httptest.NewServer(withMCP(reg, 8, h, mw))
	t.Cleanup(func() { srv.Close(); reg.Close(); http.DefaultTransport = prev })
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	return srv, strings.TrimSpace(string(raw))
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
