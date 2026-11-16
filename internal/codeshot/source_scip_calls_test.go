package codeshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// writeScipCalls writes a scip-go index of src/a.go (Run, lines 3-9,
// calls lib.Helper and T.M; module level calls Run) and lib/b.go (Helper,
// lines 3-5; type T, 7-12, holding M, 8-10, which calls Helper; U.M, an
// unrelated method with the same name; Bare, with no enclosing range).
func writeScipCalls(t *testing.T, dir string) string {
	t.Helper()
	const pkgA, pkgB = "scip-go gomod m v1 `m/src`/", "scip-go gomod m v1 `m/lib`/"
	run, helper, tm, um, bare := pkgA+"Run().", pkgB+"Helper().", pkgB+"T#M().", pkgB+"U#M().", pkgB+"Bare()."
	o := func(sym string, roles scip.SymbolRole, line, end int) *scip.Occurrence {
		x := &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
		x.SetSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), 5, 9}))
		if end > 0 {
			x.SetEnclosingSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), 0, int32(end - 1), 1}))
		}
		return x
	}
	b, err := proto.Marshal(&scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
		Documents: []*scip.Document{
			{RelativePath: "src/a.go", Language: "go", Occurrences: []*scip.Occurrence{
				o(run, 0, 1, 0),
				o(run, scip.SymbolRole_Definition, 3, 9),
				o(helper, 0, 5, 0),
				o(tm, 0, 6, 0),
			}},
			{RelativePath: "lib/b.go", Language: "go", Occurrences: []*scip.Occurrence{
				o(helper, scip.SymbolRole_Definition, 3, 5),
				o(pkgB+"T#", scip.SymbolRole_Definition, 7, 12),
				o(tm, scip.SymbolRole_Definition, 8, 10),
				o(helper, 0, 9, 0),
				o(pkgB+"U#", scip.SymbolRole_Definition, 14, 18),
				o(um, scip.SymbolRole_Definition, 15, 17),
				o(bare, scip.SymbolRole_Definition, 20, 0),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "index.scip")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Symbol mode reads a SCIP index with no codegraph installed: callers come
// from the definition around each reference (or the file), a qualified
// query keeps its scope, and --embed gets the indexer's marker.
func TestCLISymbolModeFromAScipIndexNeedsNoCodegraph(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	repo := t.TempDir()
	writeScipCalls(t, repo)
	out := filepath.Join(t.TempDir(), "h.dot")
	code, _, stderr := runCLI("Helper", "--source", "scip", "--path", repo, "--depth", "2", "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `\bRun -> Helper\b`, string(got))
	wantMatch(t, `\bM -> Helper\b`, string(got))
	wantMatch(t, `a\.go.* -> Run\b`, string(got)) // hop 2: module-level code calls Run
	wantMatch(t, `1 of 7 definitions in the scip-go index have no enclosing range`, stderr)
	wantNoMatch(t, `codegraph`, stderr)

	// T.M's callers, not U.M's (U.M has none, and must not borrow T.M's).
	code, _, stderr = runCLI("U.M", "--source", "scip", "--path", repo, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ = os.ReadFile(out)
	wantNoMatch(t, `Run ->`, string(got))

	doc := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr = runCLI("Helper", "--source", "scip", "--path", repo, "--format", "svg", "--embed", doc); code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	md, _ := os.ReadFile(doc)
	wantMatch(t, `<!-- codeshot:Helper-scip-go:start -->`, string(md))
	wantMatch(t, `Helper call graph \(from the scip-go index\)`, string(md))
	if _, err := os.Stat(filepath.Join(filepath.Dir(doc), "codeshot-callgraph-Helper-scip-go.svg")); err != nil {
		t.Error(err)
	}

	code, _, stderr = runCLI("Nope", "--source", "scip", "--path", repo, "--format", "dot", "--out", out)
	if code != 1 {
		t.Errorf("unknown symbol: exit %d", code)
	}
	wantMatch(t, `symbol 'Nope' not found in the scip-go index`, stderr)
}

// --diff finds changed files with git under --path and draws their symbols
// from the index; its warnings name the index, never codegraph.
func TestCLIDiffModeFromAScipIndex(t *testing.T) {
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
	writeScipCalls(t, repo)
	run("add", ".")
	run("commit", "-q", "-m", "v1")
	out := filepath.Join(t.TempDir(), "diff.dot")

	if err := os.WriteFile(filepath.Join(repo, "src/a.go"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI("--diff", "--source", "scip", "--path", repo, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `\bRun -> Helper\b`, string(got))

	run("checkout", "-q", "--", "src/a.go")
	if err := os.WriteFile(filepath.Join(repo, "docs/x.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI("--diff", "--source", "scip", "--path", repo, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `no matching symbols in the scip-go index — .*a language scip-go doesn't index, or the index may be stale \(rerun scip-go on the current code\)`, stderr)
	wantNoMatch(t, `codegraph`, stderr)
}
