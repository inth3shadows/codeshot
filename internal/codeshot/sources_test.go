package codeshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnionFileEdgesKeepsSourceOrderTheHighestWeightAndEverySourcesOwn(t *testing.T) {
	got := UnionFileEdges([][]FileEdge{
		{{From: "a", To: "b", Weight: 3}, {From: "a", To: "c", Weight: 1}},
		{{From: "a", To: "d", Weight: 2}, {From: "a", To: "b", Weight: 5}},
	}, []string{"cg", "scip"})
	want := []FileEdge{
		{From: "a", To: "b", Weight: 5, Seen: []SourceWeight{{"cg", 3}, {"scip", 5}}},
		{From: "a", To: "c", Weight: 1, Seen: []SourceWeight{{"cg", 1}}},
		{From: "a", To: "d", Weight: 2, Seen: []SourceWeight{{"scip", 2}}},
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
		{From: "a", To: "b", Weight: 5, Seen: []SourceWeight{{"cg", 3}, {"scip", 5}}},
		{From: "a", To: "c", Weight: 2, Seen: []SourceWeight{{"scip", 2}}},
	}
	dot := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}})
	wantMatch(t, `"a" -> "b" \[label="5"\];`, dot)
	wantMatch(t, `"a" -> "c" \[label="2 scip", style=dashed\];`, dot)
	wantMatch(t, `label="Sources: cg, scip\. Dashed edges: seen by only the sources named on them\."`, dot)
	wantNoMatch(t, `tooltip`, dot)

	svg := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Sources: []string{"cg", "scip"}, Tooltips: true})
	wantMatch(t, `"a" -> "b" \[label="5", tooltip="cg 3, scip 5"\];`, svg)
	wantMatch(t, `"a" -> "c" \[label="2 scip", style=dashed, tooltip="scip 2"\];`, svg)

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
	// Not a valid list, but a valid single source: a file name with a comma.
	if specs, err := parseSourceList("snapshot:a,b.json", "repo"); err != nil || len(specs) != 1 || specs[0].file != "a,b.json" {
		t.Errorf("comma in a file name: got %+v, %v", specs, err)
	}
	for v, pattern := range map[string]string{
		"codegraph,bogus":         `got 'bogus'`,
		"codegraph,":              `got ''`,
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
	wantMatch(t, `"src/a\.go" -> "lib/c\.go"[^;]*label="1 snapshot-u"[^;]*style=dashed`, string(got))

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
