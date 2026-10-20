package main

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// The `$schema` link in every JSON response, with the scheme the request used.
//
// huma writes "https://" for any host that is not localhost or 127.0.0.1, so a
// daemon served over plain HTTP on a LAN address answered every request with
// "$schema":"https://192.168.1.76:7431/schemas/…" — a link to a server that does
// not exist. The scheme is the request's: X-Forwarded-Proto when a proxy
// terminated TLS in front of the daemon, otherwise whether this connection is
// TLS.

// schemaSchemeConfig appends the scheme fix after huma's own link transformer.
// A create hook, because that is where huma adds its transformer, and hooks run
// in order.
func schemaSchemeConfig(c huma.Config) huma.Config {
	c.CreateHooks = append(c.CreateHooks, func(c huma.Config) huma.Config {
		c.Transformers = append(c.Transformers, schemaScheme)
		return c
	})
	return c
}

func schemaScheme(ctx huma.Context, _ string, v any) (any, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return v, nil
	}
	sv := rv.Elem()
	if sv.NumField() == 0 {
		return v, nil
	}
	f := sv.Field(0)
	if f.Kind() != reflect.String || !f.CanSet() || sv.Type().Field(0).Tag.Get("json") != "$schema" {
		return v, nil
	}
	scheme := "http"
	if p := strings.ToLower(strings.TrimSpace(strings.Split(ctx.Header("X-Forwarded-Proto"), ",")[0])); p == "http" || p == "https" {
		scheme = p
	} else if ctx.TLS() != nil {
		scheme = "https"
	}
	link := f.String()
	if i := strings.Index(link, "://"); i > 0 {
		f.SetString(scheme + link[i:])
	}
	return v, nil
}

// relativeSchemaExamples drops huma's placeholder host from the `$schema`
// examples in the OpenAPI document.
//
// With no Servers configured, huma writes "https://example.com/schemas/X.json"
// as the example of every response's `$schema` — a host that is not this
// daemon, on a scheme it does not serve. The daemon does not know the host it
// is reached by until a request arrives, so the example is the path alone.
// Call after every operation is registered.
func relativeSchemaExamples(oapi *huma.OpenAPI) {
	const placeholder = "https://example.com"
	for _, s := range oapi.Components.Schemas.Map() {
		p := s.Properties["$schema"]
		if p == nil {
			continue
		}
		for i, ex := range p.Examples {
			if str, ok := ex.(string); ok {
				p.Examples[i] = strings.TrimPrefix(str, placeholder)
			}
		}
	}
}
