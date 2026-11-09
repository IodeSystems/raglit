package raglit

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Removing a document while ingest workers write must not fail with
// SQLITE_BUSY. The workers here commit read-then-write transactions the way
// commitDoc does; the removals run against them.
func TestBusy_RemoveWhileWritersCommit(t *testing.T) {
	root := t.TempDir()
	reg, err := OpenScopedRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	main, _ := reg.Get("main")
	must(t, main.Ingest(context.Background(), Document{Path: "file:///seed.md", Title: "S", Fragments: []Fragment{{Text: "seed"}}}))
	must(t, reg.ForkBranch("feat", "main"))
	feat, _ := reg.Get("feat")

	plain, err := Open(t.TempDir() + "/plain.db")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()

	for name, s := range map[string]*Store{"branch": feat, "plain": plain} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var stop atomic.Bool
			var wg sync.WaitGroup
			var writerErrs atomic.Int64
			for w := 0; w < 2; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; !stop.Load(); i++ {
						if i%50 == 0 {
							p := fmt.Sprintf("file:///w%d-%d.md", w, i%20)
							if err := s.Ingest(ctx, Document{Path: p, Title: "W", Fragments: []Fragment{{Text: "worker text"}}}); err != nil {
								writerErrs.Add(1)
							}
						}
						_ = s.writeTx(ctx, func(c *sql.Conn) error {
							var n int
							if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&n); err != nil {
								return err
							}
							_, err := c.ExecContext(ctx, `INSERT INTO index_meta(key, value, updated_at) VALUES('busy_test', ?, 0)
								ON CONFLICT(key) DO UPDATE SET value = excluded.value`, n)
							return err
						})
					}
				}(w)
			}
			failed := 0
			for i := 0; i < 200; i++ {
				p := fmt.Sprintf("file:///del-%d.md", i)
				must(t, s.Ingest(ctx, Document{Path: p, Title: "D", Fragments: []Fragment{{Text: "doomed"}}}))
				_ = s.SetTags(p, []string{"x"})
				var err error
				if i%2 == 0 {
					err = s.DeleteDocument(p)
				} else {
					err = s.RemoveDocument(p, false)
				}
				if err != nil {
					failed++
					if failed <= 3 {
						t.Errorf("remove %d: %v", i, err)
					}
				}
			}
			stop.Store(true)
			wg.Wait()
			if failed > 0 {
				t.Errorf("%d of 200 removals failed", failed)
			}
		})
	}
}

// A transaction whose first statement is a write still read first when that
// statement's prepare connects a virtual table: deleting a document cascades
// to fragments, whose trigger writes fragments_fts, and FTS5's xConnect reads
// fragments_fts_config. Inside a deferred BEGIN that read opens a read
// transaction, and the write that follows cannot wait for the lock: SQLite
// runs the busy handler only for a connection with no transaction open, so
// the DELETE fails with SQLITE_BUSY at once while another connection holds
// the write lock. A connection connects the table once, so this is a fresh
// pooled connection's first such statement (SetMaxIdleConns(0) makes every
// connection fresh). The store's transactions take the write lock at BEGIN
// and wait for it under busy_timeout instead.
func TestBusy_FirstWriteOnAFreshConnectionWaits(t *testing.T) {
	ctx := context.Background()
	for name, remove := range map[string]func(s *Store, path string) error{
		"Begin": func(s *Store, path string) error {
			tx, err := s.db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback() //nolint:errcheck // no-op after Commit
			if _, err := tx.Exec(`DELETE FROM documents WHERE path = ?`, path); err != nil {
				return err
			}
			return tx.Commit()
		},
		"DeleteDocument": (*Store).DeleteDocument,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := Open(t.TempDir() + "/i.db")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const p = "file:///doomed.md"
			must(t, s.Ingest(ctx, Document{Path: p, Title: "D", Fragments: []Fragment{{Text: "doomed text"}}}))
			s.db.SetMaxIdleConns(0)

			holder, err := s.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- remove(s, p) }()
			time.Sleep(200 * time.Millisecond)
			if _, err := holder.ExecContext(ctx, `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("remove while another connection held the write lock: %v", err)
			}
			if h, _ := s.DocumentHash(p); h != "" {
				t.Errorf("document still there after the remove")
			}
		})
	}
}
