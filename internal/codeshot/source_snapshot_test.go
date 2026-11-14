package codeshot

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// One graph, written both as a codegraph index and as the snapshot graph-assay
// exports from it. It covers every rule FileEdgesFromIndexRows applies: an
// imports-only target (not drawn), a file-kind target (not drawn), a
// non-callee kind (contains), a same-file edge, and a target reached twice.
var testGraph = struct{ nodes, edges [][]string }{
	nodes: [][]string{ // id, kind, name, qname, file
		{"f1", "file", "a.go", "a.go", "a.go"},
		{"f2", "file", "b.go", "b.go", "b.go"},
		{"A", "function", "A", "A", "a.go"},
		{"R", "method", "Run", "T.Run", "a.go"},
		{"B", "function", "B", "B", "b.go"},
		{"C", "function", "C", "C", "c.go"},
	},
	edges: [][]string{ // src, dst, kind
		{"A", "B", "calls"}, {"A", "C", "imports"}, {"A", "f2", "references"},
		{"f1", "A", "contains"}, {"A", "R", "calls"}, {"R", "C", "calls"},
		{"R", "B", "instantiates"}, {"R", "B", "calls"}, {"B", "A", "references"},
	},
}

func testSnapshot(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	s := &snapshot.Snapshot{Schema: snapshot.Schema}
	for _, n := range testGraph.nodes {
		qn := n[3]
		s.Nodes = append(s.Nodes, snapshot.Node{ID: n[0], Kind: n[1], Name: n[2], QName: &qn, File: n[4], Start: 1, End: 1})
	}
	for _, e := range testGraph.edges {
		s.Edges = append(s.Edges, snapshot.Edge{Src: e[0], Dst: e[1], Kind: e[2]})
	}
	return s
}

func writeTestIndex(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".codegraph")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "codegraph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, kind TEXT, name TEXT, qualified_name TEXT, file_path TEXT)`,
		`CREATE TABLE edges (source TEXT, target TEXT, kind TEXT)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range testGraph.nodes {
		if _, err := db.Exec(`INSERT INTO nodes VALUES (?,?,?,?,?)`, n[0], n[1], n[2], n[3], n[4]); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range testGraph.edges {
		if _, err := db.Exec(`INSERT INTO edges VALUES (?,?,?)`, e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotSourceDrawsTheSameEdgesAsReadingTheIndexItCameFrom(t *testing.T) {
	root := t.TempDir()
	writeTestIndex(t, root)
	src := SnapshotSource{Snap: testSnapshot(t)}
	symbols, err := src.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	fromIndex, err := collectFileEdges(symbols, root)
	if err != nil {
		t.Fatal(err)
	}
	fromSnap, err := src.FileEdges(symbols)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromIndex, fromSnap) {
		t.Errorf("index read %v, snapshot %v", fromIndex, fromSnap)
	}
	// Symbols are walked by file, then name: A, Run, then b.go's B.
	want := []SymbolEdge{{"a.go", "b.go"}, {"a.go", "a.go"}, {"a.go", "c.go"}, {"a.go", "b.go"}, {"b.go", "a.go"}}
	if !reflect.DeepEqual(fromSnap, want) {
		t.Errorf("snapshot edges %v, want %v", fromSnap, want)
	}
}

// Without a qualified name, two same-named nodes in one file stay two symbols:
// the node id is the identity, so neither borrows the other's edges.
func TestSnapshotSourceKeepsSameNamedNodesWithoutAQualifiedNameApart(t *testing.T) {
	s := &snapshot.Snapshot{Schema: snapshot.Schema,
		Nodes: []snapshot.Node{
			{ID: "1", Kind: "method", Name: "String", File: "a.go"},
			{ID: "2", Kind: "method", Name: "String", File: "a.go"},
			{ID: "3", Kind: "function", Name: "F", File: "b.go"},
		},
		Edges: []snapshot.Edge{{Kind: "calls", Src: "2", Dst: "3"}},
	}
	src := SnapshotSource{Snap: s}
	symbols, _ := src.Symbols()
	edges, _ := src.FileEdges(symbols[:1]) // only node 1 enumerated
	if len(edges) != 0 {
		t.Errorf("node 1 drew node 2's edge: %v", edges)
	}
	edges, _ = src.FileEdges(symbols)
	if want := []SymbolEdge{{"a.go", "b.go"}}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges %v, want %v", edges, want)
	}
}

func writeSnapshotFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const cliSnapshot = `{"schema":"graph-assay/snapshot@1","subject":{"name":"t"},
 "capabilities":{"siteLines":false,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[],"unresolved":[],
 "nodes":[{"id":"1","kind":"function","name":"A","qname":"A","file":"src/a.go","start":1,"end":2},
          {"id":"2","kind":"function","name":"B","qname":"B","file":"lib/b.go","start":1,"end":2}],
 "edges":[{"kind":"calls","src":"1","dst":"2"}]}`

// A snapshot needs no codegraph: PATH holds only graphviz.
func TestCLIArchitectureFromASnapshotNeedsNoCodegraph(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	out := filepath.Join(t.TempDir(), "arch.dot")
	code, _, stderr := runCLI("--architecture", "--source", "snapshot:"+writeSnapshotFile(t, cliSnapshot), "--path", t.TempDir(), "--format", "dot", "--out", out, "--forbid", "src/->lib/")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (the --forbid rule is broken); stderr: %s", code, stderr)
	}
	wantMatch(t, `1 file edge\(s\) break a --forbid rule`, stderr)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wantMatch(t, `"src/a\.go" -> "lib/b\.go"`, string(got))
}

func TestCLISourceIsValidatedBeforeAnyWork(t *testing.T) {
	wantRejected(t, `--source must be 'codegraph' or 'snapshot:<file>', got 'scip'`, "--architecture", "--source", "scip")
	wantRejected(t, `--source must be 'codegraph' or 'snapshot:<file>', got 'snapshot:'`, "--architecture", "--source", "snapshot:")
	wantRejected(t, `works with --architecture only so far`, "Foo", "--source", "snapshot:x.json")
	wantRejected(t, `works with --architecture only so far`, "--diff", "--source", "snapshot:x.json")
	wantRejected(t, `could not read snapshot '.*': schema is "graph-assay/snapshot@2"`, "--architecture", "--source",
		"snapshot:"+writeSnapshotFile(t, strings.Replace(cliSnapshot, "@1", "@2", 1)))
	wantRejected(t, `could not read snapshot 'missing\.json'`, "--architecture", "--source", "snapshot:missing.json")
}
