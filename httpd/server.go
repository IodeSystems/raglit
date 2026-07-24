// Package httpd is raglit's remote daemon HTTP surface, built on the
// huma+gwag/gat multi-protocol stack (REST + in-process GraphQL + gRPC over one
// port, OpenAPI at /openapi.json). Data access is the hybrid from
// plan/daemon-stack.md: sqlc+metaquery (internal/db) for relational CRUD, raw
// SQL for FTS5 search + vector cosine.
package httpd

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/iodesystems/gwag/gw/gat"
)

// Handler builds the chi router with the gat gateway mounted: REST + OpenAPI
// (via humachi) plus /graphql and /schema/* (via gat.RegisterHuma).
func Handler(title, version string) (http.Handler, error) {
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig(title, version))

	g, err := gat.New()
	if err != nil {
		return nil, err
	}
	gat.Register(api, g, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/api/health",
		Summary:     "Liveness probe.",
	}, health)

	if err := gat.RegisterHuma(api, g, ""); err != nil {
		return nil, err
	}
	return router, nil
}

type healthOut struct {
	Body struct {
		Status string `json:"status"`
	}
}

func health(_ context.Context, _ *struct{}) (*healthOut, error) {
	out := &healthOut{}
	out.Body.Status = "ok"
	return out, nil
}
