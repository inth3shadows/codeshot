package codeshot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- argument validation: rejected before any codegraph work ----------------

func TestCLIRejectsNonPositiveIntegerValuesBeforeReachingCodegraph(t *testing.T) {
	for _, flag := range []string{"limit", "max-render", "depth", "max-depth-nodes"} {
		for _, bad := range []string{"abc", "0", "-5", "3.5", "NaN"} {
			t.Run(flag+"="+bad, func(t *testing.T) {
				wantRejected(t, `--`+flag+` must be a positive integer`, "Foo", "--"+flag, bad)
			})
		}
	}
}

func TestCLIArchitectureRejectsAnExplicitMaxDepthNodes(t *testing.T) {
	// no multi-hop file traversal to bound
	wantRejected(t, `--max-depth-nodes has no effect with --architecture`, "--architecture", "--max-depth-nodes", "500")
}

// --depth 1 passed alongside --architecture is a silent no-op because it matches
// --depth's own default; --max-depth-nodes must follow the same rule for its own
// default (200): "rejected only if it would actually change anything".
func TestCLIArchitectureAcceptsAnExplicitMaxDepthNodesMatchingItsOwnDefault(t *testing.T) {
	root := requireSelfIndex(t)
	code, stdout, stderr := runCLI("--architecture", "--max-depth-nodes", "200", "--path", root, "--format", "dot", "--max-symbols", "100000")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("expected --architecture --max-depth-nodes 200 to succeed and print an output path, not be rejected")
	}
	defer func() { _ = os.Remove(strings.TrimSpace(stdout)) }()
}

func TestCLIMissingSymbolArgumentIsRejectedWithACodeshotPrefixedMessage(t *testing.T) {
	stderr := wantRejected(t, `^codeshot: missing required <symbol> argument`)
	// the usage text names the binary now, not render/callgraph.js
	wantMatch(t, `Usage: codeshot <symbol>`, stderr)
	wantNoMatch(t, `callgraph\.js`, stderr)
}

func TestCLIExplicitEmptyOutIsRejectedInsteadOfFallingBackToATempPath(t *testing.T) {
	wantRejected(t, `--out must not be empty`, "Foo", "--out=")
}

func TestCLIExplicitEmptyPathIsRejectedInsteadOfFallingThroughToCodegraph(t *testing.T) {
	wantRejected(t, `--path must not be empty`, "Foo", "--path=")
}

func TestCLIExplicitEmptyFormatIsRejectedInsteadOfDefaultingToPng(t *testing.T) {
	wantRejected(t, `--format must not be empty`, "Foo", "--format=")
}

func TestCLIForbidIsRejectedWhenMalformedOrOutsideArchitectureBeforeAnyCodegraphWork(t *testing.T) {
	wantRejected(t, `--forbid expects FROM->TO .* got 'render/'`, "--architecture", "--forbid", "render/")
	wantRejected(t, `--forbid only applies with --architecture`, "buildDot", "--forbid", "a/->b/")
}

func TestCLIGroupDepthIsRejectedOutsideArchitecture(t *testing.T) {
	wantRejected(t, `--group-depth only applies with --architecture`, "Foo", "--group-depth", "1")
}

func TestCLIGroupDepthUnderDiffGivesADiffSpecificReasonNotSymbolModesWording(t *testing.T) {
	stderr := wantRejected(t, `--group-depth only applies with --architecture`, "--diff", "--group-depth", "1")
	wantMatch(t, `--diff diagrams individual changed symbols`, stderr)
	wantNoMatch(t, `a symbol trail has no file-level graph`, stderr)
}

func TestCLIGroupDepthRejectsANonPositiveIntegerValue(t *testing.T) {
	wantRejected(t, `--group-depth must be a positive integer, got '0'`, "--architecture", "--group-depth", "0")
}

func TestCLIArchitectureRejectsASymbolArgument(t *testing.T) {
	wantRejected(t, `--architecture cannot be combined with a <symbol> argument`, "Foo", "--architecture")
}

func TestCLIArchitectureRejectsDepthGreaterThanOne(t *testing.T) {
	wantRejected(t, `--depth has no effect with --architecture`, "--architecture", "--depth", "2")
}

func TestCLIDiffRejectsASymbolArgument(t *testing.T) {
	wantRejected(t, `--diff cannot be combined with a <symbol> argument`, "Foo", "--diff")
}

func TestCLIDiffRejectsArchitecture(t *testing.T) {
	wantRejected(t, `--architecture cannot be combined with --diff`, "--diff", "--architecture")
}

func TestCLICheckWithoutEmbedIsRejected(t *testing.T) {
	wantRejected(t, `--check only applies with --embed`, "Foo", "--check")
}

func TestCLIEmbedIntoANonexistentDocIsRejectedRefreshNotCreate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "codeshot-nope-does-not-exist.md")
	wantRejected(t, `does not exist`, "--architecture", "--embed", missing)
}

// --- fixtures ---------------------------------------------------------------

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// initFixtureIndex runs `codegraph init` on a fixture repo, skipping the test
// when codegraph is missing or cannot index it.
func initFixtureIndex(t *testing.T, dir string) {
	t.Helper()
	requireBins(t, "codegraph", "dot")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "codegraph", "init", dir).CombinedOutput(); err != nil {
		t.Skipf("`codegraph` could not index the fixture repo: %v\n%s", err, out)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCLIArchitectureForbidFailsTheRunOnAForbiddenEdgeStillWritesTheDiagramAndIsNotHiddenByGroupDepth(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"db/query.js": "function query() { return 1; }\nmodule.exports = { query };\n",
		"ui/page.js":  "const { query } = require(\"../db/query\");\nfunction render() { return query(); }\nmodule.exports = { render };\n",
	})
	initFixtureIndex(t, dir)
	out := filepath.Join(dir, "arch.dot")

	code, _, stderr := runCLI("--architecture", "--path", dir, "--out", out, "--format", "dot", "--forbid", "ui/->db/")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, stderr)
	}
	wantMatch(t, `ui/page\.js -> db/query\.js \(weight 1\) breaks --forbid 'ui/->db/'`, stderr)
	// the diagram is still written, with the violation drawn red
	wantMatch(t, `"ui/page\.js" -> "db/query\.js"\s*\[[^\]]*color="#dc2626"`, readFile(t, out))

	// --group-depth 1 draws only ui/ -> db/, with no file names left in the
	// graph; an exact-file rule still fires because rules read per-file edges.
	code, _, stderr = runCLI("--architecture", "--path", dir, "--out", out, "--format", "dot", "--group-depth", "1", "--forbid", "ui/page.js->db/query.js")
	if code != 1 {
		t.Errorf("expected an exact-file rule to fire under --group-depth: exit %d: %s", code, stderr)
	}

	code, _, stderr = runCLI("--architecture", "--path", dir, "--out", out, "--format", "dot", "--forbid", "db/->ui/")
	if code != 0 {
		t.Errorf("expected exit 0 with no violations: exit %d: %s", code, stderr)
	}
	wantNoMatch(t, `#dc2626`, readFile(t, out)) // nothing is red when no rule is broken
	wantNoMatch(t, `--forbid rule`, stderr)
}

// The regression test for the duplicate-name fix. This repo's own index has no
// duplicate names, so the self-test can never exercise the file-qualified path —
// it needs a purpose-built repo where a bare-name probe is provably wrong. The
// Go port reads codegraph's index directly, attributing each edge to its own
// definition.
func TestCLIArchitectureAttributesADuplicateNamedSymbolsEdgesToItsOwnFileNotTheUnion(t *testing.T) {
	dir := t.TempDir()
	// Two files defining `handle`, each calling a DIFFERENT function. The bare
	// name alone cannot tell them apart; the containing file can.
	writeFiles(t, dir, map[string]string{
		"a/svc.js":   "const { alpha } = require(\"./alpha\");\nfunction handle() { return alpha(); }\nmodule.exports = { handle };\n",
		"a/alpha.js": "function alpha() { return 1; }\nmodule.exports = { alpha };\n",
		"b/svc.js":   "const { beta } = require(\"../b/beta\");\nfunction handle() { return beta(); }\nmodule.exports = { handle };\n",
		"b/beta.js":  "function beta() { return 2; }\nmodule.exports = { beta };\n",
		// Two `run` methods in ONE file, calling different things. Same name, but
		// no file ambiguity — so this must keep BOTH edges.
		"c/dual.js": "const { alpha } = require(\"../a/alpha\");\nconst { beta } = require(\"../b/beta\");\nclass A { run() { return alpha(); } }\nclass B { run() { return beta(); } }\nmodule.exports = { A, B };\n",
		// Two FILES named index.js, each making a top-level call to a different function.
		"d/index.js": "const { alpha } = require(\"../a/alpha\");\nalpha();\n",
		"e/index.js": "const { beta } = require(\"../b/beta\");\nbeta();\n",
	})
	initFixtureIndex(t, dir)

	out := filepath.Join(dir, "arch.dot")
	code, _, stderr := runCLI("--architecture", "--path", dir, "--out", out, "--format", "dot")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	dot := readFile(t, out)

	wantMatch(t, `"a/svc\.js" -> "a/alpha\.js"`, dot)   // the real edge from a/svc.js
	wantMatch(t, `"b/svc\.js" -> "b/beta\.js"`, dot)    // the real edge from b/svc.js
	wantNoMatch(t, `"a/svc\.js" -> "b/beta\.js"`, dot)  // a/svc.js does not call beta: the misattributed edge the fix removes
	wantNoMatch(t, `"b/svc\.js" -> "a/alpha\.js"`, dot) // b/svc.js does not call alpha
	wantMatch(t, `"c/dual\.js" -> "a/alpha\.js"`, dot)  // same-file duplicates must keep both edges
	wantMatch(t, `"c/dual\.js" -> "b/beta\.js"`, dot)   // ...not just the last one
	// Each `run` calls its callee once, so each edge weighs 1. Handing both
	// methods the shared per-file answer used to count every edge twice.
	wantMatch(t, `"c/dual\.js" -> "a/alpha\.js"\s*\[label="?1"?[,\]]`, dot)
	wantMatch(t, `"c/dual\.js" -> "b/beta\.js"\s*\[label="?1"?[,\]]`, dot)

	probeOut, err := exec.Command("codegraph", "callees", "--path", dir, "--json", "--", "index.js").Output()
	if err != nil {
		t.Fatalf("codegraph callees: %v", err)
	}
	var probe struct {
		Definitions []json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(probeOut, &probe); err != nil {
		t.Fatalf("codegraph callees did not return JSON: %v", err)
	}
	if probe.Definitions != nil {
		wantMatch(t, `"d/index\.js" -> "a/alpha\.js"`, dot)
		wantMatch(t, `"e/index\.js" -> "b/beta\.js"`, dot)
		wantNoMatch(t, `"d/index\.js" -> "b/beta\.js"`, dot) // same-named files must not share the union of their callees
		wantNoMatch(t, `"e/index\.js" -> "a/alpha\.js"`, dot)
	} else {
		t.Log("partially skipped: this codegraph has no per-definition JSON (upstream #1801), so same-named files are expected to fall back to the union")
	}
}

// --- end-to-end against this repo's own real codegraph index ---------------

func TestCLIRunsEndToEndAgainstThisReposOwnRealCodegraphIndex(t *testing.T) {
	root := requireSelfIndex(t)
	dir := t.TempDir()
	pngOut, svgOut := filepath.Join(dir, "selftest.png"), filepath.Join(dir, "selftest.svg")

	if code, _, stderr := runCLI("BuildDot", "--path", root, "--out", pngOut); code != 0 {
		t.Fatalf("png run: exit %d: %s", code, stderr)
	}
	png := readFile(t, pngOut)
	if len(png) == 0 || png[0] != 0x89 || png[1:4] != "PNG" {
		t.Error("expected a real PNG file from the default format")
	}

	if code, _, stderr := runCLI("BuildDot", "--path", root, "--out", svgOut, "--format", "svg"); code != 0 {
		t.Fatalf("svg run: exit %d: %s", code, stderr)
	}
	wantMatch(t, `<svg`, readFile(t, svgOut)) // --format svg goes through the same pipeline
}

func TestCLIDepthTraversalRunsEndToEndAgainstThisReposOwnRealCodegraphIndex(t *testing.T) {
	root := requireSelfIndex(t)
	dir := t.TempDir()
	depth1Out, depth2Out := filepath.Join(dir, "depth1.dot"), filepath.Join(dir, "depth2.dot")
	// --format dot renders the raw digraph text, so depth 2 can be compared
	// without depending on how graphviz lays out a PNG/SVG.
	if code, _, stderr := runCLI("BuildDot", "--path", root, "--out", depth1Out, "--format", "dot"); code != 0 {
		t.Fatalf("depth 1: exit %d: %s", code, stderr)
	}
	if code, _, stderr := runCLI("BuildDot", "--path", root, "--out", depth2Out, "--format", "dot", "--depth", "2"); code != 0 {
		t.Fatalf("depth 2: exit %d: %s", code, stderr)
	}
	e1, e2 := strings.Count(readFile(t, depth1Out), "->"), strings.Count(readFile(t, depth2Out), "->")
	if e2 < e1 {
		t.Errorf("--depth 2 drew %d edges, fewer than depth 1's %d", e2, e1)
	}
}

func TestCLIMaxDepthNodesLowersTheDepthTraversalBudgetEndToEnd(t *testing.T) {
	root := requireSelfIndex(t)
	out := filepath.Join(t.TempDir(), "maxdepthnodes.dot")
	// BuildDot has at least one real caller/callee in this repo, so seeding the
	// depth-2 traversal's discovered set already meets a budget of 1 — the
	// traversal must report it hit the (lowered, not default 200) cap.
	code, _, stderr := runCLI("BuildDot", "--path", root, "--out", out, "--format", "dot", "--depth", "2", "--max-depth-nodes", "1")
	if code != 0 {
		t.Fatalf("expected a successful render even when the depth budget is hit, exit %d: %s", code, stderr)
	}
	wantMatch(t, `safety cap of 1 discovered nodes`, stderr) // the lowered value appears in the warning
}

// 'RunSymbolM' is a deliberate partial query — codegraph fuzzy-resolves it to the
// real symbol 'RunSymbolMode'. The rendered root label must show the resolved
// canonical name, not the literal query string. (The JS test used 'buildD' →
// 'buildDot'; here 'BuildD' would resolve to the still-indexed JS buildDot, so a
// Go-only symbol is used.)
func TestCLIResolvesAFuzzyPartialQueryToItsCanonicalNameForTheRenderedRootLabel(t *testing.T) {
	root := requireSelfIndex(t)
	out := filepath.Join(t.TempDir(), "resolve.dot")
	if code, _, stderr := runCLI("RunSymbolM", "--path", root, "--out", out, "--format", "dot"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// `dot -Tdot` reserializes and sorts node attributes alphabetically, so
	// fillcolor is not guaranteed to be first — match it anywhere in the list.
	wantMatch(t, `\bRunSymbolMode\b\s*\[[^\]]*fillcolor="#e2e8f0"`, readFile(t, out))
}

// --max-symbols 100000: this repo now has more than the default 500 symbols, and
// a truncated path-sorted prefix would make the groups under test arbitrary.
func TestCLIArchitectureGroupDepth1DrawsDirectoryGroupsNotFiles(t *testing.T) {
	root := requireSelfIndex(t)
	code, stdout, stderr := runCLI("--architecture", "--path", root, "--group-depth", "1", "--format", "dot", "--max-symbols", "100000")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	out := strings.TrimSpace(stdout)
	defer func() { _ = os.Remove(out) }()
	dot := readFile(t, out)
	// --format dot prints the DOT source itself, so the node ids are directly assertable.
	wantMatch(t, `"cmd/"`, dot)          // a cmd/ group node
	wantMatch(t, `"internal/"`, dot)     // an internal/ group node
	wantNoMatch(t, `\.go"?[\s;\[]`, dot) // files must be rolled up, not drawn alongside their groups
	wantNoMatch(t, `callgraph\.js`, dot)
}

func TestCLIEmbedArchitectureGroupDepthWritesItsOwnMarkerBlockLeavingAnUngroupedOneIntact(t *testing.T) {
	root := requireSelfIndex(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "DOC.md")
	if err := os.WriteFile(doc, []byte("# Doc\n\n<!-- codeshot:arch:start -->\n![existing](existing.svg)\n<!-- codeshot:arch:end -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("--architecture", "--path", root, "--group-depth", "1", "--embed", doc, "--format", "svg", "--max-symbols", "100000"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	md := readFile(t, doc)
	wantMatch(t, `<!-- codeshot:arch-d1:start -->`, md) // grouped view must use its own marker id
	wantMatch(t, `!\[existing\]\(existing\.svg\)`, md)  // the pre-existing ungrouped block must be left alone
	// #51: the default image name carries no checkout-directory name.
	wantMatch(t, `!\[[^\]]*\]\(codeshot-arch-d1\.svg\)`, md)
	if _, err := os.Stat(filepath.Join(dir, "codeshot-arch-d1.svg")); err != nil {
		t.Errorf("expected codeshot-arch-d1.svg written next to the doc: %v", err)
	}
}

func TestCLIArchitectureRunsEndToEndAgainstThisReposOwnRealCodegraphIndex(t *testing.T) {
	root := requireSelfIndex(t)
	out := filepath.Join(t.TempDir(), "arch-selftest.svg")
	if code, _, stderr := runCLI("--architecture", "--path", root, "--out", out, "--format", "svg", "--max-symbols", "100000"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	svg := readFile(t, out)
	wantMatch(t, `<svg`, svg) // real SVG output
	// cmd/codeshot/main.go calls into internal/codeshot — the rendered graph
	// should show at least one real cross-file edge, not an empty graph.
	wantMatch(t, `main\.go`, svg)
}

// Issue #51: without --embed the temp-dir name still includes the checkout
// directory; with --embed it never does (see the --embed tests).
func TestCLIArchitectureWithoutEmbedKeepsTheCheckoutDirectoryInItsTempFileName(t *testing.T) {
	root := requireSelfIndex(t)
	code, stdout, stderr := runCLI("--architecture", "--path", root, "--format", "dot", "--max-symbols", "100000")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	out := strings.TrimSpace(stdout)
	defer func() { _ = os.Remove(out) }()
	if dir := filepath.Dir(out); dir != filepath.Clean(os.TempDir()) {
		t.Errorf("output dir = %q, want the temp dir %q", dir, os.TempDir())
	}
	wantMatch(t, `^arch-`+regexp.QuoteMeta(ArchOutputBaseName(root))+`-\d+\.dot$`, filepath.Base(out))
}

// parentOfLastSourceChange finds a ref whose diff against the working tree
// always changes real symbols. HEAD~1 did not guarantee that (a docs- or
// CI-only commit leaves zero roots and a blank graph). Needs that parent in
// history; a shallow clone lacks it, so skip rather than fail.
func parentOfLastSourceChange(t *testing.T, root string) string {
	t.Helper()
	requireBins(t, "git")
	shaOut, err := exec.Command("git", "-C", root, "log", "-1", "--format=%H", "--", "internal/", "cmd/", "render/callgraph.js").Output()
	sha := strings.TrimSpace(string(shaOut))
	if err != nil || sha == "" {
		t.Skip("git history too shallow to find the parent of the last source change")
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", sha+"~1^{commit}").Run(); err != nil {
		t.Skip("git history too shallow to find the parent of the last source change")
	}
	return sha + "~1"
}

func TestCLIDiffRunsEndToEndAgainstThisReposOwnRealCodegraphIndexAndGitHistory(t *testing.T) {
	root := requireSelfIndex(t)
	ref := parentOfLastSourceChange(t, root)
	out := filepath.Join(t.TempDir(), "diff-selftest.svg")
	// `git diff <ref>` is working tree vs that commit, so uncommitted changes are
	// included on top — never LESS than the committed floor the assertions rely
	// on. Also exercises the real `git diff --name-only -z --end-of-options <ref>`.
	if code, _, stderr := runCLI("--diff", "--diff-ref", ref, "--max-symbols", "20", "--path", root, "--out", out, "--format", "svg"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	svg := readFile(t, out)
	wantMatch(t, `<svg`, svg)
	// Graphviz renders a bolded root as font-weight="bold" in SVG — confirms at
	// least one changed symbol was drawn as a root, not a blank graph.
	wantMatch(t, `font-weight="bold"`, svg)
}

// Issue #51: an embedded --diff image is named codeshot-diff[-<ref>].<fmt> next
// to the doc, with no checkout-directory name.
func TestCLIDiffEmbedDefaultImageNameCarriesTheRefAndNoCheckoutDirectory(t *testing.T) {
	root := requireSelfIndex(t)
	ref := parentOfLastSourceChange(t, root)
	dir := t.TempDir()
	doc := filepath.Join(dir, "DOC.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("--diff", "--diff-ref", ref, "--max-symbols", "20", "--path", root, "--embed", doc, "--format", "svg"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	img := "codeshot-diff-" + SanitizeForFilename(ref) + ".svg"
	if _, err := os.Stat(filepath.Join(dir, img)); err != nil {
		t.Errorf("expected %s written next to the doc: %v", img, err)
	}
	md := readFile(t, doc)
	wantMatch(t, `<!-- codeshot:diff-`+regexp.QuoteMeta(SanitizeForFilename(ref))+`:start -->`, md)
	wantMatch(t, `\(`+regexp.QuoteMeta(img)+`\)`, md)
	wantNoMatch(t, regexp.QuoteMeta(filepath.Base(root)), md)
}

func TestCLIDiffEmbedOnAnEmptyDiffRefusesToOverwriteAnExistingEmbeddedDiagram(t *testing.T) {
	root := requireSelfIndex(t)
	doc := filepath.Join(t.TempDir(), "DOC.md")
	original := "# Doc\n\n<!-- codeshot:diff:start -->\n![pre-existing](pre-existing.svg)\n<!-- codeshot:diff:end -->\n"
	if err := os.WriteFile(doc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// HEAD..HEAD is guaranteed empty regardless of repo/working-tree state.
	code, _, stderr := runCLI("--diff", "--diff-ref", "HEAD..HEAD", "--path", root, "--embed", doc, "--format", "svg")
	if code != 1 {
		t.Errorf("expected an empty --diff --embed to be refused, got exit %d", code)
	}
	wantMatch(t, `refusing to overwrite the existing diagram embedded in`, stderr)
	// The doc itself must be untouched — the actual data-loss guard, not just a non-zero exit.
	if got := readFile(t, doc); got != original {
		t.Errorf("doc was modified:\n%s", got)
	}
}

// Regression: --check had no reachable passing state on a zero-root diff.
func TestCLIDiffEmbedCheckOnAnEmptyDiffReportsNothingToCheckAndExitsZero(t *testing.T) {
	root := requireSelfIndex(t)
	doc := filepath.Join(t.TempDir(), "DOC.md")
	// Deliberately stale: proves --check isn't comparing at all — a zero-root
	// diff has nothing this invocation would diagram, so the committed block
	// (from some other invocation/range) isn't "drift" to report either way.
	original := "# Doc\n\n<!-- codeshot:diff:start -->\nstale\n<!-- codeshot:diff:end -->\n"
	if err := os.WriteFile(doc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI("--diff", "--diff-ref", "HEAD..HEAD", "--path", root, "--embed", doc, "--check", "--format", "svg")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, stderr)
	}
	wantMatch(t, `nothing to diagram, so nothing to check`, stdout)
	wantNoMatch(t, `refusing to overwrite`, stdout)
	wantNoMatch(t, `out of date`, stdout)
	if got := readFile(t, doc); got != original { // never writes, on this path either
		t.Errorf("doc was modified:\n%s", got)
	}
}

func TestCLIEmbedArchitectureRoundTripsWritesImageAndBlockCheckThenPassesDriftThenFails(t *testing.T) {
	root := requireSelfIndex(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "TECHNICAL.md")
	if err := os.WriteFile(doc, []byte("# Technical\n\nProse.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--architecture", "--path", root, "--embed", doc, "--format", "svg", "--max-symbols", "100000"}

	// 1) embed: writes the image next to the doc and inserts the block
	if code, _, stderr := runCLI(args...); code != 0 {
		t.Fatalf("embed: exit %d: %s", code, stderr)
	}
	md := readFile(t, doc)
	wantMatch(t, `<!-- codeshot:arch:start -->`, md)
	// #51: the image is `codeshot-arch.svg` — no checkout directory in its name —
	// so the committed markdown is reproducible across clones and --check is stable.
	wantMatch(t, `!\[Architecture — generated by codeshot\]\(codeshot-arch\.svg\)`, md)
	wantNoMatch(t, regexp.QuoteMeta(filepath.Base(root)), md)
	img := filepath.Join(dir, "codeshot-arch.svg")
	if _, err := os.Stat(img); err != nil {
		t.Fatalf("expected the image written next to the doc: %v", err)
	}

	// 2) --check on the freshly-embedded doc: up to date -> exit 0
	code, stdout, stderr := runCLI(append(args, "--check")...)
	if code != 0 {
		t.Fatalf("check: exit %d: %s", code, stderr)
	}
	wantMatch(t, `up to date`, stdout)

	// 3) drift: corrupt the committed image -> --check must fail (exit 1)
	if err := os.WriteFile(img, []byte("<svg>tampered</svg>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI(append(args, "--check")...)
	if code != 1 {
		t.Errorf("expected --check to fail on a stale image, exit %d", code)
	}
	wantMatch(t, `out of date`, stderr)
}

func TestCLIEmbedArchitectureGroupDepth2DefaultImageName(t *testing.T) {
	root := requireSelfIndex(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "DOC.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("--architecture", "--path", root, "--group-depth", "2", "--embed", doc, "--format", "svg", "--max-symbols", "100000"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "codeshot-arch-d2.svg")); err != nil {
		t.Errorf("expected codeshot-arch-d2.svg next to the doc: %v", err)
	}
}

func TestCLIOnAnUnindexedRepoPrintsACleanNoIndexMessageNotARawCodegraphError(t *testing.T) {
	requireBins(t, "codegraph", "dot")
	// A fresh dir under tmp with no .codegraph anywhere above it -> codegraph
	// reports "not initialized" rather than resolving a parent index.
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"app.py": "def hello():\n    pass\n"})
	code, _, stderr := runCLI("hello", "--path", dir)
	if code != 1 {
		t.Errorf("expected exit 1 on an unindexed repo, got %d", code)
	}
	wantMatch(t, `has no index for`, stderr)
	wantMatch(t, `codegraph init`, stderr)
	wantNoMatch(t, `did not return JSON|Command failed`, stderr) // the clean message, not the raw error
}
