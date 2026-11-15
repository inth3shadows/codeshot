package codeshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/codeshot/internal/scipindex"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// writeScipFile writes a SCIP index where src/a.go calls lib/b.go's Helper,
// to dir/name, and returns its path.
func writeScipFile(t *testing.T, dir, name, tool string) string {
	t.Helper()
	helper := "scip-go gomod m v1 `m/lib`/Helper()."
	o := func(sym string, roles scip.SymbolRole) *scip.Occurrence {
		return &scip.Occurrence{Range: []int32{0, 0, 1}, Symbol: sym, SymbolRoles: int32(roles)}
	}
	b, err := proto.Marshal(&scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: tool}},
		Documents: []*scip.Document{
			{RelativePath: "src/a.go", Occurrences: []*scip.Occurrence{o(helper, 0)}},
			{RelativePath: "lib/b.go", Occurrences: []*scip.Occurrence{o(helper, scip.SymbolRole_Definition)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCLIArchitectureFromAScipIndexNeedsNoCodegraph(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	repo := t.TempDir()
	writeScipFile(t, repo, "index.scip", "scip-go")
	out := filepath.Join(t.TempDir(), "arch.dot")
	// Bare `scip` reads <--path>/index.scip.
	code, _, stderr := runCLI("--architecture", "--source", "scip", "--path", repo, "--format", "dot", "--out", out, "--forbid", "src/->lib/")
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

func TestCLIScipSourceIsValidated(t *testing.T) {
	wantRejected(t, `--source must be 'codegraph', 'snapshot:<file>' or 'scip\[:<file>\]', got 'scip:'`, "--architecture", "--source", "scip:")
	wantRejected(t, `--source must be .*, got 'lsif'`, "--architecture", "--source", "lsif")
	wantRejected(t, `works with --architecture only so far`, "--diff", "--source", "scip")
	wantRejected(t, `could not read SCIP index '.*index\.scip'`, "--architecture", "--source", "scip", "--path", t.TempDir())
	bad := filepath.Join(t.TempDir(), "bad.scip")
	if err := os.WriteFile(bad, []byte{0xff, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	wantRejected(t, `could not read SCIP index '.*bad\.scip': not a SCIP index`, "--architecture", "--source", "scip:"+bad)
}

func TestCLIScipEmbedGetsItsOwnMarkerAndImageName(t *testing.T) {
	requireBins(t, "dot")
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := writeScipFile(t, t.TempDir(), "x.scip", "scip-go")
	code, _, stderr := runCLI("--architecture", "--source", "scip:"+idx, "--path", dir, "--format", "svg", "--embed", doc, "--limit", "3")
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `every edge is read from the SCIP index`, stderr)
	if _, err := os.Stat(filepath.Join(dir, "codeshot-arch-scip-go.svg")); err != nil {
		t.Errorf("image: %v", err)
	}
	md, _ := os.ReadFile(doc)
	wantMatch(t, `<!-- codeshot:arch-scip-go:start -->`, string(md))
	wantMatch(t, `Architecture \(from the scip-go index\)`, string(md))
}

func TestScipLabelNamesTheIndexerOnce(t *testing.T) {
	for tool, want := range map[string]string{
		"scip-go": "scip-go", "scip-typescript": "scip-typescript", "rust-analyzer": "scip-rust-analyzer",
		"": "scip", "scip": "scip", "a b/../c": "scip-a-b-..-c",
	} {
		if got := scipLabel(&scipindex.Index{Tool: tool}); got != want {
			t.Errorf("scipLabel(%q) = %q, want %q", tool, got, want)
		}
	}
}

// A file that defines nothing still enumerates (as its file node) and so
// still draws its edges; a cut at --max-symbols drops whole files' edges.
func TestScipSourceCountsFileNodesAndCutsByFile(t *testing.T) {
	x := &scipindex.Index{Docs: []scipindex.Doc{
		{Path: "a/main.go", Refs: []string{"s f."}},
		{Path: "b/lib.go", Defs: []scipindex.Def{{Symbol: "s f.", Name: "f", Kind: "method"}}},
	}}
	src := ScipSource{Index: x}
	syms, err := src.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 3 || syms[0].FilePath != "a/main.go" || syms[0].Kind != "file" {
		t.Fatalf("symbols %+v", syms)
	}
	all, _ := src.FileEdges(syms)
	if len(all) != 1 || all[0] != (SymbolEdge{FromFile: "a/main.go", ToFile: "b/lib.go"}) {
		t.Errorf("edges %+v", all)
	}
	if cut, _ := src.FileEdges(syms[1:]); len(cut) != 0 {
		t.Errorf("edges without a/main.go chosen: %+v", cut)
	}
}

func TestScipEmptyWarningNamesTheIndexer(t *testing.T) {
	got := ScipSource{Index: &scipindex.Index{Tool: "scip-python"}}.EmptyGraphWarning()
	wantMatch(t, `the diagram is blank\. The scip-python index has no references`, got)
}
