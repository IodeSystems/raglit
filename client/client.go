// Package client is raglit's Go client: how another program asks raglit about
// raglit's data.
//
// It exists because the alternative was tried and failed. kgraph read
// relations.jsonl directly, raglit later moved rulings into a database projected
// from an audit trail, and kgraph went on reading a file nobody wrote any more —
// no error, no warning, just stale answers. Parsing another tool's storage makes
// every storage change a silent break in a program you did not edit.
//
// So the contract is the HTTP API and never the files. raglit may reorganise its
// storage whenever it likes; a consumer only has to keep speaking this.
//
// Availability is not an error. A consumer asking about rulings on a machine
// where raglit is not running should be told "unknown", not fail: kgraph must
// still scan a corpus when no daemon is up. Every call here returns
// ErrUnavailable for that case so the caller can tell "nothing ruled" from
// "could not ask" — a distinction that matters, because the first is a fact
// about the corpus and the second is a fact about this machine.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable means raglit could not be reached — NOT that it had nothing to
// say. Callers must not read it as an empty result.
var ErrUnavailable = errors.New("raglit is not reachable")

// DefaultAddr is the shared per-user daemon.
const DefaultAddr = "http://127.0.0.1:7420"

// Client talks to a raglit daemon.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for base, defaulting to $RAGLIT_DAEMON then the shared
// per-user daemon. Discovery also consults the daemon's own state file, so a
// daemon on a non-default port is found instead of a dead default being probed.
func New(base string) *Client {
	if base == "" {
		base = os.Getenv("RAGLIT_DAEMON")
	}
	if base == "" {
		base = discover()
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		// Short: a consumer enriching a scan must not hang on a wedged daemon.
		// Being told "unknown" quickly beats being right slowly here.
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

// discover reads <root>/daemon.json, which the daemon writes on startup, and
// falls back to the default address.
func discover() string {
	root := os.Getenv("RAGLIT_ROOT")
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".raglit")
		}
	}
	if root == "" {
		return DefaultAddr
	}
	b, err := os.ReadFile(filepath.Join(root, "daemon.json"))
	if err != nil {
		return DefaultAddr
	}
	var st struct {
		Addr string `json:"addr"`
	}
	if json.Unmarshal(b, &st) != nil || st.Addr == "" {
		return DefaultAddr
	}
	return "http://" + st.Addr
}

// Relation is a ruling on a pair of documents.
type Relation struct {
	A          string  `json:"a"`
	B          string  `json:"b"`
	Kind       string  `json:"kind"` // copy | version | unrelated
	Supersedes string  `json:"supersedes,omitempty"`
	Note       string  `json:"note,omitempty"`
	By         string  `json:"by,omitempty"`
	At         string  `json:"at,omitempty"`
	Relation   string  `json:"relation,omitempty"`
	Coverage   float64 `json:"coverage,omitempty"`
}

// Other returns the side of the pair that is not doc.
func (r Relation) Other(doc string) (string, bool) {
	switch doc {
	case r.A:
		return r.B, true
	case r.B:
		return r.A, true
	}
	return "", false
}

// Slice is a declared sub-document: a page range of a bundle.
type Slice struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	Title  string `json:"title,omitempty"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

// Hit is one search result: a fragment of a document raglit holds.
//
// Doc is raglit's own document id, which is an ABSOLUTE path — a consumer whose
// own document paths are relative has to relativize it, and comparing the two
// unnormalized matches nothing while looking exactly like "found nothing".
type Hit struct {
	Index   string  `json:"index"`
	Doc     string  `json:"doc_id"`
	Title   string  `json:"title,omitempty"`
	Page    int     `json:"page,omitempty"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
}

// Search runs one query against a named index and returns the best n fragments.
//
// index is raglit's full index name, which for a project-scoped index is
// "<project>__<index>" — the daemon namespaces them, and passing a bare project
// name silently searches somebody else's corpus.
//
// An unreachable daemon is an ERROR, never an empty result. A consumer deciding
// whether a corpus holds a document cannot be handed "no" when the truth is "not
// asked": that is the one answer that stops a person looking for evidence that
// is actually there.
func (c *Client) Search(ctx context.Context, index, q string, n int) ([]Hit, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}
	v := url.Values{"q": {q}, "n": {strconv.Itoa(n)}}
	if index != "" {
		v.Set("index", index)
	}
	var out struct {
		Hits []Hit `json:"hits"`
	}
	if err := c.get(ctx, "/search", v, &out); err != nil {
		return nil, err
	}
	return out.Hits, nil
}

// Alive reports whether the daemon answers.
func (c *Client) Alive(ctx context.Context) bool {
	var out struct {
		Status string `json:"status"`
	}
	return c.get(ctx, "/api/health", nil, &out) == nil
}

// Relations returns every ruling for a project. doc, when non-empty, narrows to
// rulings involving that document.
func (c *Client) Relations(ctx context.Context, project, doc string) ([]Relation, error) {
	q := url.Values{"project": {project}}
	if doc != "" {
		q.Set("doc", doc)
	}
	var out struct {
		Relations []Relation `json:"relations"`
	}
	if err := c.get(ctx, "/api/relations", q, &out); err != nil {
		return nil, err
	}
	return out.Relations, nil
}

// Slices returns declared sub-documents for a project, optionally one bundle's.
func (c *Client) Slices(ctx context.Context, project, parent string) ([]Slice, error) {
	q := url.Values{"project": {project}}
	if parent != "" {
		q.Set("parent", parent)
	}
	var out struct {
		Slices []Slice `json:"slices"`
	}
	if err := c.get(ctx, "/api/slices", q, &out); err != nil {
		return nil, err
	}
	return out.Slices, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Connection refused, DNS, timeout: the daemon is not there. Wrapped so a
		// caller can distinguish it from a daemon that answered with an error.
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// An older daemon without these routes. Same meaning as unreachable: this
		// raglit cannot answer, so the caller must say "unknown" rather than
		// "none" — silently reporting no rulings is the failure this whole
		// package exists to prevent.
		return fmt.Errorf("%w: daemon has no %s (older raglit?)", ErrUnavailable, path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("raglit %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
