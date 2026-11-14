package codegraph

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite: no cgo, so release binaries cross-compile statically
)

// CalleeEdgeKinds are codegraph's own callee edge kinds (getCallees,
// src/graph/traversal.ts).
var CalleeEdgeKinds = []string{"calls", "references", "imports", "instantiates", "navigates"}

// nullSentinel stands in for a SQL NULL text column, so a NULL never compares
// equal to an empty string from the CLI's JSON.
const nullSentinel = "\x00NULL"

// IndexRow is one callee edge from any node: the source's definition identity
// (name, file, qualified name) and the target's id, kind and file.
type IndexRow struct {
	Name, FilePath, QualifiedName string
	Target                        string
	EdgeKind, TargetKind          string
	TargetFile                    string
}

// FindIndex returns the codegraph.db that governs repoPath, walking up from it
// the way the codegraph CLI resolves --path, so a --path below the project
// root reads the same index the CLI enumerated from.
func FindIndex(repoPath string) (string, error) {
	dir, err := filepath.Abs(repoPath)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, ".codegraph", "codegraph.db")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .codegraph/codegraph.db at or above '%s'", repoPath)
		}
		dir = parent
	}
}

// indexURI is the read-only SQLite URI for dbPath. The path always starts with
// "/" so a Windows drive (C:/...) stays in the path instead of being read as a
// URI authority. busy_timeout waits out a codegraph write instead of failing.
// immutable=1 skips SQLite's locking and -shm file entirely; it is only safe
// when nothing is waiting in the WAL (see ReadIndexRows).
func indexURI(dbPath string, immutable bool) string {
	p := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	q := "mode=ro&_pragma=busy_timeout(5000)"
	if immutable {
		q += "&immutable=1"
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q}).String()
}

// IsSchemaError reports an index whose tables or columns aren't the ones
// codeshot reads, i.e. a codegraph build with a different schema.
func IsSchemaError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column")
}

// walIsEmpty reports that dbPath's write-ahead log holds nothing, so the main
// file alone is the whole index.
func walIsEmpty(dbPath string) bool {
	fi, err := os.Stat(dbPath + "-wal")
	return os.IsNotExist(err) || (err == nil && fi.Size() == 0)
}

// ReadIndexRows reads every callee-kind edge from a codegraph.db, read-only,
// in one query. codegraph keeps its index in WAL mode, and a read-only open
// still needs to create the -shm file beside it, which fails in a directory
// codeshot can't write (a read-only mount, a restored CI cache). When the WAL
// is empty the read is retried as immutable, which needs no -shm and is exact;
// with pending WAL content it is not retried, since immutable would silently
// miss those edges.
func ReadIndexRows(dbPath string) ([]IndexRow, error) {
	rows, err := readIndexRows(indexURI(dbPath, false))
	if err != nil && !IsSchemaError(err) && walIsEmpty(dbPath) {
		if retry, rerr := readIndexRows(indexURI(dbPath, true)); rerr == nil {
			return retry, nil
		}
	}
	return rows, err
}

func readIndexRows(uri string) ([]IndexRow, error) {
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(CalleeEdgeKinds)), ", ")
	args := make([]any, len(CalleeEdgeKinds))
	for i, k := range CalleeEdgeKinds {
		args[i] = k
	}
	rows, err := db.Query(`SELECT s.name, s.file_path, s.qualified_name, e.target, e.kind, t.kind, t.file_path
	   FROM edges e
	   JOIN nodes s ON s.id = e.source
	   JOIN nodes t ON t.id = e.target
	  WHERE e.kind IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []IndexRow
	for rows.Next() {
		var name, file, qn, ek, tk, tf sql.NullString
		var target any
		if err := rows.Scan(&name, &file, &qn, &target, &ek, &tk, &tf); err != nil {
			return nil, err
		}
		out = append(out, IndexRow{
			Name: str(name), FilePath: str(file), QualifiedName: str(qn),
			Target:   fmt.Sprint(target),
			EdgeKind: str(ek), TargetKind: str(tk), TargetFile: nullable(tf),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		// An index with zero callee edges is legitimate; distinguish it from a failed read.
		return []IndexRow{}, nil
	}
	return out, nil
}

func str(s sql.NullString) string {
	if !s.Valid {
		return nullSentinel
	}
	return s.String
}

// nullable maps NULL to "", for a target file, so it reads as "no file" and is
// dropped like any other edge without a real file.
func nullable(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

// ErrNoIdentity means the enumerated symbols lack qualifiedName (codegraph
// before 1.6.0), so they can't be matched to index rows.
var ErrNoIdentity = errors.New("symbols carry no qualifiedName")
