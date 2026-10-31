package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/iodesystems/gwag/v2/gw/gat"

	"github.com/iodesystems/raglit"
)

// Minting, listing and revoking API tokens and session keys (raglit/keys.go).
//
//   - An API token: the admin mints one for a principal that is not a person
//     ("aw4"), then hands it an index with POST /api/owner.
//   - A session key: any principal that is not itself a session key mints one
//     for a project and a session. It reads the project's indexes and the
//     global ones (as far as its issuer may), and writes only that session's
//     branches, "<project>__*@<session>".
//   - A child key: a session key mints one for ONE of its branches, for a
//     sub-agent. The child owns that branch; its parent reads and revokes it.

const keysFile = "keys.json"

func registerKeyRoutes(api huma.API, g *gat.Gateway, reg *raglit.Registry) {
	op := func(id, method, path, summary string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
	}
	gat.Register(api, g, op("mintKey", http.MethodPost, "/api/keys",
		"Mint an API token (admin, for a principal), a session key (for a project and session) or a child key (from a session key, for one branch). The token is returned once."),
		mintKeyOp(reg))
	gat.Register(api, g, op("listKeys", http.MethodGet, "/api/keys",
		"The keys the caller issued and the keys under them (all of them for the admin); never a token."), listKeysOp(reg))
	gat.Register(api, g, op("revokeKey", http.MethodPost, "/api/keys/revoke",
		"Revoke a key and every key under it (its issuer, an ancestor key or the admin)."), revokeKeyOp(reg))
}

type mintKeyIn struct {
	Body struct {
		Principal string   `json:"principal,omitempty" doc:"API token: the principal it acts as (admin only)"`
		Project   string   `json:"project,omitempty" doc:"session key: the project namespace"`
		Session   string   `json:"session,omitempty" doc:"session key: the session id (e.g. aw4's tracking id)"`
		Shared    []string `json:"shared,omitempty" doc:"session key: other namespaces it may also read"`
		Branch    string   `json:"branch,omitempty" doc:"child key: the daemon name of the one branch it writes"`
		Label     string   `json:"label,omitempty"`
	}
}

type mintKeyOut struct {
	Body struct {
		Token string     `json:"token"`
		Key   raglit.Key `json:"key"`
	}
}

func mintKeyOp(reg *raglit.Registry) func(context.Context, *mintKeyIn) (*mintKeyOut, error) {
	return func(ctx context.Context, in *mintKeyIn) (*mintKeyOut, error) {
		ks := reg.Keys()
		p := raglit.PrincipalFrom(ctx)
		if ks == nil || p == "" {
			return nil, huma.Error403Forbidden("keys need the daemon's auth")
		}
		b := in.Body
		k := raglit.Key{Issuer: p, Label: b.Label}
		parent, fromKey := ks.ByPrincipal(p)
		switch {
		case strings.TrimSpace(b.Principal) != "":
			if err := requireAdmin(ctx, reg); err != nil {
				return nil, err
			}
			pr := strings.TrimSpace(b.Principal)
			if strings.HasPrefix(pr, raglit.KeyPrincipalPrefix) || pr == reg.Admin() {
				return nil, huma.Error400BadRequest("an API token's principal must be a new name, not a key's or the admin's")
			}
			k.Kind, k.Principal = raglit.KeyAPI, pr
		case fromKey:
			branch := raglit.NormalizeIndexName(b.Branch)
			if _, ok := raglit.SessionBranchParent(branch); !ok || strings.TrimSpace(b.Branch) == "" {
				return nil, huma.Error400BadRequest("a session key mints a child key for one session branch: give branch (\"proj__idx@session\")")
			}
			if !parent.AllowsWrite(branch) {
				return nil, huma.Error403Forbidden("that branch is outside what this key may write")
			}
			k.Kind, k.Parent = raglit.KeySession, parent.ID
			k.Read, k.Write = parent.Read, []string{branch}
		default:
			ns, sess := raglit.NormalizeIndexName(b.Project), raglit.NormalizeIndexName(b.Session)
			if strings.TrimSpace(b.Project) == "" || strings.TrimSpace(b.Session) == "" || strings.Contains(sess, "@") {
				return nil, huma.Error400BadRequest("a session key needs project and session")
			}
			k.Kind = raglit.KeySession
			k.Read = []string{ns + nsSep + "*", raglit.GlobalPattern}
			for _, sh := range b.Shared {
				if sh = raglit.NormalizeIndexName(sh); sh != "" {
					k.Read = append(k.Read, sh+nsSep+"*")
				}
			}
			k.Write = []string{ns + nsSep + "*@" + sess}
		}
		token, stored, err := ks.Mint(k)
		if err != nil {
			return nil, huma.Error500InternalServerError("mint", err)
		}
		out := &mintKeyOut{}
		out.Body.Token, out.Body.Key = token, stored
		return out, nil
	}
}

type listKeysOut struct {
	Body struct {
		Keys []raglit.Key `json:"keys"`
	}
}

func listKeysOp(reg *raglit.Registry) func(context.Context, *struct{}) (*listKeysOut, error) {
	return func(ctx context.Context, _ *struct{}) (*listKeysOut, error) {
		out := &listKeysOut{}
		out.Body.Keys = []raglit.Key{}
		if ks := reg.Keys(); ks != nil {
			if l := ks.List(raglit.PrincipalFrom(ctx), reg.Admin()); l != nil {
				out.Body.Keys = l
			}
		}
		return out, nil
	}
}

type revokeKeyIn struct {
	Body struct {
		ID string `json:"id"`
	}
}

func revokeKeyOp(reg *raglit.Registry) func(context.Context, *revokeKeyIn) (*okOut, error) {
	return func(ctx context.Context, in *revokeKeyIn) (*okOut, error) {
		ks := reg.Keys()
		if ks == nil {
			return nil, huma.Error404NotFound("no such key")
		}
		switch err := ks.Revoke(raglit.PrincipalFrom(ctx), reg.Admin(), in.Body.ID); {
		case errors.Is(err, raglit.ErrBadKey):
			return nil, huma.Error404NotFound("no such key")
		case errors.Is(err, raglit.ErrForbidden):
			return nil, huma.Error403Forbidden("only the key's issuer, an ancestor key or the admin revokes it")
		case err != nil:
			return nil, huma.Error500InternalServerError("revoke", err)
		}
		out := &okOut{}
		out.Body.OK = true
		return out, nil
	}
}
