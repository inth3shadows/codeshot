package codegraph

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIndex creates a minimal codegraph-shaped index in WAL mode, as real
// codegraph indexes are, with one call edge a.go:A -> b.go:B.
func writeIndex(t *testing.T, dbPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, kind TEXT, name TEXT, qualified_name TEXT NOT NULL, file_path TEXT)`,
		`CREATE TABLE edges (source TEXT, target TEXT, kind TEXT)`,
		`INSERT INTO nodes VALUES ('a','function','A','A','a.go'), ('b','function','B','B','b.go')`,
		`INSERT INTO edges VALUES ('a','b','calls')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// F1: a Windows path has no leading slash; as a URI path it must still be
// file:///C:/..., never file://C:/... (SQLite reads "C:" as an authority).
func TestIndexURIKeepsAWindowsDriveInThePath(t *testing.T) {
	got := indexURI("C:/Users/me/proj/.codegraph/codegraph.db", false)
	if !strings.HasPrefix(got, "file:///C:/Users/me/proj/.codegraph/codegraph.db?") {
		t.Errorf("indexURI(windows path) = %q; want file:///C:/... with no authority", got)
	}
	got = indexURI("/home/me/a b#c%d?e/.codegraph/codegraph.db", false)
	if !strings.HasPrefix(got, "file:///home/me/a%20b%23c%25d%3Fe/.codegraph/codegraph.db?") {
		t.Errorf("indexURI(posix path with specials) = %q; want an escaped file:/// path", got)
	}
}

func TestReadIndexRowsReadsAPathWithURISpecialCharacters(t *testing.T) {
	db := filepath.Join(t.TempDir(), "a b#c%d", ".codegraph", "codegraph.db")
	writeIndex(t, db)
	rows, err := ReadIndexRows(db)
	if err != nil || len(rows) != 1 || rows[0].TargetFile != "b.go" {
		t.Fatalf("ReadIndexRows = %+v, %v; want one a.go -> b.go row", rows, err)
	}
}

// F2: a WAL-mode index in a directory codeshot can't write (a read-only mount,
// a CI cache) has no -shm to create. With nothing un-checkpointed in the WAL
// the main file is complete, so the read must still succeed.
func TestReadIndexRowsReadsAWALIndexInAReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), ".codegraph")
	db := filepath.Join(dir, "codegraph.db")
	writeIndex(t, db)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	rows, err := ReadIndexRows(db)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ReadIndexRows(read-only dir) = %d rows, %v; want 1 row", len(rows), err)
	}
}

// F2: a missing table is a schema problem; anything else is reported as what
// it is, not blamed on the schema.
func TestReadIndexRowsClassifiesSchemaErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other (x)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := ReadIndexRows(empty); !IsSchemaError(err) {
		t.Errorf("missing tables: IsSchemaError(%v) = false; want true", err)
	}
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a database ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndexRows(junk); err == nil || IsSchemaError(err) {
		t.Errorf("corrupt file: err = %v, IsSchemaError = %v; want a non-schema error", err, IsSchemaError(err))
	}
}
