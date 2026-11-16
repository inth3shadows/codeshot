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
	return writeScipCallsBy(t, dir, "scip-go")
}

// writeScipCallsBy is writeScipCalls with the indexer's name.
func writeScipCallsBy(t *testing.T, dir, tool string) string {
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
	return writeIndex(t, dir, &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: tool}},
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
}

func writeIndex(t *testing.T, dir string, x *scip.Index) string {
	t.Helper()
	b, err := proto.Marshal(x)
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

// gitRepo makes a git repo with a committed file at each path.
func gitRepo(t *testing.T, files ...string) (repo string, run func(args ...string)) {
	t.Helper()
	repo = t.TempDir()
	run = func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for _, f := range files {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo, run
}

// --diff finds changed files with git under --path and draws their symbols
// from the index; its warnings name the index, never codegraph.
func TestCLIDiffModeFromAScipIndex(t *testing.T) {
	requireBins(t, "dot", "git")
	repo, run := gitRepo(t, "src/a.go", "lib/b.go", "docs/x.md")
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

// F3 (PR #68 review): an index built in a subdirectory names files from
// there, and --diff --path <subdir> matches git's changes against it.
// F7: the advice names the indexer as its metadata does.
func TestCLIDiffModeFromAScipIndexInASubdirectory(t *testing.T) {
	requireBins(t, "dot", "git")
	repo, run := gitRepo(t, "svc/src/a.go", "svc/lib/b.go", "svc/docs/x.md", "other/c.go")
	svc := filepath.Join(repo, "svc")
	writeScipCallsBy(t, svc, "rust-analyzer")
	run("add", ".")
	run("commit", "-q", "-m", "v1")
	out := filepath.Join(t.TempDir(), "diff.dot")
	for _, f := range []string{"svc/src/a.go", "other/c.go"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, _, stderr := runCLI("--diff", "--source", "scip", "--path", svc, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `\bRun -> Helper\b`, string(got))

	run("checkout", "-q", "--", "svc/src/a.go")
	if err := os.WriteFile(filepath.Join(svc, "docs/x.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI("--diff", "--source", "scip", "--path", svc, "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	wantMatch(t, `found 1 changed file\(s\)`, stderr) // other/c.go is outside --path
	wantMatch(t, `a language rust-analyzer doesn't index, or the index may be stale \(rerun rust-analyzer on the current code\)`, stderr)
}

// F1 (PR #68 review): a symbol defined three times in one file (overloads)
// is one callee, so X's two callees fit --limit 2 with nothing cut.
func TestCLIScipSymbolDefinedRepeatedlyCountsOnce(t *testing.T) {
	requireBins(t, "dot")
	const ts = "scip-typescript npm m 1.0.0 src/`y.ts`/"
	x, f, z := ts+"X().", ts+"F().", ts+"Z()."
	o := func(sym string, roles scip.SymbolRole, line, end int) *scip.Occurrence {
		r := &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
		r.SetSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), 9, 10}))
		if end > 0 {
			r.SetEnclosingSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), 0, int32(end - 1), 1}))
		}
		return r
	}
	repo := t.TempDir()
	writeIndex(t, repo, &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-typescript"}},
		Documents: []*scip.Document{{RelativePath: "src/y.ts", Language: "TypeScript",
			Symbols: []*scip.SymbolInformation{
				{Symbol: x, Kind: scip.SymbolInformation_Function},
				{Symbol: f, Kind: scip.SymbolInformation_Function},
				{Symbol: z, Kind: scip.SymbolInformation_Function},
			},
			Occurrences: []*scip.Occurrence{
				o(f, scip.SymbolRole_Definition, 1, 1),
				o(f, scip.SymbolRole_Definition, 2, 2),
				o(f, scip.SymbolRole_Definition, 3, 5),
				o(z, scip.SymbolRole_Definition, 7, 9),
				o(x, scip.SymbolRole_Definition, 11, 15),
				o(f, 0, 12, 0),
				o(z, 0, 13, 0),
			}}},
	})
	out := filepath.Join(t.TempDir(), "x.dot")
	code, _, stderr := runCLI("X", "--source", "scip", "--path", repo, "--limit", "2", "--format", "dot", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	got, _ := os.ReadFile(out)
	wantMatch(t, `\bX -> F\b`, string(got))
	wantMatch(t, `\bX -> Z\b`, string(got))
	wantNoMatch(t, `cut off`, stderr)
}
