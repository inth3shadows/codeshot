package codeshot

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// Callers and callees follow the callee kinds in edge order, deduped by
// node, without the symbol itself; contains edges and self-calls are out.
func TestSnapshotCallsFollowCalleeKindsInEdgeOrder(t *testing.T) {
	snap := testSnapshot(t)
	snap.Edges = append(snap.Edges, snapshot.Edge{Src: "B", Dst: "B", Kind: "calls"})
	s := NewSnapshotCalls(snap)
	r, err := s.Edges("callers", "B", 20, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r.Callers); !reflect.DeepEqual(got, []string{"A", "Run"}) {
		t.Errorf("callers of B %v, want [A Run] (R reaches B twice, B calls itself)", got)
	}
	r, _ = s.Edges("callees", "A", 20, true)
	if got := names(r.Callees); !reflect.DeepEqual(got, []string{"B", "C", "b.go", "Run"}) {
		t.Errorf("callees of A %v", got)
	}
	if *r.Total != 4 || *r.Truncated {
		t.Errorf("total %v truncated %v", *r.Total, *r.Truncated)
	}
}

// Same-named definitions in two files get a definition each, so --diff
// attributes them per file, and the union is capped at --limit.
func TestSnapshotCallsSplitSameNamedDefinitionsAndCapAtLimit(t *testing.T) {
	q := func(s string) *string { return &s }
	snap := &snapshot.Snapshot{Schema: snapshot.Schema,
		Nodes: []snapshot.Node{
			{ID: "x1", Kind: "function", Name: "X", QName: q("X"), File: "a.go"},
			{ID: "x2", Kind: "function", Name: "X", QName: q("X"), File: "b.go"},
			{ID: "p", Kind: "function", Name: "P", File: "c.go"},
			{ID: "r", Kind: "function", Name: "R", File: "c.go"},
			{ID: "s", Kind: "function", Name: "S", File: "c.go"},
		},
		Edges: []snapshot.Edge{
			{Kind: "calls", Src: "p", Dst: "x1"}, {Kind: "calls", Src: "r", Dst: "x1"},
			{Kind: "calls", Src: "s", Dst: "x2"},
		},
	}
	r, err := NewSnapshotCalls(snap).Edges("callers", "X", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions) != 2 {
		t.Fatalf("definitions %+v", r.Definitions)
	}
	a, b := r.Definitions[0], r.Definitions[1]
	if *a.Definition.FilePath != "a.go" || !reflect.DeepEqual(names(a.Callers), []string{"P"}) || !*a.Truncated || *a.Total != 2 {
		t.Errorf("a.go definition %+v", a)
	}
	if *b.Definition.FilePath != "b.go" || !reflect.DeepEqual(names(b.Callers), []string{"S"}) || *b.Truncated {
		t.Errorf("b.go definition %+v", b)
	}
	if !reflect.DeepEqual(names(r.Callers), []string{"P"}) || *r.Total != 3 || !*r.Truncated {
		t.Errorf("union %v total %v", names(r.Callers), *r.Total)
	}
	if picked := PickDefinitionResult(r, "callers", "b.go"); picked == nil || !reflect.DeepEqual(names(picked.Callers), []string{"S"}) {
		t.Errorf("PickDefinitionResult(b.go) = %+v", picked)
	}
}

// Lookup is exact or qualified, never fuzzy.
func TestSnapshotCallsResolveExactOrQualifiedNames(t *testing.T) {
	s := NewSnapshotCalls(testSnapshot(t))
	for query, want := range map[string]string{"Run": "Run", "T.Run": "Run", "T::Run": "Run", "B": "B"} {
		if got, err := s.Resolve(query); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", query, got, err, want)
		}
	}
	for _, query := range []string{"Ru", "run", "U.Run", "X.T.Run"} {
		_, err := s.Resolve(query)
		if err == nil {
			t.Errorf("Resolve(%q) found something", query)
			continue
		}
		wantMatch(t, `not found in the snapshot — .*it never guesses`, err.Error())
	}
	if r, err := s.Edges("callers", "Nope", 5, false); r != nil || err != nil {
		t.Errorf("non-fatal miss = %v, %v", r, err)
	}
}

// Symbol mode reads a snapshot with no codegraph installed, follows --depth,
// and embeds under its own marker and image name.
func TestCLISymbolModeFromASnapshotNeedsNoCodegraph(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	snapBody := `{"schema":"graph-assay/snapshot@1","subject":{"name":"t"},
 "capabilities":{"siteLines":false,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[],"unresolved":[],
 "nodes":[{"id":"1","kind":"function","name":"A","qname":"A","file":"src/a.go","start":1,"end":2},
          {"id":"2","kind":"function","name":"B","qname":"B","file":"lib/b.go","start":1,"end":2},
          {"id":"3","kind":"function","name":"Main","qname":"Main","file":"main.go","start":1,"end":2}],
 "edges":[{"kind":"calls","src":"1","dst":"2"},{"kind":"calls","src":"3","dst":"1"}]}`
	snap := writeSnapshotFile(t, snapBody)
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI("B", "--source", "snapshot:"+snap, "--depth", "2", "--format", "svg", "--embed", doc)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantNoMatch(t, `codegraph`, stderr)
	md, _ := os.ReadFile(doc)
	wantMatch(t, `<!-- codeshot:B-snapshot-t:start -->`, string(md))
	wantMatch(t, `B call graph \(from the t snapshot\)`, string(md))
	svg, err := os.ReadFile(filepath.Join(dir, "codeshot-callgraph-B-snapshot-t.svg"))
	if err != nil {
		t.Fatal(err)
	}
	wantMatch(t, `>A<`, string(svg))
	wantMatch(t, `>Main<`, string(svg)) // hop 2
	code, _, stderr = runCLI("Bee", "--source", "snapshot:"+snap, "--format", "dot", "--out", filepath.Join(dir, "x.dot"))
	if code != 1 {
		t.Errorf("unknown symbol: exit %d", code)
	}
	wantMatch(t, `symbol 'Bee' not found in the t snapshot`, stderr)
}

// --diff finds changed files with git under --path, then reads the
// snapshot; its warnings name the snapshot, never codegraph.
func TestCLIDiffModeFromASnapshot(t *testing.T) {
	requireBins(t, "dot", "git")
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for _, f := range []string{"src/a.go", "lib/b.go", "docs/x.md"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "v1")
	snap := writeSnapshotFile(t, cliSnapshot)
	out := filepath.Join(t.TempDir(), "diff.dot")

	if err := os.WriteFile(filepath.Join(repo, "src/a.go"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI("--diff", "--source", "snapshot:"+snap, "--path", repo, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `\bA -> B\b`, string(got))

	run("checkout", "-q", "--", "src/a.go")
	if err := os.WriteFile(filepath.Join(repo, "docs/x.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI("--diff", "--source", "snapshot:"+snap, "--path", repo, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `no matching symbols in the t snapshot — .*a language t doesn't index, or the snapshot may be stale \(export a new snapshot from the current code\)`, stderr)
	wantNoMatch(t, `codegraph`, stderr)
}

// The order a source lists neighbours in never reaches the DOT: codegraph's
// SQL has no ORDER BY, and a snapshot keeps its file's order.
func TestSymbolDotDoesNotDependOnNeighbourOrder(t *testing.T) {
	fwd := testSnapshot(t)
	rev := testSnapshot(t)
	for i, j := 0, len(rev.Edges)-1; i < j; i, j = i+1, j-1 {
		rev.Edges[i], rev.Edges[j] = rev.Edges[j], rev.Edges[i]
	}
	p := SymbolParams{Limit: 50, MaxRender: Unlimited, Depth: 2, MaxDepthNodes: 200}
	for _, sym := range []string{"A", "B", "Run"} {
		a, _, err := RunSymbolMode(NewSnapshotCalls(fwd), sym, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := RunSymbolMode(NewSnapshotCalls(rev), sym, p, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Errorf("%s: DOT depends on edge order:\n%s\n---\n%s", sym, a, b)
		}
	}
}
