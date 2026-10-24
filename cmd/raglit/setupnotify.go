package main

import (
	"context"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// setupLogger is the notifications/message logger for the setup summary, named
// like poly-lsp-mcp's "poly-lsp-mcp/setup" so a client (dun) handles both the
// same way.
const setupLogger = "raglit/setup"

// setupData is the "raglit/setup" payload: the summary, and how many
// capabilities are off.
type setupData struct {
	setupSummary
	Missing int `json:"missing"`
}

// coreCapabilities are the ones whose absence the notification raises to a
// warning. The rest (audio, image search, figure descriptions, captions) are
// additions a user may well not want, and warning about them on every start
// would teach a client to ignore the level.
var coreCapabilities = map[string]bool{
	"keyword search over text, markdown and code": true,
	"semantic and hybrid search":                  true,
	"PDF text":                                    true,
	"scanned pages and photos":                    true,
}

// setupNotification is the notifications/message params for s.
func setupNotification(s setupSummary) map[string]any {
	d := setupData{setupSummary: s}
	level := "info"
	for _, c := range s.Capabilities {
		if !c.On {
			d.Missing++
			if coreCapabilities[c.Name] {
				level = "warning"
			}
		}
	}
	return map[string]any{"level": level, "logger": setupLogger, "data": d}
}

// notifySetup sends the setup summary once per session, when the client says
// it is initialized: a notification before that is one the client may drop.
// summary runs off the handler, because asking the daemon can take seconds.
func notifySetup(s *server.MCPServer, summary func() setupSummary) {
	var mu sync.Mutex
	sent := map[string]bool{}
	s.AddNotificationHandler(string(mcp.MethodNotificationInitialized), func(ctx context.Context, _ mcp.JSONRPCNotification) {
		session := server.ClientSessionFromContext(ctx)
		if session == nil {
			return
		}
		id := session.SessionID()
		mu.Lock()
		if sent[id] {
			mu.Unlock()
			return
		}
		sent[id] = true
		mu.Unlock()
		go func() {
			_ = s.SendNotificationToSpecificClient(id, "notifications/message", setupNotification(summary()))
		}()
	})
}
