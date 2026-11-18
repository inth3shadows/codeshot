package codeshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

func TestUnionFileEdgesKeepsSourceOrderTheHighestWeightAndEverySourcesOwn(t *testing.T) {
	got := UnionFileEdges([][]FileEdge{
		{{From: "a", To: "b", Weight: 3}, {From: "a", To: "c", Weight: 1}},
		{{From: "a", To: "d", Weight: 2}, {From: "a", To: "b", Weight: 5}},
	}, []string{"cg", "scip"})
	want := []FileEdge{
		{From: "a", To: "b", Weight: 5, Seen: []SourceWeight{{"cg", 3}, {"scip", 5}}, Full: []string{"cg", "scip"}},
		{From: "a", To: "c", Weight: 1, Seen: []SourceWeight{{"cg", 1}}, Full: []string{"cg"}},
		{From: "a", To: "d", Weight: 2, Seen: []SourceWeight{{"scip", 2}}, Full: []string{"scip"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A group edge's weight is the highest per-source sum, not the sum of the
// file edges' maxima (3+3 here), which no source reports.
func TestRollupOfAMultiSourceGraphWeighsAGroupEdgeByItsHighestSource(t *testing.T) {
	got := RollupFileEdges([]FileEdge{
		{From: "src/a", To: "lib/x", Weight: 3, Seen: []SourceWeight{{"cg", 3}, {"scip", 1}}},
		{From: "src/b", To: "lib/y", Weight: 3, Seen: []SourceWeight{{"scip", 3}}},
	}, 1)
	want := []FileEdge{{From: "src/", To: "lib/", Weight: 4, Seen: []SourceWeight{{"cg", 3}, {"scip", 4}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestMultiSourceArchitectureDotDashesEdgesOnlySomeSourcesSaw(t *testing.T) {
	edges := []FileEdge{
		{From: "a", To: "b", Weight: 5, Seen: []SourceWeight{{"cg", 3}, {"scip", 5}}, Full: []string{"cg", "scip"}},
		{From: "a", To: "c", Weight: 2, Seen: []SourceWeight{{"scip", 2}}, Full: []string{"scip"}},
	}
	dot := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"a" -> "b" \[label="5"\];`, dot)
	wantMatch(t, `"a" -> "c" \[label="scip 2", style=dashed\];`, dot)
	wantMatch(t, `label="Sources: cg, scip\. Dashed: not every source saw all of the edge; its label gives each source's own weight\."`, dot)
	wantNoMatch(t, `tooltip`, dot)

	svg := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}, Tooltips: true})
	wantMatch(t, `"a" -> "b" \[label="5", tooltip="cg 3, scip 5"\];`, svg)
	wantMatch(t, `"a" -> "c" \[label="scip 2", style=dashed, tooltip="scip 2"\];`, svg)

	// One source: no legend, no provenance, exactly as before lists existed.
	single := BuildArchitectureDot([]FileEdge{{From: "a", To: "b", Weight: 5}}, ArchOptions{MaxRender: Unlimited, Tooltips: true})
	wantNoMatch(t, `Sources:|tooltip|dashed`, single)
}

func TestParseSourceList(t *testing.T) {
	specs, err := parseSourceList("codegraph, scip:x.scip,snapshot:s.json", "repo")
	if err != nil {
		t.Fatal(err)
	}
	want := []sourceSpec{{kind: "codegraph"}, {kind: "scip", file: "x.scip"}, {kind: "snapshot", file: "s.json"}}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("got %+v, want %+v", specs, want)
	}
	if specs, _ := parseSourceList("codegraph,scip", "repo"); len(specs) != 2 || !specs[1].underPath || specs[1].file != filepath.Join("repo", "index.scip") {
		t.Errorf("bare scip in a list: got %+v", specs)
	}
	for v, pattern := range map[string]string{
		"codegraph,bogus":         `got 'bogus'`,
		"codegraph,":              `--source 'codegraph,' has an empty entry`,
		"codegraph,codegraph":     `lists 'codegraph' twice`,
		"scip:x.scip,scip:x.scip": `lists 'scip:x\.scip' twice`,
	} {
		_, err := parseSourceList(v, "repo")
		if err == nil {
			t.Errorf("%q: no error", v)
			continue
		}
		wantMatch(t, pattern, err.(*Exit).Stderr)
	}
}

const cliSnapshotU = `{"schema":"graph-assay/snapshot@1","subject":{"name":"u"},
 "capabilities":{"siteLines":false,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[],"unresolved":[],
 "nodes":[{"id":"1","kind":"function","name":"A","qname":"A","file":"src/a.go","start":1,"end":2},
          {"id":"2","kind":"function","name":"B","qname":"B","file":"lib/b.go","start":1,"end":2},
          {"id":"3","kind":"function","name":"C","qname":"C","file":"lib/c.go","start":1,"end":2}],
 "edges":[{"kind":"calls","src":"1","dst":"2"},{"kind":"calls","src":"1","dst":"3"}]}`

// Two snapshots need no codegraph; the union is drawn with provenance,
// --forbid checks it and names who saw each violation, and --embed gets a
// marker naming both sources.
func TestCLIArchitectureFromTwoSourcesUnionsThem(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	src := "snapshot:" + writeSnapshotFile(t, cliSnapshot) + ",snapshot:" + writeSnapshotFile(t, cliSnapshotU)
	out := filepath.Join(t.TempDir(), "arch.gv")
	code, _, stderr := runCLI("--architecture", "--source", src, "--format", "canon", "--out", out, "--forbid", "src/->lib/c.go")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (the --forbid rule is broken by the second source's edge); stderr: %s", code, stderr)
	}
	wantMatch(t, `src/a\.go -> lib/c\.go \(weight 1\) breaks --forbid 'src/->lib/c\.go' — seen by snapshot-u 1`, stderr)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wantMatch(t, `"src/a\.go" -> "lib/b\.go"\s*\[label=1\]`, string(got))
	wantMatch(t, `"src/a\.go" -> "lib/c\.go"[^;]*label="snapshot-u 1"[^;]*style=dashed`, string(got))

	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("--architecture", "--source", src, "--format", "svg", "--embed", doc); code != 0 {
		t.Fatalf("embed: exit %d; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "codeshot-arch-snapshot-t-snapshot-u.svg")); err != nil {
		t.Errorf("image: %v", err)
	}
	md, _ := os.ReadFile(doc)
	wantMatch(t, `<!-- codeshot:arch-snapshot-t-snapshot-u:start -->`, string(md))
	wantMatch(t, `Architecture \(from the t snapshot and the u snapshot\)`, string(md))
	svg, _ := os.ReadFile(filepath.Join(dir, "codeshot-arch-snapshot-t-snapshot-u.svg"))
	wantMatch(t, `xlink:title="snapshot&#45;t 1, snapshot&#45;u 1"`, string(svg))
}

func TestCLISourceListIsArchitectureOnlyAndLabelsMustDiffer(t *testing.T) {
	wantRejected(t, `a --source list \(e\.g\. 'codegraph,scip'\) only applies with --architecture`, "Foo", "--source", "codegraph,scip")
	wantRejected(t, `a --source list .* only applies with --architecture`, "--diff", "--source", "codegraph,scip")
	wantRejected(t, `got 'bogus'`, "--architecture", "--source", "codegraph,bogus")
	a, b := writeSnapshotFile(t, cliSnapshot), writeSnapshotFile(t, cliSnapshot)
	wantRejected(t, `--source entries 'snapshot:.*' and 'snapshot:.*' are both labeled 'snapshot-t'`, "--architecture", "--source", "snapshot:"+a+",snapshot:"+b)
	wantRejected(t, `could not read snapshot 'missing\.json'`, "--architecture", "--source", "snapshot:"+a+",snapshot:missing.json")
}

// One source finding nothing is worth a warning even when the diagram isn't
// blank: it usually means the wrong file or an indexer that didn't run.
func TestCLIMultiSourceWarnsAboutASourceThatFoundNothing(t *testing.T) {
	requireBins(t, "dot")
	empty := strings.Replace(strings.Replace(cliSnapshotU, `"name":"u"`, `"name":"e"`, 1),
		`[{"kind":"calls","src":"1","dst":"2"},{"kind":"calls","src":"1","dst":"3"}]`, `[]`, 1)
	src := "snapshot:" + writeSnapshotFile(t, cliSnapshot) + ",snapshot:" + writeSnapshotFile(t, empty)
	out := filepath.Join(t.TempDir(), "arch.gv")
	code, _, stderr := runCLI("--architecture", "--source", src, "--format", "canon", "--out", out, "--limit", "5")
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `codeshot: snapshot-e found no cross-file edges, so every edge drawn comes from the other sources`, stderr)
	wantMatch(t, `--limit has no effect with --architecture \(every edge is read from the listed sources\)`, stderr)
	wantNoMatch(t, `the diagram is blank`, stderr)

	both := "snapshot:" + writeSnapshotFile(t, empty) + ",snapshot:" + writeSnapshotFile(t, strings.Replace(empty, `"name":"e"`, `"name":"f"`, 1))
	_, _, stderr = runCLI("--architecture", "--source", both, "--format", "canon", "--out", out)
	wantMatch(t, `codeshot: snapshot-e: --architecture found no cross-file call edges — the diagram is blank\. The e snapshot`, stderr)
	wantMatch(t, `codeshot: snapshot-f: --architecture found no cross-file call edges — the diagram is blank\. The f snapshot`, stderr)
}

// F5: a group edge is drawn solid only when every source saw every file edge
// inside it. V2: a dashed edge's label gives each source's own weight, so a
// number is never shown next to a source that didn't count it.
func TestGroupEdgeIsDashedWhenTheSourcesAgreeOnNoneOfItsFileEdges(t *testing.T) {
	union := UnionFileEdges([][]FileEdge{
		{{From: "src/a", To: "lib/x", Weight: 1}},
		{{From: "src/b", To: "lib/y", Weight: 1}},
	}, []string{"cg", "scip"})
	dot := BuildArchitectureDot(RollupFileEdges(union, 1), ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"src/" -> "lib/" \[label="cg 1, scip 1", style=dashed\];`, dot)

	union = UnionFileEdges([][]FileEdge{
		{{From: "src/a", To: "lib/x", Weight: 1}, {From: "src/b", To: "lib/y", Weight: 1}},
		{{From: "src/b", To: "lib/y", Weight: 1}},
	}, []string{"cg", "scip"})
	dot = BuildArchitectureDot(RollupFileEdges(union, 1), ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"src/" -> "lib/" \[label="cg 2, scip 1", style=dashed\];`, dot)

	union = UnionFileEdges([][]FileEdge{
		{{From: "src/a", To: "lib/x", Weight: 1}, {From: "src/b", To: "lib/y", Weight: 2}},
		{{From: "src/a", To: "lib/x", Weight: 3}, {From: "src/b", To: "lib/y", Weight: 1}},
	}, []string{"cg", "scip"})
	dot = BuildArchitectureDot(RollupFileEdges(union, 1), ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"src/" -> "lib/" \[label="4"\];`, dot)
}

// F2/F3: a bad list entry is reported as itself, not swallowed into one file
// name; a whole value that names an existing file is that one file, even when
// its comma-separated parts happen to be valid entries.
func TestParseSourceListReportsTheBadEntryAndPrefersAnExistingCommaFile(t *testing.T) {
	_, err := parseSourceList("snapshot:a.json,scip-go:x.scip", "repo")
	if err == nil {
		t.Fatal("no error")
	}
	wantMatch(t, `got 'scip-go:x\.scip'`, err.(*Exit).Stderr)
	_, err = parseSourceList("codegraph,", "repo")
	if err == nil {
		t.Fatal("no error")
	}
	wantMatch(t, `--source 'codegraph,' has an empty entry`, err.(*Exit).Stderr)

	dir := t.TempDir()
	comma := filepath.Join(dir, "idx,scip")
	if err := os.WriteFile(comma, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	specs, err := parseSourceList("scip:"+comma, "repo")
	if err != nil || len(specs) != 1 || specs[0].file != comma {
		t.Errorf("existing comma file: got %+v, %v", specs, err)
	}
	specs, err = parseSourceList("snapshot:"+filepath.Join(dir, "a,b.json"), "repo")
	if err == nil {
		t.Errorf("missing comma file and an invalid list: got %+v, want the bad entry reported", specs)
	}
}

// F7: codegraph has no file or indexer to check; its advice is init/status.
func TestSourceFoundNoEdgesWarningGivesCodegraphItsOwnAdvice(t *testing.T) {
	wantMatch(t, `codegraph found no cross-file edges.*'codegraph init <path>'.*'codegraph status'`, SourceFoundNoEdgesWarning("codegraph"))
	wantNoMatch(t, `indexer run`, SourceFoundNoEdgesWarning("codegraph"))
	wantMatch(t, `right file, indexer run over the whole project`, SourceFoundNoEdgesWarning("scip-go"))
}

// F6: in a list, a source's fatal error names the source.
func TestMultiSourceFatalErrorNamesTheSource(t *testing.T) {
	_, _, err := RunMultiArchitectureMode([]LabeledSource{{Label: "codegraph", Src: failingSource{}}}, ArchParams{MaxSymbols: 10, MaxRender: Unlimited}, &strings.Builder{})
	if err == nil || !strings.HasPrefix(err.Error(), "codeshot: codegraph: ") {
		t.Errorf("got %v, want the error prefixed with the source label", err)
	}
}

type failingSource struct{}

func (failingSource) Symbols() ([]Node, error) {
	return nil, &codegraph.FatalError{Msg: "codeshot: codegraph has no index"}
}
func (failingSource) FileEdges([]Node) ([]SymbolEdge, error) { return nil, nil }
func (failingSource) EmptyGraphWarning() string              { return "" }

// F1: a file source's paths are relative to --path; codegraph's are relative
// to its index root, which can sit above --path. In a list, file sources are
// rebased onto the index root so the same file is one node.
func TestFileSourcePrefixRebasesOntoTheCodegraphIndexRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codegraph", "codegraph.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{root: "", sub: "pkg/sub/"} {
		if got := fileSourcePrefix(path); got != want {
			t.Errorf("fileSourcePrefix(%q) = %q, want %q", path, got, want)
		}
	}
	edges, _ := prefixedSource{Src: ScipSourceStub{}, Prefix: "pkg/sub/"}.FileEdges(nil)
	if want := []SymbolEdge{{"pkg/sub/a.go", "pkg/sub/b.go"}}; !reflect.DeepEqual(edges, want) {
		t.Errorf("got %+v, want %+v", edges, want)
	}
}

type ScipSourceStub struct{}

func (ScipSourceStub) Symbols() ([]Node, error) { return nil, nil }
func (ScipSourceStub) FileEdges([]Node) ([]SymbolEdge, error) {
	return []SymbolEdge{{"a.go", "b.go"}}, nil
}
func (ScipSourceStub) EmptyGraphWarning() string { return "" }

// V2: the group edge from the skill's repro: cg 2 over both file edges, scip
// 5 over one. The label must not read "5 cg".
func TestDashedGroupLabelNeverPairsOneSourcesWeightWithAnothersName(t *testing.T) {
	union := UnionFileEdges([][]FileEdge{
		{{From: "src/a", To: "lib/x", Weight: 1}, {From: "src/b", To: "lib/y", Weight: 1}},
		{{From: "src/a", To: "lib/x", Weight: 5}},
	}, []string{"cg", "scip"})
	dot := BuildArchitectureDot(RollupFileEdges(union, 1), ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"src/" -> "lib/" \[label="cg 2, scip 5", style=dashed\];`, dot)
}

// V4: a source listing one edge twice still counts once in Full.
func TestUnionFullCountsASourceOnceEvenIfItRepeatsAnEdge(t *testing.T) {
	got := UnionFileEdges([][]FileEdge{
		{{From: "a", To: "b", Weight: 1}, {From: "a", To: "b", Weight: 2}},
		{},
	}, []string{"cg", "scip"})
	if len(got) != 1 || !reflect.DeepEqual(got[0].Full, []string{"cg"}) {
		t.Errorf("got %+v, want one edge with Full [cg]", got)
	}
}

// V6: a comma value whose file is missing says so, besides naming the entry.
func TestBadListThatCouldBeAMissingFileSaysTheFileIsMissing(t *testing.T) {
	_, err := parseSourceList("scip:build/idx,v2.scip", "repo")
	if err == nil {
		t.Fatal("no error")
	}
	wantMatch(t, `got 'v2\.scip' \(and there is no file 'build/idx,v2\.scip'\)`, err.(*Exit).Stderr)
}

// D2: a --path directory named "..x" is inside the index root.
func TestFileSourcePrefixKeepsADirectoryNamedDotDotSomething(t *testing.T) {
	root := fakeIndexRoot(t)
	weird := filepath.Join(root, "..weird")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fileSourcePrefix(weird); got != "..weird/" {
		t.Errorf("got %q, want %q", got, "..weird/")
	}
}

// V1: a file source whose paths already start at the index root (one
// repo-wide index, --path below it) is not rebased; one relative to --path is.
func TestFileSourceIsRebasedOnlyWhenItsPathsAreRelativeToPath(t *testing.T) {
	root := fakeIndexRoot(t)
	for _, f := range []string{"pkg/sub/a.go", "pkg/sub/b.go", "other.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(root, "pkg", "sub")
	rootRelative := filesSource{"pkg/sub/a.go", "pkg/sub/b.go", "other.go"}
	pathRelative := filesSource{"a.go", "b.go"}
	if rel, _ := pathsRelativeToPath(rootRelative, sub, "pkg/sub/"); rel {
		t.Error("root-relative paths judged relative to --path (would double the prefix)")
	}
	if rel, _ := pathsRelativeToPath(pathRelative, sub, "pkg/sub/"); !rel {
		t.Error("--path-relative paths not judged relative to --path")
	}
	// Neither on disk and no path under the prefix: relative to --path.
	if rel, found := pathsRelativeToPath(filesSource{"gone.go"}, sub, "pkg/sub/"); !rel || found {
		t.Error("paths found nowhere should default to relative to --path")
	}
}

func fakeIndexRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codegraph", "codegraph.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// filesSource is a file source whose Symbols name the given files.
type filesSource []string

func (f filesSource) Symbols() ([]Node, error) {
	var out []Node
	for _, p := range f {
		out = append(out, Node{Name: "x", FilePath: p})
	}
	return out, nil
}
func (filesSource) FileEdges([]Node) ([]SymbolEdge, error) { return nil, nil }
func (filesSource) EmptyGraphWarning() string              { return "" }

// W1: absent files ahead of the real ones (generated code the checkout
// lacks) must not turn a repo-root-relative index into a tie. And with no
// file on disk at all, the paths themselves decide: one already under the
// --path prefix means index-root-relative.
func TestRootVoteIsNotSwampedByAbsentFilesAndFallsBackToThePaths(t *testing.T) {
	root := fakeIndexRoot(t)
	for _, f := range []string{"pkg/sub/a.go", "pkg/sub/b.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(root, "pkg", "sub")
	var gen filesSource
	for i := range 250 {
		gen = append(gen, fmt.Sprintf("api/gen/g%03d.go", i))
	}
	if rel, found := pathsRelativeToPath(append(gen, "pkg/sub/a.go", "pkg/sub/b.go"), sub, "pkg/sub/"); rel || !found {
		t.Error("250 absent files ahead of two root-relative ones: judged relative to --path (doubles the prefix)")
	}
	if rel, found := pathsRelativeToPath(filesSource{"pkg/sub/gone.go", "other/gone.go"}, sub, "pkg/sub/"); rel || found {
		t.Error("nothing on disk, paths under the prefix: judged relative to --path")
	}
	if rel, _ := pathsRelativeToPath(filesSource{"gone.go"}, sub, "pkg/sub/"); !rel {
		t.Error("nothing on disk, no path under the prefix: not judged relative to --path")
	}
}

// W2/W3: in a list, file-source paths are cleaned whether or not they are
// rebased, and an absolute path never gets the prefix.
func TestPrefixedSourceCleansAndLeavesAbsolutePathsAlone(t *testing.T) {
	src := edgesSource{{"./pkg/x.go", "pkg//y.go"}, {"/abs/x.go", "a.go"}, {"", "a.go"}}
	got, _ := prefixedSource{Src: src, Prefix: ""}.FileEdges(nil)
	if want := []SymbolEdge{{"pkg/x.go", "pkg/y.go"}, {"/abs/x.go", "a.go"}, {"", "a.go"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("no prefix: got %+v, want %+v", got, want)
	}
	got, _ = prefixedSource{Src: src, Prefix: "sub/"}.FileEdges(nil)
	if want := []SymbolEdge{{"sub/pkg/x.go", "sub/pkg/y.go"}, {"/abs/x.go", "sub/a.go"}, {"", "sub/a.go"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("prefix: got %+v, want %+v", got, want)
	}
}

type edgesSource []SymbolEdge

func (edgesSource) Symbols() ([]Node, error)                 { return nil, nil }
func (e edgesSource) FileEdges([]Node) ([]SymbolEdge, error) { return e, nil }
func (edgesSource) EmptyGraphWarning() string                { return "" }

// W3: two file sources spelling one path differently ("./src/a.go") draw one
// edge, solid, with no codegraph in the list.
func TestCLITwoFileSourcesSpellingAPathDifferentlyDrawOneEdge(t *testing.T) {
	requireBins(t, "dot")
	dotted := strings.Replace(strings.Replace(cliSnapshotU, `"src/a.go"`, `"./src/a.go"`, 1), `"lib/b.go"`, `"lib//b.go"`, 1)
	src := "snapshot:" + writeSnapshotFile(t, cliSnapshot) + ",snapshot:" + writeSnapshotFile(t, dotted)
	out := filepath.Join(t.TempDir(), "arch.gv")
	if code, _, stderr := runCLI("--architecture", "--source", src, "--format", "canon", "--out", out); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `"src/a\.go" -> "lib/b\.go"\s*\[label=1\]`, string(got))
	wantNoMatch(t, `"\./src/a\.go"|lib//b`, string(got))
}

func TestRootGuessWarningSaysWhichWayItGuessed(t *testing.T) {
	wantMatch(t, `given the 'pkg/sub/' prefix`, RootGuessWarning(true, "pkg/sub/"))
	wantMatch(t, `start at codegraph's index root`, RootGuessWarning(false, "pkg/sub/"))
}
