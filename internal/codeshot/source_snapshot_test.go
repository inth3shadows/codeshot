package codeshot

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/codegraph"
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
	// Symbols are walked by file, then name (A, Run, then b.go's B); each
	// one's targets by target file.
	want := []SymbolEdge{{"a.go", "a.go"}, {"a.go", "b.go"}, {"a.go", "b.go"}, {"a.go", "c.go"}, {"b.go", "a.go"}}
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
	wantRejected(t, `--source must be 'codegraph', 'snapshot:<file>' or 'scip\[:<file>\]', got 'snapshot:'`, "--architecture", "--source", "snapshot:")
	wantRejected(t, `could not read snapshot 'x\.json'`, "Foo", "--source", "snapshot:x.json")
	wantRejected(t, `could not read snapshot 'x\.json'`, "--diff", "--source", "snapshot:x.json")
	wantRejected(t, `could not read snapshot '.*': schema is "graph-assay/snapshot@2"`, "--architecture", "--source",
		"snapshot:"+writeSnapshotFile(t, strings.Replace(cliSnapshot, "@1", "@2", 1)))
	wantRejected(t, `could not read snapshot 'missing\.json'`, "--architecture", "--source", "snapshot:missing.json")
}

// F1: the codegraph index read returns rows in SQLite's plan order and a
// snapshot in its file order. The edges must not depend on either, or the
// two sources draw the same graph in different statement orders.
func TestFileEdgesDoNotDependOnRowOrder(t *testing.T) {
	q := func(s string) *string { return &s }
	symbols := []Node{{Name: "A", FilePath: "a.go", QualifiedName: q("A")}, {Name: "B", FilePath: "b.go", QualifiedName: q("B")}}
	rows := []codegraph.IndexRow{
		{Name: "A", FilePath: "a.go", QualifiedName: "A", Target: "z", EdgeKind: "calls", TargetKind: "function", TargetFile: "z.go"},
		{Name: "A", FilePath: "a.go", QualifiedName: "A", Target: "c", EdgeKind: "calls", TargetKind: "function", TargetFile: "c.go"},
		{Name: "A", FilePath: "a.go", QualifiedName: "A", Target: "m", EdgeKind: "references", TargetKind: "function", TargetFile: "m.go"},
		{Name: "B", FilePath: "b.go", QualifiedName: "B", Target: "c", EdgeKind: "calls", TargetKind: "function", TargetFile: "c.go"},
	}
	reversed := slices.Clone(rows)
	slices.Reverse(reversed)
	got, back := FileEdgesFromIndexRows(symbols, rows), FileEdgesFromIndexRows(symbols, reversed)
	if !reflect.DeepEqual(got, back) {
		t.Errorf("row order changed the edges: %v vs %v", got, back)
	}
	want := []SymbolEdge{{"a.go", "c.go"}, {"a.go", "m.go"}, {"a.go", "z.go"}, {"b.go", "c.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v (each symbol's targets by file)", got, want)
	}
}

// P2c-1: codegraph and snapshots feed every mode; SCIP only --architecture.
// F7: cheap flag checks run before the snapshot is read, so a typo is
// reported as itself, not as a snapshot error.
func TestCLIFlagTyposAreReportedBeforeTheSnapshotIsRead(t *testing.T) {
	wantRejected(t, `--forbid expects FROM->TO`, "--architecture", "--source", "snapshot:missing.json", "--forbid", "bad")
	wantRejected(t, `--embed target 'missing\.md' does not exist`, "--architecture", "--source", "snapshot:missing.json", "--embed", "missing.md")
}

const sparseSnapshot = `{"schema":"graph-assay/snapshot@1","subject":{"name":"cgr"},
 "capabilities":{"siteLines":false,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[],"unresolved":[],
 "nodes":[{"id":"f","kind":"file","name":"a.go","qname":"a.go","file":"a.go","start":1,"end":9},
          {"id":"1","kind":"function","name":"A","file":"a.go","start":1,"end":2},
          {"id":"2","kind":"function","name":"B","file":"b.go","start":1,"end":2}],
 "edges":[{"kind":"contains","src":"f","dst":"1"},{"kind":"uses","src":"1","dst":"2"},
          {"kind":"overrides","src":"1","dst":"2"},{"kind":"uses","src":"2","dst":"1"}]}`

// F4 + F6: a snapshot whose only cross-file edges are kinds codeshot doesn't
// draw says so, and no warning sends the user to codegraph.
func TestCLISnapshotWarningsNameTheSnapshotNotCodegraph(t *testing.T) {
	requireBins(t, "dot")
	out := filepath.Join(t.TempDir(), "arch.dot")
	code, _, stderr := runCLI("--architecture", "--source", "snapshot:"+writeSnapshotFile(t, sparseSnapshot), "--format", "dot", "--out", out, "--limit", "5")
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantNoMatch(t, `codegraph`, stderr)
	wantMatch(t, `--limit has no effect with --architecture \(every edge is read from the snapshot\)`, stderr)
	wantMatch(t, `edge kinds --architecture doesn't draw: overrides \(1\), uses \(2\)`, stderr)
	wantMatch(t, `the diagram is blank\. The cgr snapshot has no calls, instantiates, navigates or references edges between files \(an imports edge counts only alongside one of those\)`, stderr)
}

// F3: a snapshot diagram gets its own --embed marker and image name, so it can
// sit in one doc beside the codegraph diagram without overwriting it.
func TestCLISnapshotEmbedGetsItsOwnMarkerAndImageName(t *testing.T) {
	requireBins(t, "dot")
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := writeSnapshotFile(t, cliSnapshot)
	for _, c := range []struct {
		extra  []string
		marker string
	}{{nil, "arch-snapshot-t"}, {[]string{"--group-depth", "1"}, "arch-d1-snapshot-t"}} {
		args := append([]string{"--architecture", "--source", "snapshot:" + snap, "--path", dir, "--format", "svg", "--embed", doc}, c.extra...)
		if code, _, stderr := runCLI(args...); code != 0 {
			t.Fatalf("%v: exit %d; stderr: %s", c.extra, code, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "codeshot-"+c.marker+".svg")); err != nil {
			t.Errorf("%v: image: %v", c.extra, err)
		}
		md, _ := os.ReadFile(doc)
		wantMatch(t, `<!-- codeshot:`+c.marker+`:start -->`, string(md))
	}
}

// F10: same file and name is not a tie; the qualified name decides, so two
// sources listing the same nodes in different orders enumerate them alike.
func TestSortSymbolsBreaksSameFileSameNameTiesByQualifiedName(t *testing.T) {
	q := func(s string) *string { return &s }
	a := Node{Name: "String", FilePath: "a.go", QualifiedName: q("T2.String")}
	b := Node{Name: "String", FilePath: "a.go", QualifiedName: q("T1.String")}
	c := Node{Name: "String", FilePath: "a.go"} // no qualified name sorts first
	for _, in := range [][]Node{{a, b, c}, {c, b, a}, {b, c, a}} {
		got := SortSymbolsForEnumeration(in)
		if got[0].QualifiedName != nil || *got[1].QualifiedName != "T1.String" || *got[2].QualifiedName != "T2.String" {
			t.Errorf("order from %v: %v", in, got)
		}
	}
}

// F12: the tool name becomes a filename and a markdown link target, so only
// filename-and-link-safe characters survive, and the length is capped.
func TestSnapshotMarkerSuffixIsSafeForFilenamesAndLinks(t *testing.T) {
	for name, want := range map[string]string{
		"codegraph":              "snapshot-codegraph",
		"code-graph-rag":         "snapshot-code-graph-rag",
		"":                       "snapshot",
		"my tool":                "snapshot-my-tool",
		"a]b(c)\nd":              "snapshot-a-b-c-d",
		"../x":                   "snapshot-..-x",
		"  ":                     "snapshot",
		strings.Repeat("a", 300): "snapshot-" + strings.Repeat("a", 40),
	} {
		got := SnapshotMarkerSuffix(&snapshot.Snapshot{Subject: snapshot.Subject{Name: name}})
		if got != want {
			t.Errorf("SnapshotMarkerSuffix(%q) = %q, want %q", name, got, want)
		}
	}
}

// F13: the warning must not claim imports are drawn on their own.
func TestSnapshotEmptyWarningSaysImportsCountOnlyAlongsideAnotherKind(t *testing.T) {
	w := SnapshotSource{Snap: &snapshot.Snapshot{Subject: snapshot.Subject{Name: "t"}}}.EmptyGraphWarning()
	wantMatch(t, `no calls, instantiates, navigates or references edges between files \(an imports edge counts only alongside one of those\)`, w)
}

// F14: --path means nothing to a snapshot, so it doesn't name the image.
func TestCLISnapshotImageNameIgnoresPath(t *testing.T) {
	requireBins(t, "dot", "git")
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	sub := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(root, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("--architecture", "--source", "snapshot:"+writeSnapshotFile(t, cliSnapshot), "--path", sub, "--format", "svg", "--embed", doc); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "codeshot-arch-snapshot-t.svg")); err != nil {
		t.Errorf("want codeshot-arch-snapshot-t.svg next to the doc: %v", err)
	}
}
