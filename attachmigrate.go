package raglit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Moving extracted attachments out of somebody's corpus and into raglit's own
// storage.
//
// Extraction used to write <archive>.raglit-attachments/ beside the archive:
// 204 files across 44 directories in one legal evidence tree, for a feature
// raglit owns end to end. New extractions land in Home.AttachmentDir. This moves
// the ones already there.
//
// It is not a file move. An extracted attachment is an indexed DOCUMENT — it has
// fragments, a caption, a history, possibly notes somebody wrote — and its path
// is its identity. Moving the bytes and leaving the row behind turns 204
// documents into `missing-file`; re-ingesting them at the new path creates 204
// new rows and abandons everything anybody recorded about the old ones. So the
// row is rewritten in place, and everything else keyed by the path moves with
// it: originals/ and pages/ are both named by tag(docPath) (see Home), so a
// document whose path changes loses its stored original and its page images
// unless they are renamed too. That is the part which is easy to miss and
// silent when missed.

// AttachmentMove is one document this migration would move, or moved.
type AttachmentMove struct {
	Archive string `json:"archive"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	// Err is set when this one could not be moved. The migration continues: a
	// half-moved corpus with a report is recoverable, a migration that stops at
	// the first bad file leaves nobody knowing which side of it they are on.
	Err string `json:"error,omitempty"`
}

// MigrateExtractedAttachments moves this index's corpus-side attachment
// directories into raglit's storage, rewriting the document rows to match.
//
// dryRun reports what it would do and touches nothing — the only responsible
// default for something that renames files in an evidence tree.
func (s *Store) MigrateExtractedAttachments(dryRun bool) ([]AttachmentMove, error) {
	if !s.withHome {
		return nil, fmt.Errorf("raglit: this index has no home to migrate attachments into")
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM documents WHERE path LIKE '%'||?||'/%' ORDER BY path`, attachmentDirSuffix)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []AttachmentMove
	for _, old := range paths {
		// <archive>.raglit-attachments/<file> → the archive is everything before
		// the suffix, which is how the sidecar was named in the first place.
		i := strings.Index(old, attachmentDirSuffix+string(filepath.Separator))
		if i < 0 {
			continue
		}
		archive := old[:i]
		file := filepath.Base(old)
		dest := s.home.AttachmentDir(archive)
		mv := AttachmentMove{Archive: archive, OldPath: old, NewPath: filepath.Join(dest, file)}
		if !dryRun {
			if err := s.moveAttachment(ctx, mv); err != nil {
				mv.Err = err.Error()
			}
		}
		out = append(out, mv)
	}
	if !dryRun {
		s.sweepLegacyAttachmentDirs(ctx, out)
	}
	return out, nil
}

// moveAttachment moves one attachment's bytes and everything keyed by its path.
func (s *Store) moveAttachment(ctx context.Context, mv AttachmentMove) error {
	if err := os.MkdirAll(filepath.Dir(mv.NewPath), 0o755); err != nil {
		return err
	}
	// The file itself. A destination that already holds it is not an error —
	// re-running the migration must be safe, because the first run may have died
	// halfway and the only way to find out is to run it again.
	if _, err := os.Stat(mv.NewPath); err != nil {
		if err := os.Rename(mv.OldPath, mv.NewPath); err != nil {
			// Across filesystems rename fails; copy then remove.
			b, rerr := os.ReadFile(mv.OldPath)
			if rerr != nil {
				return rerr
			}
			if werr := os.WriteFile(mv.NewPath, b, 0o644); werr != nil {
				return werr
			}
			_ = os.Remove(mv.OldPath)
		}
	}
	// Everything else raglit keyed by the OLD path. Both are derived names, not
	// stored ones, so a row whose path changes silently stops finding them.
	for _, d := range [][2]string{
		{s.home.OriginalPath(mv.OldPath), s.home.OriginalPath(mv.NewPath)},
		{s.home.PageDir(mv.OldPath), s.home.PageDir(mv.NewPath)},
	} {
		if _, err := os.Stat(d[0]); err == nil {
			_ = os.Rename(d[0], d[1])
		}
	}
	// The row last: if anything above failed we have not yet claimed the document
	// lives somewhere it does not.
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET path = ? WHERE path = ?`, mv.NewPath, mv.OldPath)
	return err
}

// sweepLegacyAttachmentDirs empties every corpus sidecar this index knows about
// and removes the directory.
//
// Everything left, not only the indexed files. Of 204 files in one corpus only
// 72 were documents — the rest are the MANIFEST, and attachments that were
// extracted and never ingested (a duplicate absorbed by content dedup, or a file
// nothing queued). Moving the 72 and leaving 132 behind would satisfy the
// database and miss the point: the corpus is meant to stop holding raglit's
// working files.
//
// Driven from the indexed ARCHIVES as well as the moved attachments, because an
// archive whose attachments were never ingested has no rows to find it by and
// its sidecar would otherwise be invisible to this.
func (s *Store) sweepLegacyAttachmentDirs(ctx context.Context, moves []AttachmentMove) {
	archives := map[string]bool{}
	for _, mv := range moves {
		if mv.Err == "" {
			archives[mv.Archive] = true
		}
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM documents WHERE lower(path) LIKE '%.eml' OR lower(path) LIKE '%.mbox'`)
	if err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				archives[p] = true
			}
		}
		rows.Close()
	}

	for archive := range archives {
		oldDir := LegacyAttachmentDir(archive)
		entries, err := os.ReadDir(oldDir)
		if err != nil {
			continue
		}
		newDir := s.home.AttachmentDir(archive)
		if err := os.MkdirAll(newDir, 0o755); err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue // not something extraction writes; leave it and say so by leaving the dir
			}
			from := filepath.Join(oldDir, e.Name())
			to := filepath.Join(newDir, e.Name())
			if _, err := os.Stat(to); err == nil {
				_ = os.Remove(from) // already there, byte-for-byte the same name
				continue
			}
			_ = os.Rename(from, to)
		}
		// Remove only if now empty. Never RemoveAll: this is somebody's corpus,
		// and a directory that still holds something is a fact worth keeping.
		_ = os.Remove(oldDir)
	}
}
