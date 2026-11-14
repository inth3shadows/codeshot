package codeshot

import (
	"slices"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

func names(nodes []Node) []string {
	out := []string{}
	for _, x := range nodes {
		out = append(out, x.Name)
	}
	return out
}

func TestMatchRootSymbolsKeepsOnlySymbolsWhoseFilePathIsAChangedFile(t *testing.T) {
	symbols := []Node{n("a", "src/a.js"), n("b", "src/b.js"), n("c", "src/c.js")}
	roots := MatchRootSymbols(symbols, []string{"src/a.js", "src/c.js"})
	if got := names(roots); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("roots = %v, want [a c]", got)
	}
}

func TestMatchRootSymbolsNormalizesBackslashSeparatorsOnBothSides(t *testing.T) {
	if got := MatchRootSymbols([]Node{n("a", `src\a.js`)}, []string{"src/a.js"}); len(got) != 1 {
		t.Errorf("got %v, want 1 match", got)
	}
}

func TestMatchRootSymbolsIgnoresSymbolsWithNoFilePathAndReturnsNoneOnNoMatch(t *testing.T) {
	if got := MatchRootSymbols([]Node{n("a", "")}, []string{"src/a.js"}); len(got) != 0 {
		t.Errorf("no filePath: got %v, want none", got)
	}
	if got := MatchRootSymbols([]Node{n("a", "src/a.js")}, []string{"src/other.js"}); len(got) != 0 {
		t.Errorf("no match: got %v, want none", got)
	}
}

// An import statement is not a changed symbol: a changed test/t.ts drew its
// `import ... from "../src/mcp/tools"` as a bold root, and probing that text as
// a name can resolve to some other symbol and draw its callers/callees.
func TestMatchRootSymbolsExcludesKindImportNodes(t *testing.T) {
	symbols := []Node{
		{Name: "../src/mcp/tools", Kind: "import", FilePath: "test/t.ts"},
		{Name: "runTest", Kind: "function", FilePath: "test/t.ts"},
	}
	if got := names(MatchRootSymbols(symbols, []string{"test/t.ts"})); !slices.Equal(got, []string{"runTest"}) {
		t.Errorf("roots = %v, want [runTest]", got)
	}
}

func TestMatchRootSymbolsExcludesKindFileIndexEntries(t *testing.T) {
	symbols := []Node{
		{Name: "a.js", FilePath: "a.js", Kind: "file"},
		{Name: "realSymbol", FilePath: "a.js"},
	}
	if got := names(MatchRootSymbols(symbols, []string{"a.js"})); !slices.Equal(got, []string{"realSymbol"}) {
		t.Errorf("roots = %v, want [realSymbol]", got)
	}
}

// Shape copied from the real (upstream #1801, post-1.6.0 fork build) `callers
// --json -- parse` on a fixture with `parse` defined in both a/parse.js and b/parse.js.
func twoParseCallers() *codegraph.Response {
	def := func(file string, callers []codegraph.Node) codegraph.Definition {
		return codegraph.Definition{
			Definition: &codegraph.DefinitionRef{FilePath: strp(file)},
			Callers:    callers, Total: f64p(1), Truncated: boolp(false),
		}
	}
	useA := codegraph.Node{Name: "useA", Kind: "function", FilePath: "a/use.js"}
	useB := codegraph.Node{Name: "useB", Kind: "function", FilePath: "b/use.js"}
	return &codegraph.Response{
		Callers: []codegraph.Node{useA, useB},
		Total:   f64p(2), Truncated: boolp(false),
		Definitions: []codegraph.Definition{def("a/parse.js", []codegraph.Node{useA}), def("b/parse.js", []codegraph.Node{useB})},
	}
}

func twoParseCallees() *codegraph.Response {
	alpha := codegraph.Node{Name: "alpha", FilePath: "a/parse.js"}
	beta := codegraph.Node{Name: "beta", FilePath: "b/parse.js"}
	def := func(file string, callees []codegraph.Node) codegraph.Definition {
		return codegraph.Definition{
			Definition: &codegraph.DefinitionRef{FilePath: strp(file)},
			Callees:    callees, Total: f64p(1), Truncated: boolp(false),
		}
	}
	return &codegraph.Response{
		Callees:     []codegraph.Node{alpha, beta},
		Definitions: []codegraph.Definition{def("a/parse.js", []codegraph.Node{alpha}), def("b/parse.js", []codegraph.Node{beta})},
	}
}

func TestPickDefinitionResultKeepsOnlyTheDefinitionInTheRootsFileNotTheBareNameUnion(t *testing.T) {
	r := PickDefinitionResult(twoParseCallers(), "callers", "a/parse.js")
	if r == nil {
		t.Fatal("PickDefinitionResult = nil, want the a/parse.js slice")
	}
	if got := names(r.Callers); !slices.Equal(got, []string{"useA"}) {
		t.Errorf("callers = %v, want [useA]", got)
	}
	if got, want := ReadTruncation(r.Truncated, r.Total, r.Callers, 20), (Truncation{Truncated: false, Total: 1}); got != want {
		t.Errorf("truncation = %+v, want %+v", got, want)
	}
}

func TestPickDefinitionResultUnionsEveryDefinitionInTheSameFileASameFileCollisionIsOneIdentity(t *testing.T) {
	def := func(file string, callees []codegraph.Node, total float64, trunc bool) codegraph.Definition {
		return codegraph.Definition{Definition: &codegraph.DefinitionRef{FilePath: strp(file)}, Callees: callees, Total: f64p(total), Truncated: boolp(trunc)}
	}
	sameFile := &codegraph.Response{Definitions: []codegraph.Definition{
		def("x.go", []codegraph.Node{{Name: "a"}}, 1, false),
		def("x.go", []codegraph.Node{{Name: "b"}}, 3, true),
		def("y.go", []codegraph.Node{{Name: "c"}}, 1, false),
	}}
	r := PickDefinitionResult(sameFile, "callees", "x.go")
	if r == nil {
		t.Fatal("PickDefinitionResult = nil")
	}
	if got := names(r.Callees); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("callees = %v, want [a b]", got)
	}
	if got, want := ReadTruncation(r.Truncated, r.Total, r.Callees, 20), (Truncation{Truncated: true, Total: 4}); got != want {
		t.Errorf("truncation = %+v, want %+v", got, want)
	}
}

// Return nil — fall back to the union — without definitions (codegraph 1.5.0) or without a match.
func TestPickDefinitionResultReturnsNilWithoutDefinitionsOrWithoutAMatch(t *testing.T) {
	if got := PickDefinitionResult(&codegraph.Response{Callers: []codegraph.Node{{Name: "useA"}}}, "callers", "a/parse.js"); got != nil {
		t.Errorf("no definitions: got %+v, want nil", got)
	}
	if got := PickDefinitionResult(nil, "callers", "a/parse.js"); got != nil {
		t.Errorf("nil response: got %+v, want nil", got)
	}
	if got := PickDefinitionResult(twoParseCallers(), "callers", "c/parse.js"); got != nil {
		t.Errorf("no match: got %+v, want nil", got)
	}
}

func TestPickDefinitionResultLeavesTotalTruncatedUnknownWhenAKeptDefinitionDoesNotReportThem(t *testing.T) {
	r := PickDefinitionResult(&codegraph.Response{Definitions: []codegraph.Definition{
		{Definition: &codegraph.DefinitionRef{FilePath: strp("a.js")}, Callers: []codegraph.Node{}, Truncated: boolp(false)},
	}}, "callers", "a.js")
	if r == nil || r.Total != nil {
		t.Errorf("total = %v, want unreported (nil)", r)
	}
	noTrunc := PickDefinitionResult(&codegraph.Response{Definitions: []codegraph.Definition{
		{Definition: &codegraph.DefinitionRef{FilePath: strp("a.js")}, Callers: []codegraph.Node{{Name: "1"}, {Name: "2"}}},
	}}, "callers", "a.js")
	if noTrunc == nil || noTrunc.Truncated != nil {
		t.Fatalf("truncated = %v, want unreported (nil)", noTrunc)
	}
	if got, want := ReadTruncation(noTrunc.Truncated, noTrunc.Total, noTrunc.Callers, 2), (Truncation{Truncated: true, Total: -1}); got != want {
		t.Errorf("truncation = %+v, want %+v", got, want)
	}
}

func TestResolveRootResultsNarrowsBothDirectionsToTheRootsFileAndReportsItResolved(t *testing.T) {
	callers, callees, unresolved := ResolveRootResults(twoParseCallers(), twoParseCallees(), "a/parse.js")
	if got := names(callers.Callers); !slices.Equal(got, []string{"useA"}) {
		t.Errorf("callers = %v, want [useA]", got)
	}
	if got := names(callees.Callees); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("callees = %v, want [alpha]", got)
	}
	if unresolved {
		t.Error("unresolved = true, want false")
	}
}

// codegraph 1.5.0 shape: no definitions[].
func TestResolveRootResultsKeepsTheUnionAndFlagsTheRootWhenCodegraphGivesNoPerFileAnswer(t *testing.T) {
	union := &codegraph.Response{Callers: twoParseCallers().Callers}
	callers, callees, unresolved := ResolveRootResults(union, twoParseCallees(), "a/parse.js")
	if callers != union {
		t.Error("callers result must be the very union response")
	}
	if got := names(callees.Callees); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("callees = %v, want [alpha]", got)
	}
	if !unresolved {
		t.Error("unresolved = false, want true")
	}
}

func TestResolveRootResultsANilDirectionAddsNoEdgesAndDoesNotDragTheOtherBackToTheUnion(t *testing.T) {
	callers, callees, unresolved := ResolveRootResults(twoParseCallers(), nil, "a/parse.js")
	if got := names(callers.Callers); !slices.Equal(got, []string{"useA"}) {
		t.Errorf("callers = %v, want [useA]", got)
	}
	if callees != nil {
		t.Errorf("callees = %+v, want nil", callees)
	}
	if unresolved {
		t.Error("unresolved = true, want false")
	}
	if _, _, un := ResolveRootResults(nil, nil, "a/parse.js"); un {
		t.Error("both nil: unresolved = true, want false")
	}
}

func TestHandleEmptyRootsLogsOnlyTheWarnMessageNeverExitsWhenThereIsNoEmbedFile(t *testing.T) {
	var stderr strings.Builder
	if err := handleEmptyRoots(DiffParams{}, "REFUSAL", "WARN", "NOTHING_TO_CHECK", &stderr); err != nil {
		t.Fatalf("handleEmptyRoots = %v, want nil", err)
	}
	if got := stderr.String(); got != "WARN\n" {
		t.Errorf("stderr = %q, want %q", got, "WARN\n")
	}
}

// The JS test left these branches to the CLI tests (calling process.exit
// in-process would kill the runner); in Go they return an *Exit.
func TestHandleEmptyRootsWithAnEmbedFileRefusesOrReportsNothingToCheck(t *testing.T) {
	var stderr strings.Builder
	err := handleEmptyRoots(DiffParams{EmbedFile: "docs.md"}, "REFUSAL", "WARN", "NOTHING_TO_CHECK", &stderr)
	if ex, ok := err.(*Exit); !ok || ex.Code != 1 || ex.Stderr != "REFUSAL" {
		t.Errorf("--embed: err = %#v, want exit 1 with the refusal", err)
	}
	err = handleEmptyRoots(DiffParams{EmbedFile: "docs.md", Check: true}, "REFUSAL", "WARN", "NOTHING_TO_CHECK", &stderr)
	if ex, ok := err.(*Exit); !ok || ex.Code != 0 || ex.Stdout != "NOTHING_TO_CHECK" {
		t.Errorf("--embed --check: err = %#v, want exit 0 with nothing-to-check", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}
