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

// ReadSiteLines: the smallest edge line per file pair, the caller's start
// line when the edge has none; imports, file targets and same-file edges
// are left out.
func TestReadSiteLinesTakesTheFirstCallSitePerFilePair(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), ".codegraph", "codegraph.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, kind TEXT, name TEXT, qualified_name TEXT NOT NULL, file_path TEXT, start_line INTEGER)`,
		`CREATE TABLE edges (source TEXT, target TEXT, kind TEXT, line INTEGER)`,
		`INSERT INTO nodes VALUES ('a1','function','A1','A1','a.go',3), ('a2','function','A2','A2','a.go',40),
		  ('b','function','B','B','b.go',1), ('c','function','C','C','c.go',1), ('f','file','d.go','d.go','d.go',1)`,
		`INSERT INTO edges VALUES ('a2','b','calls',44), ('a1','b','calls',12), ('a1','b','imports',1),
		  ('a2','c','calls',NULL), ('a1','f','calls',2), ('a1','a2','calls',5)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	got, err := ReadSiteLines(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[FilePair]int{{From: "a.go", To: "b.go"}: 12, {From: "a.go", To: "c.go"}: 40}
	if len(got) != len(want) || got[FilePair{"a.go", "b.go"}] != (SiteLine{12, true}) || got[FilePair{"a.go", "c.go"}] != (SiteLine{40, false}) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// F3: a real call line beats the caller's start line, whatever the order,
// and a 0 is no line.
func TestReadSiteLinesPrefersARealCallLineAndIgnoresZero(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), ".codegraph", "codegraph.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, kind TEXT, name TEXT, qualified_name TEXT NOT NULL, file_path TEXT, start_line INTEGER)`,
		`CREATE TABLE edges (source TEXT, target TEXT, kind TEXT, line INTEGER)`,
		`INSERT INTO nodes VALUES ('a1','function','A1','A1','a.go',40), ('a0','file','a.go','a.go','a.go',0),
		  ('b','function','B','B','b.go',1), ('c','function','C','C','c.go',1), ('d','function','D','D','d.go',1)`,
		`INSERT INTO edges VALUES ('a1','b','calls',55), ('a1','b','references',NULL),
		  ('a0','c','calls',0), ('a1','c','calls',12),
		  ('a1','d','references',NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	got, err := ReadSiteLines(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for pair, want := range map[FilePair]SiteLine{{"a.go", "b.go"}: {55, true}, {"a.go", "c.go"}: {12, true}, {"a.go", "d.go"}: {40, false}} {
		if got[pair] != want {
			t.Errorf("%v: got %v, want %v", pair, got[pair], want)
		}
	}
}

// V6: an index with node start lines but no edge line column still gives
// the start-line fallback.
func TestReadSiteLinesFallsBackWhenEdgesHaveNoLineColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), ".codegraph", "codegraph.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, kind TEXT, name TEXT, qualified_name TEXT NOT NULL, file_path TEXT, start_line INTEGER)`,
		`CREATE TABLE edges (source TEXT, target TEXT, kind TEXT)`,
		`INSERT INTO nodes VALUES ('a','function','A','A','a.go',7), ('b','function','B','B','b.go',1)`,
		`INSERT INTO edges VALUES ('a','b','calls')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	got, err := ReadSiteLines(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got[FilePair{"a.go", "b.go"}] != (SiteLine{7, false}) {
		t.Errorf("got %v, want a.go -> b.go at start line 7", got)
	}
}
