package raglit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Tree indexing (R10a): bring an index in step with a directory tree in one
// call. The selection rules are PlanSources' (git ls-files in a git work tree,
// the built-in ignores), plus a size limit and an up-front refusal of the
// bytes the worker would refuse anyway (binary, minified). A file is Current
// when its stored hash equals the hash of its bytes, Queued when it is new or
// changed, Failed when its last ingest failed on these same bytes and is not
// to be retried yet (failedsources.go: a document error until the bytes
// change, a transient one until its backoff passes; Store.ClearFailed
// forgets both), and a document under the root that is no longer selected is
// Removed.

// TreeRules choose the files of a tree. IndexTree and WatchTree share them.
type TreeRules struct {
	// Include globs, relative to the root; empty = every file. A glob with no
	// "/" matches the file name, as in PlanSources.
	Include []string
	// Ignore globs, relative to the root, on top of the built-in ignores.
	Ignore []string
	// Gitignore: nil or true = honor .gitignore (git ls-files) in a git work
	// tree; false = walk the directory.
	Gitignore *bool
	// Skip drops an absolute path (a file, or for WatchTree a directory).
	Skip func(path string) bool
	// MaxBytes: a larger file counts as Skipped. 0 = 2 MiB; negative = no
	// limit. A document whose file grew past the limit is kept, not Removed.
	MaxBytes int64
}

// TreeOptions are IndexTree's options.
type TreeOptions struct {
	Rules TreeRules
	// Paths: check only these (absolute, or relative to the root). A directory
	// covers what is under it; a path that no longer exists counts its
	// documents as Removed. Empty = the whole tree.
	Paths []string
	// Route picks the index (a key of IndexTrees' map) for a file. Optional
	// with one store.
	Route func(path string) string
	// Keep spares a document under the root that the rules do not select:
	// one whose path (absolute, no scheme) Keep reports true for is not
	// Removed. For roots that overlap in one index, each with its own rules:
	// Keep is "another root selects it".
	Keep func(path string) bool
}

// TreeResult counts what IndexTree did per file. Errors are per-file
// failures (a read, an enqueue); the run goes on past them.
type TreeResult struct {
	Queued, Current, Removed, Failed, Skipped int
	Errors                                    []error
}

// Add sums o into r.
func (r *TreeResult) Add(o TreeResult) {
	r.Queued += o.Queued
	r.Current += o.Current
	r.Removed += o.Removed
	r.Failed += o.Failed
	r.Skipped += o.Skipped
	r.Errors = append(r.Errors, o.Errors...)
}

const defaultTreeMaxBytes = 2 << 20

// maxBytes is the size limit; 0 = none.
func (r TreeRules) maxBytes() int64 {
	switch {
	case r.MaxBytes > 0:
		return r.MaxBytes
	case r.MaxBytes < 0:
		return 0
	}
	return defaultTreeMaxBytes
}

func (r TreeRules) gitignore() bool { return r.Gitignore == nil || *r.Gitignore }

// excluded reports whether the exclusion rules drop abs (rel to the root).
// Include is not an exclusion rule: it cannot judge a path that is gone.
func (r TreeRules) excluded(abs, rel string) bool {
	if matchAny(builtinIgnore, rel) || matchAny(r.Ignore, rel) {
		return true
	}
	return r.Skip != nil && r.Skip(abs)
}

// Admits reports whether the path rules (built-in ignores, Ignore, Include,
// Skip) select the file at path under root. Not .gitignore, and not the
// file's size or bytes. False outside root.
func (r TreeRules) Admits(root, path string) bool {
	if !under(path, root) || path == root {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return r.admits(path, filepath.ToSlash(rel))
}

// admits reports whether a file at abs (rel to the root) is selected by its
// path.
func (r TreeRules) admits(abs, rel string) bool {
	if r.excluded(abs, rel) {
		return false
	}
	return len(r.Include) == 0 || matchAny(r.Include, rel)
}

// IndexTree brings st in step with the files under root. See TreeOptions.
func IndexTree(ctx context.Context, st *Store, root string, o TreeOptions) (TreeResult, error) {
	o.Route = func(string) string { return "" }
	m, err := IndexTrees(ctx, map[string]*Store{"": st}, root, o)
	return m[""], err
}

// IndexTrees is IndexTree over several indexes: o.Route picks each file's
// index, and a document under root in an index its file does not route to is
// Removed from it. A file routed to a name not in stores is in none.
func IndexTrees(ctx context.Context, stores map[string]*Store, root string, o TreeOptions) (map[string]TreeResult, error) {
	route := o.Route
	if route == nil {
		if len(stores) != 1 {
			return nil, errors.New("raglit: index trees: Route is required with more than one store")
		}
		for name := range stores {
			route = func(string) string { return name }
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		// A root that is gone or unmounted is not a tree whose files were all
		// deleted: removing every document would be the wrong answer.
		return nil, fmt.Errorf("raglit: index tree: %s is not a directory", root)
	}
	full := len(o.Paths) == 0
	var scopes []string
	for _, p := range o.Paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		if p == root {
			full = true
		} else if under(p, root) {
			scopes = append(scopes, p)
		}
	}
	if full {
		scopes = nil
	} else if len(scopes) == 0 {
		out := map[string]TreeResult{}
		for name := range stores {
			out[name] = TreeResult{}
		}
		return out, nil
	}
	files, err := selectTree(root, o.Rules, scopes)
	if err != nil {
		return nil, err
	}
	routed := map[string][]string{}
	for _, f := range files {
		name := route(f)
		if _, ok := stores[name]; ok {
			routed[name] = append(routed[name], f)
		}
	}
	names := make([]string, 0, len(stores))
	for name := range stores {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]TreeResult, len(stores))
	for _, name := range names {
		res, err := indexTreeFiles(ctx, stores[name], root, scopes, o, routed[name])
		out[name] = res
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// selectTree lists the files under root (or under scopes, when set) that the
// path rules select: git's view of the tree when it is a git work tree and
// rules honor .gitignore, else a walk. A git listing may name a tracked file
// that is gone from disk; the caller's stat drops it.
func selectTree(root string, r TreeRules, scopes []string) ([]string, error) {
	var cands []string
	var err error
	useGit := r.gitignore() && isGitWorkTree(root)
	if useGit {
		if cands, err = gitFiles(root, scopes); err != nil {
			useGit = false // git failed: walk instead
		}
	}
	if !useGit {
		if cands, err = walkScopes(root, scopes); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, abs := range cands {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[abs] || !r.admits(abs, rel) {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	sort.Strings(out)
	return out, nil
}

// gitFiles is git ls-files under root, limited to scopes when set. It returns
// (nil, err) when git fails, so the caller walks instead.
func gitFiles(root string, scopes []string) ([]string, error) {
	run := func(specs []string) ([]string, error) {
		args := []string{"--literal-pathspecs", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z"}
		if len(specs) > 0 {
			args = append(append(args, "--"), specs...)
		}
		b, err := exec.Command("git", args...).Output()
		if err != nil {
			return nil, err
		}
		var files []string
		for _, rel := range strings.Split(strings.TrimRight(string(b), "\x00"), "\x00") {
			if rel != "" {
				files = append(files, filepath.Join(root, rel))
			}
		}
		return files, nil
	}
	if len(scopes) == 0 {
		return run(nil)
	}
	var out []string
	const batch = 500
	for i := 0; i < len(scopes); i += batch {
		var specs []string
		for _, s := range scopes[i:min(i+batch, len(scopes))] {
			rel, err := filepath.Rel(root, s)
			if err != nil {
				continue
			}
			specs = append(specs, rel)
		}
		files, err := run(specs)
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}
	return out, nil
}

// walkScopes walks root, or each scope: a file is itself, a directory is what
// is under it, a missing path is nothing.
func walkScopes(root string, scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return walkFiles(root)
	}
	var out []string
	for _, s := range scopes {
		fi, err := os.Stat(s)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			out = append(out, s)
			continue
		}
		files, err := walkFiles(s)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		out = append(out, files...)
	}
	return out, nil
}

// treeDocs is the documents of st under root (and under a scope, when set),
// keyed by bare path, with every stored spelling of each. A document in the
// index's own storage (an extracted attachment, an upload) is raglit's, not
// the tree's, and is left out.
func treeDocs(st *Store, root string, scopes []string) (docs map[string][]string, scheme string, err error) {
	paths, err := st.DocumentPaths()
	if err != nil {
		return nil, "", err
	}
	docs = map[string][]string{}
	urls, bare := 0, 0
	for _, p := range paths {
		sch, rest := splitScheme(p)
		if !filepath.IsAbs(rest) || !under(rest, root) {
			continue
		}
		if len(scopes) > 0 && !underAny(rest, scopes) {
			continue
		}
		if _, own := st.StorageRel(rest); own {
			continue
		}
		docs[rest] = append(docs[rest], p)
		if sch != "" {
			urls++
		} else {
			bare++
		}
	}
	if urls > bare {
		scheme = "file://"
	}
	return docs, scheme, nil
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if under(p, d) {
			return true
		}
	}
	return false
}

// treeSkipsBytes reports whether the worker would refuse the file's bytes
// for having nothing to index: the same test as its binary and minified
// guards, which only judge a file whose kind nothing named. It takes a
// fileScan, not the bytes, so a large file is not held in memory.
func treeSkipsBytes(path string, sc fileScan) bool {
	kind := ClassifyDoc(path, "")
	if kind == KindUnknown {
		kind = SniffBytes(sc.head)
	}
	if kind != KindUnknown {
		return false
	}
	if ct := http.DetectContentType(sc.head); strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
		return false // the worker routes it to the transcriber
	}
	return IsOpaque(sc.head) || sc.longest >= minifiedLineBytes
}

// fileScanHead is how many leading bytes a fileScan keeps: more than
// http.DetectContentType (512) and SniffBytes look at.
const fileScanHead = 8 << 10

// fileScan is what one streaming read of a file learns: the hash of its
// bytes, its first bytes, and its longest line (IsMinified's measure).
type fileScan struct {
	hash    string
	head    []byte
	longest int
}

// scanWriter feeds a fileScan from io.Copy.
type scanWriter struct {
	h       hash.Hash
	head    []byte
	longest int
	line    int // bytes since the last newline
}

func (w *scanWriter) Write(b []byte) (int, error) {
	n := len(b)
	w.h.Write(b)
	if room := fileScanHead - len(w.head); room > 0 {
		w.head = append(w.head, b[:min(room, len(b))]...)
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			w.line += len(b)
			break
		}
		w.longest = max(w.longest, w.line+i)
		w.line = 0
		b = b[i+1:]
	}
	return n, nil
}

// scanFile reads the file at path once, in pieces.
func scanFile(path string) (fileScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileScan{}, err
	}
	defer f.Close()
	w := &scanWriter{h: sha256.New()}
	if _, err := io.Copy(w, f); err != nil {
		return fileScan{}, err
	}
	return fileScan{hash: hex.EncodeToString(w.h.Sum(nil)), head: w.head, longest: max(w.longest, w.line)}, nil
}

// indexTreeFiles classifies files (selected and routed to st) and removes
// st's documents under root that are not among the files kept.
func indexTreeFiles(ctx context.Context, st *Store, root string, scopes []string, o TreeOptions, files []string) (TreeResult, error) {
	var res TreeResult
	r := o.Rules
	docs, scheme, err := treeDocs(st, root, scopes)
	if err != nil {
		return res, err
	}
	keep := map[string]bool{}
	var items []EnqueueItem
	limit := r.maxBytes()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		key := scheme + f
		if stored := docs[f]; len(stored) > 0 {
			key = stored[0]
		}
		if st.IgnoredBy(key) != "" {
			res.Skipped++
			continue
		}
		fi, err := os.Stat(f)
		if os.IsNotExist(err) {
			continue // a tracked file deleted from the work tree
		}
		if err != nil {
			res.Errors = append(res.Errors, err)
			keep[f] = true // unreadable is not gone
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if limit > 0 && fi.Size() > limit {
			// Too large to index now is not gone: a document already
			// stored for it stays.
			res.Skipped++
			keep[f] = true
			continue
		}
		sc, err := scanFile(f)
		if err != nil {
			res.Errors = append(res.Errors, err)
			keep[f] = true
			continue
		}
		hash := sc.hash
		if prev, _ := st.DocumentHash(key); prev != "" && prev == hash {
			res.Current++
			keep[f] = true
			continue
		}
		if treeSkipsBytes(f, sc) {
			res.Skipped++
			continue
		}
		keep[f] = true
		if st.failedOn(hash, key, strings.TrimPrefix(key, "file://"), "file://"+f) {
			res.Failed++
			continue
		}
		items = append(items, EnqueueItem{URL: key})
	}
	if len(items) > 0 {
		out, err := st.EnqueueMany(items)
		if err != nil {
			return res, err
		}
		for i, o := range out {
			switch {
			case errors.Is(o.Err, ErrIgnored):
				res.Skipped++
				delete(keep, strings.TrimPrefix(items[i].URL, "file://"))
			case o.Err != nil:
				res.Errors = append(res.Errors, o.Err)
			default:
				res.Queued++
			}
		}
	}
	gone := make([]string, 0, len(docs))
	for f := range docs {
		if !keep[f] && (o.Keep == nil || !o.Keep(f)) {
			gone = append(gone, f)
		}
	}
	sort.Strings(gone)
	for _, f := range gone {
		for _, p := range docs[f] {
			if err := st.removeTreeDocument(p); err != nil {
				res.Errors = append(res.Errors, err)
				continue
			}
			res.Removed++
		}
	}
	return res, nil
}

// removeTreeDocument removes a document whose file left the tree, with its
// pending jobs, caption rows and failure record. The file is the user's and
// is not touched.
func (s *Store) removeTreeDocument(p string) error {
	if err := s.RemoveDocument(p, false); err != nil {
		return fmt.Errorf("raglit: remove %s: %w", p, err)
	}
	if _, err := s.db.Exec(`DELETE FROM identity_jobs WHERE path=? AND state<>'running'`, p); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM ingest_jobs WHERE url=? AND state='pending'`, p); err != nil {
		return err
	}
	s.clearFailedSource(p)
	return nil
}
