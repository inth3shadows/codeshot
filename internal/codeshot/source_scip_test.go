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
		return &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
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
	wantRejected(t, `could not read SCIP index '.*index\.scip'`, "--diff", "--source", "scip", "--path", t.TempDir())
	wantRejected(t, `could not read SCIP index 'none\.scip'`, "Foo", "--source", "scip:none.scip")
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
		{Path: "b/lib.go", Defs: []string{"s f."}},
	}}
	src := ScipSource{Index: x}
	syms, err := src.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 || syms[0].FilePath != "a/main.go" || syms[0].Kind != "file" {
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

// F2: bare `--source scip` reads <--path>/index.scip, so --path does name
// the graph and keeps two subtrees' images apart; `scip:<file>` doesn't.
func TestCLIScipImageNameFollowsPathOnlyForTheDefaultIndex(t *testing.T) {
	requireBins(t, "dot", "git")
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	sub := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := writeScipFile(t, sub, "index.scip", "scip-go")
	doc := filepath.Join(root, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ source, image string }{
		{"scip", "codeshot-arch-scip-go-services_api.svg"},
		{"scip:" + idx, "codeshot-arch-scip-go.svg"},
	} {
		if code, _, stderr := runCLI("--architecture", "--source", c.source, "--path", sub, "--format", "svg", "--embed", doc); code != 0 {
			t.Fatalf("%s: exit %d; stderr: %s", c.source, code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, c.image)); err != nil {
			t.Errorf("%s: want %s next to the doc: %v", c.source, c.image, err)
		}
	}
}

// F12: a SCIP source's edges are per file, so --max-symbols counts files:
// fields, namespaces and other definitions don't use up the budget.
func TestScipSymbolsAreOneNodePerFile(t *testing.T) {
	x := &scipindex.Index{Docs: []scipindex.Doc{
		{Path: "a.go", Defs: []string{"s A#", "s A#f."}},
		{Path: "b.go", Defs: []string{"s `m`/"}},
	}}
	syms, err := ScipSource{Index: x}.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 || syms[0].FilePath != "a.go" || syms[1].FilePath != "b.go" || syms[0].Kind != "file" {
		t.Errorf("symbols %+v, want one file node per document", syms)
	}
}

// R3: a SCIP source's --max-symbols counts files, so its cut warning says so.
func TestCLIScipBudgetWarningCountsFiles(t *testing.T) {
	requireBins(t, "dot")
	idx := writeScipFile(t, t.TempDir(), "x.scip", "scip-go")
	out := filepath.Join(t.TempDir(), "arch.dot")
	code, _, stderr := runCLI("--architecture", "--source", "scip:"+idx, "--max-symbols", "1", "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `stopped after 1 file\(s\) \(--max-symbols counts files with a SCIP source\)`, stderr)
	wantNoMatch(t, `symbols \(--max-symbols\)`, stderr)
}
