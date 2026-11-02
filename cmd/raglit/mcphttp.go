package main

// MCP over HTTP: /mcp/<project> on the daemon, authenticated by a key.
//
// A client names a URL and a bearer token and runs nothing. `raglit project
// create` prints both. The key decides what the caller reaches; the URL only
// says which project it means, so a key on another project's URL is "no such
// project", as the ACL answers for an index.
//
// The tools are the ones `raglit mcp` serves, proxies to the daemon's own HTTP
// surface. Those proxies call http.Get with a base URL and carry no caller, so
// each request gets a base that names its token, and mcpLoop answers that base
// in process through the full handler. Auth and the ACL therefore see a tool
// call as they see any other request.
//
// Stateless: the server is built per request and keeps no session, so a daemon
// restart costs a client nothing but the requests in flight.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/iodesystems/raglit"
)

const mcpLoopScheme = "raglit-loop"

// mcpLoop routes raglit-loop:// requests to the daemon's handler and everything
// else to the transport it wraps.
type mcpLoop struct {
	base http.RoundTripper
	next http.Handler

	mu     sync.Mutex
	tokens map[string]string // handle → bearer token
}

// open returns a base URL that acts with token until the returned close.
func (l *mcpLoop) open(token string) (string, func()) {
	handle := "h" + raglit.RandomHex(8)
	l.mu.Lock()
	l.tokens[handle] = token
	l.mu.Unlock()
	return mcpLoopScheme + "://" + handle, func() {
		l.mu.Lock()
		delete(l.tokens, handle)
		l.mu.Unlock()
	}
}

func (l *mcpLoop) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != mcpLoopScheme {
		return l.base.RoundTrip(req)
	}
	l.mu.Lock()
	token, ok := l.tokens[req.URL.Host]
	l.mu.Unlock()
	if !ok {
		return nil, http.ErrUseLastResponse
	}
	in := req.Clone(req.Context())
	in.Header.Set("Authorization", "Bearer "+token)
	in.RequestURI = req.URL.RequestURI()
	in.RemoteAddr = "127.0.0.1:0"
	in.Host = "raglit" // the handle is not for error bodies, which echo the host
	if in.Body == nil {
		in.Body = http.NoBody
	}
	rec := &mcpRecorder{header: http.Header{}, status: http.StatusOK}
	l.next.ServeHTTP(rec, in)
	return &http.Response{
		Status: http.StatusText(rec.status), StatusCode: rec.status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: rec.header, Body: io.NopCloser(&rec.body), Request: req,
		ContentLength: int64(rec.body.Len()),
	}, nil
}

// mcpRecorder is the response of one in-process request.
type mcpRecorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *mcpRecorder) Header() http.Header { return r.header }

func (r *mcpRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *mcpRecorder) Write(p []byte) (int, error) {
	r.wrote = true
	return r.body.Write(p)
}

// withMCP is the daemon's handler: inner with /mcp/<project> beside it, both
// behind auth. It also installs the loop, once, at daemon start.
func withMCP(reg *raglit.Registry, defLimit int, inner http.Handler, auth func(http.Handler) http.Handler) http.Handler {
	loop := &mcpLoop{base: http.DefaultTransport, tokens: map[string]string{}}
	http.DefaultTransport = loop
	mcpH := mcpHandler(reg, defLimit, loop)
	full := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := strings.CutPrefix(r.URL.Path, "/mcp/"); ok {
			mcpH.ServeHTTP(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	loop.next = full
	return full
}

func mcpHandler(reg *raglit.Registry, defLimit int, loop *mcpLoop) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			// A browser session passed auth. A tool call acts with a token.
			w.Header().Set("WWW-Authenticate", `Bearer realm="raglit"`)
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		raw := strings.TrimPrefix(r.URL.Path, "/mcp/")
		project := raglit.NormalizeIndexName(raw)
		if raw == "" || project != raw || !mcpProjectAllowed(r.Context(), reg, project) {
			http.Error(w, "no such project", http.StatusNotFound)
			return
		}
		cv := clientView{ns: project}
		if project == raglit.RootProject {
			cv = clientView{root: true, rootNames: reg.Names}
		}
		base, closeLoop := loop.open(strings.TrimSpace(token))
		defer closeLoop()
		h := daemonToolHandlersFor(base, defLimit, cv)
		h.ingest = guardIngestPaths(reg, h.ingest)
		h.ocr = guardOCRPath(reg, h.ocr)
		s := server.NewMCPServer("raglit", version)
		addRaglitTools(s, h)
		// Here and not in addRaglitTools: `raglit mcp` hands its tools to a
		// model, and dun disables by name, so a new tool there would reach it.
		s.AddTool(uploadTool(), uploadHandler(base, cv))
		s.AddTool(manageProjectTool(), manageProjectHandler(base, cv))
		server.NewStreamableHTTPServer(s, server.WithStateLess(true)).ServeHTTP(w, r)
	})
}

// mcpProjectAllowed reports whether the caller's key reaches project. A
// principal with no key (the admin, an OIDC user) is left to the ACL.
func mcpProjectAllowed(ctx context.Context, reg *raglit.Registry, project string) bool {
	k, ok := reg.Keys().ByPrincipal(raglit.PrincipalFrom(ctx))
	if !ok {
		return true
	}
	name := nsIndex(project, "default")
	if project == raglit.RootProject {
		name = "default"
	}
	return k.AllowsRead(name)
}

const hostPathRefusal = ": this reads a file on the daemon's host that this key may not read; send the document with upload"

// guardIngestPaths answers before the handler runs: the handler stats paths
// here, which would tell a caller without the right which files exist. A URL
// is refused the same way, so a key cannot make the daemon fetch for it.
func guardIngestPaths(reg *raglit.Registry, next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		paths := req.GetStringSlice("paths", nil)
		if u := req.GetString("url", ""); u != "" {
			paths = append(paths, u)
		}
		for _, p := range paths {
			if !hostPathAllowed(ctx, reg, p) {
				return mcp.NewToolResultError("ingest" + hostPathRefusal), nil
			}
		}
		return next(ctx, req)
	}
}

func guardOCRPath(reg *raglit.Registry, next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if p := req.GetString("path", ""); p != "" && requireAdmin(ctx, reg) != nil {
			return mcp.NewToolResultError("ocr" + hostPathRefusal), nil
		}
		return next(ctx, req)
	}
}
