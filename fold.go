package raglit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Folding public layers into a project's base — the maintainer's job.
//
// A base nobody writes to leaves every shared document duplicated across the
// layers that hold it (savings.go counts what that costs). The maintainer owns
// the base and, run on a schedule, copies each PUBLIC layer document the base
// does not yet hold into it, once per content. Private layers are never read.
//
// Deduplication is by content hash: the same bytes held by three layers become
// one base document. Different bytes under the same name stay separate
// documents — "merge" here means one copy per content, not a text merge.
//
// Provenance travels with the copy. The base document gets a note saying which
// layer and file it came from, who folded it and the content hash, and the
// source document's notes are copied with their original authors and times.
// Notes attach to an imported document, and import is asynchronous, so that
// happens on the NEXT run ("settling"), which is why the fold log exists.
//
// The layers are not changed: they belong to other people.

// FoldRecord is one content folded into the base, kept in <base home>/folds.json.
type FoldRecord struct {
	Hash      string `json:"hash"`
	Layer     string `json:"layer"`
	Source    string `json:"source"`    // the layer document's path
	BasePath  string `json:"base_path"` // the base document's path (an upload URL)
	Title     string `json:"title,omitempty"`
	By        string `json:"by"`
	FoldedAt  string `json:"folded_at"`
	Settled   bool   `json:"settled"` // provenance note and source notes copied
	SettledAt string `json:"settled_at,omitempty"`
}

// FoldReport is what one run did (or, dry, would do).
type FoldReport struct {
	ProjectID     string       `json:"project_id"`
	Base          string       `json:"base"`
	DryRun        bool         `json:"dry_run"`
	LayersScanned []string     `json:"layers_scanned"` // public layers read
	Folded        []FoldRecord `json:"folded"`
	AlreadyInBase int          `json:"already_in_base"` // layer documents whose content the base holds
	Settled       int          `json:"settled"`         // earlier folds whose notes were copied this run
	Pending       int          `json:"pending"`         // earlier folds still importing
	Skipped       []string     `json:"skipped,omitempty"`
}

func foldsPath(home Home) string { return filepath.Join(string(home), "folds.json") }

func readFolds(home Home) (map[string]*FoldRecord, error) {
	m := map[string]*FoldRecord{}
	b, err := os.ReadFile(foldsPath(home))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []*FoldRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("raglit: %s: %w", foldsPath(home), err)
	}
	for _, r := range recs {
		m[r.Hash] = r
	}
	return m, nil
}

func writeFolds(home Home, m map[string]*FoldRecord) error {
	recs := make([]*FoldRecord, 0, len(m))
	for _, r := range m {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].FoldedAt+recs[i].Hash < recs[j].FoldedAt+recs[j].Hash })
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := foldsPath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, foldsPath(home))
}

// FoldProject folds the public layers of project id into its base, as the
// caller, who must own the base. dry reports without writing anything.
func (r *Registry) FoldProject(ctx context.Context, id string, dry bool) (FoldReport, error) {
	rep := FoldReport{ProjectID: id, Base: r.ProjectBase(id), DryRun: dry}
	if rep.Base == "" || !r.CanRead(ctx, rep.Base) {
		return rep, ErrNoIndex
	}
	base, err := r.Write(ctx, rep.Base)
	if err != nil {
		return rep, err // ErrForbidden: the caller does not own the base
	}
	if !base.withHome {
		return rep, fmt.Errorf("raglit: the base has no home to store folded documents in")
	}
	// One fold at a time per registry: two runs racing would copy one content
	// twice before either recorded it.
	r.registerMu.Lock()
	defer r.registerMu.Unlock()

	folds, err := readFolds(base.home)
	if err != nil {
		return rep, err
	}
	by := PrincipalFrom(ctx)
	if by == "" {
		by = "raglit maintain"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ns, _, _ := strings.Cut(rep.Base, "__")

	// 1. Settle earlier folds whose base document has finished importing.
	for _, f := range folds {
		if f.Settled {
			continue
		}
		if _, err := base.docIDForNote(f.BasePath); err != nil {
			rep.Pending++
			continue
		}
		if dry {
			rep.Settled++
			continue
		}
		if err := r.settleFold(base, f); err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("settle %s: %v", f.Source, err))
			continue
		}
		f.Settled, f.SettledAt = true, now
		rep.Settled++
	}

	// 2. Fold new public content.
	inBase := map[string]bool{}
	if costs, err := base.DocCosts(); err == nil {
		for _, c := range costs {
			inBase[c.Hash] = true
		}
	}
	for _, layer := range r.LayersOf(rep.Base) {
		if !r.foldable(ctx, layer) {
			continue
		}
		st, err := r.Existing(layer)
		if err != nil {
			continue
		}
		rep.LayersScanned = append(rep.LayersScanned, layer)
		costs, err := st.DocCosts()
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: %v", layer, err))
			continue
		}
		for _, c := range costs {
			if inBase[c.Hash] || folds[c.Hash] != nil {
				rep.AlreadyInBase++
				continue
			}
			src, ok := sourceBytesPath(st, c.Path)
			if !ok {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s %s: the source bytes are not on this host", layer, c.Path))
				continue
			}
			name := strings.TrimPrefix(layer, ns+"__") + "/" + foldName(st, c.Path)
			rec := &FoldRecord{Hash: c.Hash, Layer: layer, Source: c.Path, Title: docTitle(st, c.Path), By: by, FoldedAt: now}
			if !dry {
				data, err := os.ReadFile(src)
				if err != nil {
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s %s: %v", layer, c.Path, err))
					continue
				}
				saved, err := base.SaveUpload(name, data)
				if err != nil {
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s %s: %v", layer, c.Path, err))
					continue
				}
				rec.BasePath = UploadURL(saved)
				if _, err := base.EnqueueFresh(rec.BasePath, rec.Title, false); err != nil {
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s %s: %v", layer, c.Path, err))
					continue
				}
				folds[c.Hash] = rec
			}
			inBase[c.Hash] = true
			rep.Folded = append(rep.Folded, *rec)
		}
	}
	if dry {
		return rep, nil
	}
	return rep, writeFolds(base.home, folds)
}

// settleFold writes the provenance note on the base document and copies the
// source document's notes, with their authors and times.
func (r *Registry) settleFold(base *Store, f *FoldRecord) error {
	if _, err := base.AddNote(f.BasePath, Note{
		Author: f.By,
		Body: fmt.Sprintf("Folded from layer %s (%s) on %s. Content sha256 %s. The layer's copy is unchanged.",
			f.Layer, f.Source, f.FoldedAt, f.Hash),
	}); err != nil {
		return err
	}
	src, err := r.Existing(f.Layer)
	if err != nil {
		return nil // the layer is gone; the provenance note still says where it came from
	}
	notes, err := src.Notes(f.Source)
	if err != nil {
		return nil
	}
	for _, n := range notes {
		n.ID = 0
		if _, err := base.AddNote(f.BasePath, n); err != nil {
			return err
		}
	}
	return nil
}

// sourceBytesPath is where a layer document's bytes are readable on this host:
// raglit's storage for an upload or attachment, else the document's own file.
func sourceBytesPath(st *Store, path string) (string, bool) {
	if p, ok := st.StoragePath(path); ok {
		return p, true
	}
	p := strings.TrimPrefix(path, "file://")
	if fi, err := os.Stat(p); err == nil && filepath.IsAbs(p) && fi.Mode().IsRegular() {
		return p, true
	}
	return "", false
}

// foldName is the file part of a folded document's name in the base: its
// storage path without the "uploads/" prefix, else its file name.
func foldName(st *Store, path string) string {
	if rel, ok := st.StorageRel(strings.TrimPrefix(path, "file://")); ok {
		return filepath.ToSlash(strings.TrimPrefix(rel, "uploads"+string(filepath.Separator)))
	}
	return filepath.Base(strings.TrimPrefix(path, "file://"))
}

// docTitle is a document's caption, else its title.
func docTitle(st *Store, path string) string {
	if id, err := st.DocumentIdentity(path); err == nil && id.Name != "" {
		return id.Name
	}
	var t string
	_ = st.db.QueryRow(`SELECT title FROM documents WHERE path = ?`, path).Scan(&t)
	return t
}

// foldable reports whether a layer is public and readable. With access control
// off there are no ACLs to say so, and the public layers are the ones register
// named "-pub" (layerNames).
func (r *Registry) foldable(ctx context.Context, layer string) bool {
	if !r.ACLEnabled() {
		return strings.HasSuffix(layer, "-pub")
	}
	return r.ACLOf(layer).Visibility == Public && r.CanRead(ctx, layer)
}
