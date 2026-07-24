package db_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/iodesystems/raglit/internal/db"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery"
	"github.com/iodesystems/sqlc-go-codegen-metaquery/metaquery/mqsqlite"

	_ "modernc.org/sqlite"
)

// openSchema opens an in-memory DB and applies the canonical schema — proving
// sqlc's codegen schema (sql/schema.sql, incl. the fts5 virtual table + triggers)
// also runs on the pure-Go modernc driver at runtime.
func openSchema(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("../../sql/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(ddl)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return conn
}

func TestGenerated_PlainQueries(t *testing.T) {
	conn := openSchema(t)
	defer conn.Close()
	ctx := context.Background()
	q := db.New(conn)

	id, err := q.EnqueueJob(ctx, db.EnqueueJobParams{Url: "file:///a", Title: "A", EnqueuedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	j, err := q.GetJob(ctx, id)
	if err != nil || j.Url != "file:///a" || j.State != "pending" {
		t.Fatalf("GetJob = %+v, %v", j, err)
	}
	if _, err := q.UpsertDocument(ctx, db.UpsertDocumentParams{Path: "file:///d", Title: "D", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	docs, err := q.ListDocumentSummaries(ctx)
	if err != nil || len(docs) != 1 || docs[0].Path != "file:///d" {
		t.Fatalf("ListDocumentSummaries = %+v, %v", docs, err)
	}
}

// TestGenerated_MetaqueryBuilder proves the metaquery runtime path end-to-end:
// a generated Wrap* Builder + dynamic filter, executed via mqsqlite on modernc.
func TestGenerated_MetaqueryBuilder(t *testing.T) {
	conn := openSchema(t)
	defer conn.Close()
	ctx := context.Background()
	q := db.New(conn)
	for _, u := range []string{"file:///alpha", "file:///bravo", "file:///charlie"} {
		if _, err := q.EnqueueJob(ctx, db.EnqueueJobParams{Url: u, EnqueuedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Dynamic filter on top of the generated ListJobs query.
	b := db.WrapListJobs().ApplyFilters([]metaquery.Filter{
		{Column: "url", Op: metaquery.OpLike, Value: "%bravo%"},
	})
	res, err := mqsqlite.Scan[db.IngestJob](ctx, conn, b)
	if err != nil {
		t.Fatalf("mqsqlite.Scan: %v", err)
	}
	if len(res.Data) != 1 || res.Data[0].Url != "file:///bravo" {
		t.Fatalf("filtered jobs = %+v, want just bravo", res.Data)
	}
}
