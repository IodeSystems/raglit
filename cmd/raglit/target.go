package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// The daemon and the one index a command acts on.
//
// Every command reaches its index through the daemon (USER 2026-10-30: raglit
// is always a daemon connection; "Solo is a nuance to be avoided"). Before,
// some commands opened the index file in-process: a project-local one, or the
// daemon's own file behind its back, read-only or, for two of them, as a second
// writer. Those paths skipped auth, namespaces and the daemon's single-writer
// rule, so they are gone; a command resolves a target and calls a route.

// target is a resolved daemon and index.
type target struct {
	base  string // daemon base URL
	index string // the daemon's name for the index: "<project>__<local>"
	ns    string // the project namespace
	home  raglit.Home
}

// addTargetFlags registers --home, --index, --daemon and --project, and
// returns the resolver to call after fs.Parse.
//
// --index takes a local name (this project's), "proj:idx" (one index of any
// project, the daemon's ACL deciding) or ":idx" (a global index).
func addTargetFlags(fs *flag.FlagSet) func() (target, error) {
	home := fs.String("home", "", "project home dir (default: nearest ./.raglit, else $RAGLIT_HOME)")
	index := fs.String("index", "", "index name, proj:idx or :idx (default: config default_index, else 'default')")
	daemon := addDaemonFlag(fs)
	project := fs.String("project", "", `project name (default: config "project")`)
	return func() (target, error) {
		homeOf := func() raglit.Home {
			if *home != "" {
				return raglit.Home(*home)
			}
			return raglit.DiscoverHome()
		}
		base, err := ensureDaemon(*daemon, homeOf)
		if err != nil {
			return target{}, err
		}
		local := resolveIndexName(*index, homeOf)
		if d, ok := nsNamedIndex(local); ok {
			ns, _ := resolveProject(*project, homeOf)
			return target{base: base, index: d, ns: ns, home: homeOf()}, nil
		}
		ns, err := resolveProject(*project, homeOf)
		if err != nil {
			return target{}, err
		}
		return target{base: base, index: nsIndex(ns, local), ns: ns, home: homeOf()}, nil
	}
}

// get calls GET path with index set, decoding the JSON answer into out (nil
// to discard it).
func (t target) get(path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if q.Get("index") == "" {
		q.Set("index", t.index)
	}
	b, err := daemonGet(t.base, path, q)
	if err != nil {
		return err
	}
	return decodeInto(path, b, out)
}

// post calls POST path?index=… with body as JSON, decoding the answer into
// out (nil to discard it).
func (t target) post(path string, body, out any) error {
	return t.send(http.MethodPost, path, body, out)
}

// send is post with any method.
func (t target) send(method, path string, body, out any) error {
	b, err := daemonDo(t.base, method, path+"?index="+url.QueryEscape(t.index), body)
	if err != nil {
		return err
	}
	return decodeInto(path, b, out)
}

func decodeInto(path string, b []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("daemon %s: %w", path, err)
	}
	return nil
}

// local is the index's name as this project's user knows it: the project
// prefix removed.
func (t target) local() string { return nsStrip(t.ns, t.index) }

// documents lists the index's documents.
func (t target) documents() ([]raglit.DocSummary, error) {
	var out struct {
		Documents []raglit.DocSummary `json:"documents"`
	}
	if err := t.get("/api/documents", nil, &out); err != nil {
		return nil, err
	}
	return out.Documents, nil
}
