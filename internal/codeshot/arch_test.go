package codeshot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// indexRow builds an index row as codegraph.ReadIndexRows returns it. An empty
// targetKind defaults to "function" and an empty qualified to the name.
func indexRow(name, filePath, target, edgeKind, targetFile, targetKind, qualified string) codegraph.IndexRow {
	if targetKind == "" {
		targetKind = "function"
	}
	if qualified == "" {
		qualified = name
	}
	return codegraph.IndexRow{Name: name, FilePath: filePath, QualifiedName: qualified, Target: target, EdgeKind: edgeKind, TargetKind: targetKind, TargetFile: targetFile}
}

func qSym(name, file, qualified string) Node {
	return Node{Name: name, FilePath: file, QualifiedName: strp(qualified)}
}

func TestFileEdgesFromIndexRowsCountsEachDefinitionOnceOverloadsTogetherInEnumerationOrder(t *testing.T) {
	symbols := []Node{
		qSym("f", "c/over.ts", "f"),
		qSym("f", "c/over.ts", "f"),
		qSym("run", "c/dual.js", "A.run"),
		qSym("run", "c/dual.js", "B.run"),
	}
	rows := []codegraph.IndexRow{
		// Two overload nodes of f, both calling alpha: one definition, one edge.
		indexRow("f", "c/over.ts", "fn:alpha", "calls", "a/alpha.js", "", ""),
		indexRow("f", "c/over.ts", "fn:alpha", "calls", "a/alpha.js", "", ""),
		// Two same-file methods are two definitions: each counts.
		indexRow("run", "c/dual.js", "fn:alpha", "calls", "a/alpha.js", "function", "A.run"),
		indexRow("run", "c/dual.js", "fn:alpha", "calls", "a/alpha.js", "function", "B.run"),
		indexRow("run", "c/dual.js", "fn:beta", "references", "b/beta.js", "function", "B.run"),
	}
	want := []SymbolEdge{
		{FromFile: "c/over.ts", ToFile: "a/alpha.js"},
		{FromFile: "c/dual.js", ToFile: "a/alpha.js"},
		{FromFile: "c/dual.js", ToFile: "a/alpha.js"},
		{FromFile: "c/dual.js", ToFile: "b/beta.js"},
	}
	if got := FileEdgesFromIndexRows(symbols, rows); !slices.Equal(got, want) {
		t.Errorf("FileEdgesFromIndexRows = %v, want %v", got, want)
	}
}

func TestFileEdgesFromIndexRowsDropsFileKindAndImportOnlyTargetsKeepsAnImportedTargetThatIsAlsoCalledAndIgnoresUnenumeratedSources(t *testing.T) {
	file := qSym("main.ts", "src/main.ts", "src/main.ts")
	rows := []codegraph.IndexRow{
		indexRow("main.ts", "src/main.ts", "fn:alpha", "imports", "src/alpha.ts", "function", "src/main.ts"),
		indexRow("main.ts", "src/main.ts", "fn:beta", "imports", "src/alpha.ts", "function", "src/main.ts"),
		indexRow("main.ts", "src/main.ts", "fn:beta", "calls", "src/alpha.ts", "function", "src/main.ts"),
		indexRow("main.ts", "src/main.ts", "file:x", "references", "src/x.ts", "file", "src/main.ts"),
		indexRow("hidden", "src/hidden.ts", "fn:alpha", "calls", "src/alpha.ts", "", ""),
	}
	want := []SymbolEdge{{FromFile: "src/main.ts", ToFile: "src/alpha.ts"}}
	if got := FileEdgesFromIndexRows([]Node{file}, rows); !slices.Equal(got, want) {
		t.Errorf("FileEdgesFromIndexRows = %v, want %v", got, want)
	}
	if got := FileEdgesFromIndexRows([]Node{file}, nil); len(got) != 0 {
		t.Errorf("FileEdgesFromIndexRows with no rows = %v, want none", got)
	}
}

func TestParseForbidRuleReadsFromToTrimsSpacesAndLeadingDotSlashAndRejectsMalformedRules(t *testing.T) {
	got, ok := ParseForbidRule("render/->test/")
	if want := (ForbidRule{From: "render/", To: "test/", Raw: "render/->test/"}); !ok || got != want {
		t.Errorf("ParseForbidRule(render/->test/) = %+v, %v; want %+v", got, ok, want)
	}
	got, ok = ParseForbidRule(" ./src/ui/ -> src/db.js ")
	if want := (ForbidRule{From: "src/ui/", To: "src/db.js", Raw: "src/ui/->src/db.js"}); !ok || got != want {
		t.Errorf("ParseForbidRule(spaced) = %+v, %v; want %+v", got, ok, want)
	}
	for _, bad := range []string{"render/", "->test/", "render/->", "a->b->c", ""} {
		if r, ok := ParseForbidRule(bad); ok {
			t.Errorf("expected %q to be rejected, got %+v", bad, r)
		}
	}
}

func mustRule(t *testing.T, raw string) ForbidRule {
	t.Helper()
	r, ok := ParseForbidRule(raw)
	if !ok {
		t.Fatalf("rule %q did not parse", raw)
	}
	return r
}

func weights(vs []Violation) []int {
	out := []int{}
	for _, v := range vs {
		out = append(out, v.Weight)
	}
	return out
}

func TestForbiddenEdgesMatchesATrailingSlashSideAsADirectoryAndAnyOtherSideAsOneExactFile(t *testing.T) {
	edges := []FileEdge{
		{From: "src/ui/page.js", To: "src/db/query.js", Weight: 2},
		{From: "src/ui/page.js", To: "src/core/api.js", Weight: 5},
		{From: "src/uix/other.js", To: "src/db/query.js", Weight: 9},
		{From: "src/core/api.js", To: "src/db/query.js", Weight: 4},
	}
	// src/ui/ must not match src/uix/ — a directory prefix ends at the slash.
	got := ForbiddenEdges(edges, []ForbidRule{mustRule(t, "src/ui/->src/db/")})
	if want := []Violation{{Rule: "src/ui/->src/db/", From: "src/ui/page.js", To: "src/db/query.js", Weight: 2}}; !slices.Equal(got, want) {
		t.Errorf("directory rule = %+v, want %+v", got, want)
	}
	if got := weights(ForbiddenEdges(edges, []ForbidRule{mustRule(t, "src/core/api.js->src/db/query.js")})); !slices.Equal(got, []int{4}) {
		t.Errorf("exact-file rule weights = %v, want [4]", got)
	}
	if got := ForbiddenEdges(edges, []ForbidRule{mustRule(t, "src/core->src/db/")}); len(got) != 0 {
		t.Errorf("no slash means an exact file, not a prefix; got %+v", got)
	}
	// Heaviest first across rules.
	both := ForbiddenEdges(edges, []ForbidRule{mustRule(t, "src/ui/->src/db/"), mustRule(t, "src/ui/->src/core/")})
	if got := weights(both); !slices.Equal(got, []int{5, 2}) {
		t.Errorf("weights across rules = %v, want [5 2]", got)
	}
	if got := ForbiddenEdges(edges, nil); len(got) != 0 {
		t.Errorf("no rules = %+v, want none", got)
	}
}

// keySet turns drawn-edge keys into sorted "from -> to" strings.
func keySet(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, strings.ReplaceAll(k, "\x00", " -> "))
	}
	slices.Sort(out)
	return out
}

func TestViolatingDrawnKeysMarksTheDirectoryEdgeAViolationRollsIntoAndDropsOneThatRollsIntoASingleGroup(t *testing.T) {
	v := []Violation{{From: "src/ui/page.js", To: "src/db/query.js"}, {From: "lib/a.js", To: "test/b.js"}}
	tests := []struct {
		depth int
		want  []string
		note  string
	}{
		{0, []string{"lib/a.js -> test/b.js", "src/ui/page.js -> src/db/query.js"}, "per-file"},
		{1, []string{"lib/ -> test/"}, "src/ui -> src/db is one group at depth 1"},
		{2, []string{"lib/ -> test/", "src/ui/ -> src/db/"}, "depth 2"},
	}
	for _, tt := range tests {
		if got := keySet(ViolatingDrawnKeys(v, tt.depth)); !slices.Equal(got, tt.want) {
			t.Errorf("ViolatingDrawnKeys(depth %d) = %v, want %v (%s)", tt.depth, got, tt.want, tt.note)
		}
	}
}

func TestAggregateFileEdgesDropsSelfFileEdges(t *testing.T) {
	edges := AggregateFileEdges([]SymbolEdge{{FromFile: "a.js", ToFile: "a.js"}, {FromFile: "a.js", ToFile: "b.js"}})
	if len(edges) != 1 || edges[0].To != "b.js" {
		t.Errorf("got %+v, want the single a.js -> b.js edge", edges)
	}
}

func TestAggregateFileEdgesDropsEdgesMissingARealFromToFilePath(t *testing.T) {
	edges := AggregateFileEdges([]SymbolEdge{
		{FromFile: "a.js", ToFile: ""},
		{FromFile: "", ToFile: "b.js"},
		{FromFile: "a.js", ToFile: "b.js"},
	})
	if len(edges) != 1 {
		t.Errorf("got %+v, want 1 edge", edges)
	}
}

func TestAggregateFileEdgesSumsRepeatedPairsIntoEdgeWeight(t *testing.T) {
	edges := AggregateFileEdges([]SymbolEdge{
		{FromFile: "a.js", ToFile: "b.js"},
		{FromFile: "a.js", ToFile: "b.js"},
		{FromFile: "a.js", ToFile: "b.js"},
	})
	if len(edges) != 1 || edges[0].Weight != 3 {
		t.Errorf("got %+v, want one edge of weight 3", edges)
	}
}

func TestTopFilesByWeightReturnsNilNoCapWhenMaxRenderIsUnset(t *testing.T) {
	edges := AggregateFileEdges([]SymbolEdge{{FromFile: "a.js", ToFile: "b.js"}})
	if got := TopFilesByWeight(edges, Unlimited); got != nil {
		t.Errorf("TopFilesByWeight(unlimited) = %v, want nil", got)
	}
}

func TestTopFilesByWeightKeepsOnlyTheTopNBusiestFiles(t *testing.T) {
	edges := []FileEdge{
		{From: "busy.js", To: "a.js", Weight: 5},
		{From: "busy.js", To: "b.js", Weight: 5},
		{From: "quiet.js", To: "c.js", Weight: 1},
	}
	kept := TopFilesByWeight(edges, 1)
	if len(kept) != 1 || !kept["busy.js"] {
		t.Errorf("TopFilesByWeight(1) = %v, want only busy.js", kept)
	}
}

func TestArchOutputBaseNameSanitizesARepoPathDownToItsBasename(t *testing.T) {
	if got := ArchOutputBaseName("/home/ericm/personal_projects/codeshot/master"); got != "master" {
		t.Errorf("ArchOutputBaseName = %q, want \"master\"", got)
	}
}

func TestGroupPath(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		depth int
		want  string
	}{
		{"rolls a nested file up to its first N directory segments (1)", "src/api/user.js", 1, "src/"},
		{"rolls a nested file up to its first N directory segments (2)", "src/api/user.js", 2, "src/api/"},
		// Grouping a root file under '' would merge every root-level file into one nameless box.
		{"leaves a repo-root file as itself", "index.js", 1, "index.js"},
		{"degrades to the file's own directories when depth exceeds the tree", "render/callgraph.js", 5, "render/"},
		{"splits Windows separators too", `src\api\user.js`, 1, "src/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GroupPath(tt.file, tt.depth); got != tt.want {
				t.Errorf("GroupPath(%q, %d) = %q, want %q", tt.file, tt.depth, got, tt.want)
			}
		})
	}
}

func TestRollupFileEdgesSumsTheWeightsOfEveryFilePairCollapsingIntoOneGroupPair(t *testing.T) {
	rolled := RollupFileEdges([]FileEdge{
		{From: "src/a.js", To: "lib/x.js", Weight: 2},
		{From: "src/b.js", To: "lib/y.js", Weight: 3},
	}, 1)
	if want := []FileEdge{{From: "src/", To: "lib/", Weight: 5}}; !slices.Equal(rolled, want) {
		t.Errorf("got %+v, want %+v", rolled, want)
	}
}

func TestRollupFileEdgesDropsEdgesThatBecomeSelfGroup(t *testing.T) {
	rolled := RollupFileEdges([]FileEdge{
		{From: "src/a.js", To: "src/b.js", Weight: 4},
		{From: "src/a.js", To: "lib/x.js", Weight: 1},
	}, 1)
	if want := []FileEdge{{From: "src/", To: "lib/", Weight: 1}}; !slices.Equal(rolled, want) {
		t.Errorf("got %+v, want %+v", rolled, want)
	}
}

func TestRollupFileEdgesIsANoOpWhenNoGroupDepthWasGiven(t *testing.T) {
	edges := []FileEdge{{From: "src/a.js", To: "src/b.js", Weight: 1}}
	if got := RollupFileEdges(edges, 0); !slices.Equal(got, edges) {
		t.Errorf("got %+v, want the input unchanged", got)
	}
}

// localeCompare with no explicit locale varies with the environment; code point
// order puts uppercase before lowercase and '_' (U+005F) after uppercase.
func TestSortSymbolsForEnumerationComparesByCodePointNotLocaleCollation(t *testing.T) {
	sorted := SortSymbolsForEnumeration([]Node{
		n("a", "src/api.js"),
		n("a", "src/Api.js"),
		n("a", "src/_x.js"),
	})
	var files []string
	for _, s := range sorted {
		files = append(files, s.FilePath)
	}
	if want := []string{"src/Api.js", "src/_x.js", "src/api.js"}; !slices.Equal(files, want) {
		t.Errorf("order = %v, want %v", files, want)
	}
}

func TestSortSymbolsForEnumerationOrdersByFilePathThenNameSoAMaxSymbolsSliceIsReproducible(t *testing.T) {
	sorted := SortSymbolsForEnumeration([]Node{n("zeta", "b.js"), n("beta", "a.js"), n("alpha", "a.js")})
	var got []string
	for _, s := range sorted {
		got = append(got, s.FilePath+":"+s.Name)
	}
	if want := []string{"a.js:alpha", "a.js:beta", "b.js:zeta"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestSortSymbolsForEnumerationDoesNotMutateItsInput(t *testing.T) {
	input := []Node{n("z", "b.js"), n("a", "a.js")}
	SortSymbolsForEnumeration(input)
	if input[0].Name != "z" {
		t.Errorf("input[0].Name = %q, want \"z\" (input must not be reordered)", input[0].Name)
	}
}

func TestDuplicateNamesReturnsOnlyNamesSeenInMoreThanOneFile(t *testing.T) {
	dupes := DuplicateNames([]Node{n("render", "a.js"), n("render", "b.js"), n("unique", "c.js")})
	if len(dupes) != 1 || !dupes["render"] {
		t.Errorf("DuplicateNames = %v, want only render", dupes)
	}
}

// Two methods named String() on different types in one file: re-probing by
// file would only lose edges, so same-file collisions are not duplicates.
func TestDuplicateNamesIgnoresSameNameSameFileSymbols(t *testing.T) {
	dupes := DuplicateNames([]Node{n("String", "svc.go"), n("String", "svc.go")})
	if len(dupes) != 0 {
		t.Errorf("DuplicateNames = %v, want none", dupes)
	}
}

// New behaviour (replaces the CLI fallback): --architecture errors instead of
// falling back when symbols lack qualifiedName or no index is found.
func TestCollectFileEdgesErrorsWhenSymbolsLackQualifiedName(t *testing.T) {
	_, err := collectFileEdges([]Node{n("a", "a.js")}, t.TempDir())
	var fe *codegraph.FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want a FatalError", err)
	}
	wantMatch(t, `needs codegraph 1\.6\.0 or later`, fe.Msg)
}

func TestCollectFileEdgesErrorsWhenNoIndexIsFound(t *testing.T) {
	_, err := collectFileEdges([]Node{qSym("a", "a.js", "a")}, t.TempDir())
	var fe *codegraph.FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want a FatalError", err)
	}
	wantMatch(t, `could not find codegraph's index`, fe.Msg)
}

// F12: two docs in one directory that embed different --path targets of one
// repo must not share a default image name, while the repo root keeps the
// checkout-independent name from #51.
func TestEmbedPathSuffixNamesASubdirectoryTargetButNotTheRepoRoot(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	for _, d := range []string{"services/api", "services/web"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]string{
		root:                                   "",
		filepath.Join(root, "services/api"):    "services_api",
		filepath.Join(root, "services/web"):    "services_web",
		filepath.Join(root, "services/api/.."): "services",
		t.TempDir():                            "", // not in a git repo
	}
	for path, want := range cases {
		if got := EmbedPathSuffix(path); got != want {
			t.Errorf("EmbedPathSuffix(%q) = %q, want %q", path, got, want)
		}
	}
}
