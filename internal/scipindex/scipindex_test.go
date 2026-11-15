package scipindex

import (
	"reflect"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

const (
	pkgA   = "scip-go gomod example.com/m v1 `example.com/m/a`/"
	pkgB   = "scip-go gomod example.com/m v1 `example.com/m/b`/"
	funcB  = "scip-go gomod example.com/m v1 `example.com/m/b`/Helper()."
	typeB  = "scip-go gomod example.com/m v1 `example.com/m/b`/Config#"
	funcA  = "scip-go gomod example.com/m v1 `example.com/m/a`/Run()."
	extern = "scip-go gomod github.com/other/lib v2 `github.com/other/lib`/Do()."
)

func occ(sym string, roles scip.SymbolRole) *scip.Occurrence {
	return &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
}

func encode(t *testing.T, x *scip.Index) []byte {
	t.Helper()
	b, err := proto.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// a.go defines Run, imports package b, calls b.Helper twice, uses b.Config,
// calls itself, an external function and a local; b.go defines Helper and
// Config; c.go only refers to b's package.
func fixture() *scip.Index {
	return &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
		Documents: []*scip.Document{
			{RelativePath: "a/a.go", Occurrences: []*scip.Occurrence{
				occ(pkgA, scip.SymbolRole_Definition),
				occ(pkgB, scip.SymbolRole_Import),
				occ(funcA, scip.SymbolRole_Definition),
				occ(funcB, 0),
				occ(funcB, scip.SymbolRole_ReadAccess),
				occ(typeB, 0),
				occ(funcA, 0),
				occ(extern, 0),
				occ("local 3", 0),
			}},
			{RelativePath: "b/b.go", Occurrences: []*scip.Occurrence{
				occ(pkgB, scip.SymbolRole_Definition),
				occ(funcB, scip.SymbolRole_Definition),
				occ(typeB, scip.SymbolRole_Definition),
			}, Symbols: []*scip.SymbolInformation{{Symbol: typeB, Kind: scip.SymbolInformation_Struct}}},
			{RelativePath: "c/c.go", Occurrences: []*scip.Occurrence{occ(pkgB, 0)}},
			{RelativePath: "../../.cache/go-build/x-d", Occurrences: []*scip.Occurrence{occ(funcB, 0)}},
		},
	}
}

func TestParseKeepsDefsAndDistinctNonImportRefs(t *testing.T) {
	x, err := Parse(encode(t, fixture()))
	if err != nil {
		t.Fatal(err)
	}
	if x.Tool != "scip-go" {
		t.Errorf("tool %q", x.Tool)
	}
	var paths []string
	for _, d := range x.Docs {
		paths = append(paths, d.Path)
	}
	if !reflect.DeepEqual(paths, []string{"a/a.go", "b/b.go", "c/c.go"}) {
		t.Errorf("docs %v: the out-of-root build-cache document must be dropped", paths)
	}
	a := x.Docs[0]
	if want := []string{funcA, typeB, funcB, extern}; !reflect.DeepEqual(a.Refs, want) {
		t.Errorf("a refs %v, want %v (no import, local or repeat)", a.Refs, want)
	}
	b := x.Docs[1]
	want := []Def{{pkgB, "example.com/m/b", "namespace"}, {typeB, "Config", "struct"}, {funcB, "Helper", "method"}}
	if !reflect.DeepEqual(b.Defs, want) {
		t.Errorf("b defs %+v, want %+v", b.Defs, want)
	}
}

func TestFileEdgesFollowReferencesToOtherProjectFiles(t *testing.T) {
	x, err := Parse(encode(t, fixture()))
	if err != nil {
		t.Fatal(err)
	}
	want := []FileEdge{{"a/a.go", "b/b.go", typeB}, {"a/a.go", "b/b.go", funcB}}
	if got := x.FileEdges(nil); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v (c.go's package-only reference draws nothing)", got, want)
	}
	if got := x.FileEdges(map[string]bool{"b/b.go": true}); len(got) != 0 {
		t.Errorf("edges from b only: %v", got)
	}
}

func TestFileEdgesGoToEveryFileDefiningTheSymbol(t *testing.T) {
	x := &Index{Docs: []Doc{
		{Path: "z.ts", Defs: []Def{{Symbol: "s f.", Name: "f"}}},
		{Path: "m.ts", Refs: []string{"s f."}},
		{Path: "y.ts", Defs: []Def{{Symbol: "s f.", Name: "f"}}},
	}}
	want := []FileEdge{{"m.ts", "y.ts", "s f."}, {"m.ts", "z.ts", "s f."}}
	if got := x.FileEdges(nil); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v", got, want)
	}
}

func TestParseRejectsWhatCannotNameDiagramNodes(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"garbage", []byte{0xff, 0xff, 0xff}, "not a SCIP index"},
		{"empty", nil, "no documents"},
		{"empty path", encode(t, &scip.Index{Documents: []*scip.Document{{}}}), `relative_path "" is not a clean path`},
		{"unclean path", encode(t, &scip.Index{Documents: []*scip.Document{{RelativePath: "a/./b.go"}}}), `documents[0]: relative_path "a/./b.go"`},
	} {
		_, err := Parse(c.data)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
	}
}

// F1: the indexer's document and occurrence order must not reach the edges,
// or the same code draws a different DOT and --check fails.
func TestFileEdgesDoNotDependOnIndexOrder(t *testing.T) {
	fwd := fixture()
	rev := fixture()
	docs := rev.Documents
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	occs := docs[len(docs)-1].Occurrences // a/a.go, now last
	for i, j := 0, len(occs)-1; i < j; i, j = i+1, j-1 {
		occs[i], occs[j] = occs[j], occs[i]
	}
	a, err := Parse(encode(t, fwd))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(encode(t, rev))
	if err != nil {
		t.Fatal(err)
	}
	if ea, eb := a.FileEdges(nil), b.FileEdges(nil); !reflect.DeepEqual(ea, eb) {
		t.Errorf("edges differ with index order:\n%v\n%v", ea, eb)
	}
	if !reflect.DeepEqual(a.Docs, b.Docs) {
		t.Errorf("docs differ with index order")
	}
}

// F3: an indexer may split one file across several Document messages; they
// are one file, not an error.
func TestParseMergesDocumentsSplitAcrossMessages(t *testing.T) {
	x, err := Parse(encode(t, &scip.Index{Documents: []*scip.Document{
		{RelativePath: "a.go", Occurrences: []*scip.Occurrence{occ(funcB, 0)}},
		{RelativePath: "b.go", Occurrences: []*scip.Occurrence{occ(funcB, scip.SymbolRole_Definition)}},
		{RelativePath: "a.go", Occurrences: []*scip.Occurrence{occ(typeB, 0)}},
		{RelativePath: "b.go", Occurrences: []*scip.Occurrence{occ(typeB, scip.SymbolRole_Definition)}},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	want := []FileEdge{{"a.go", "b.go", typeB}, {"a.go", "b.go", funcB}}
	if got := x.FileEdges(nil); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v", got, want)
	}
}

// F5: a path outside the root is skipped however it is spelled; "." is no file.
func TestParseSkipsOutOfRootPathsBeforeTheCleanCheck(t *testing.T) {
	x, err := Parse(encode(t, &scip.Index{Documents: []*scip.Document{
		{RelativePath: "a.go"}, {RelativePath: "a/../../x.go"}, {RelativePath: "/abs/dir/"}, {RelativePath: "../g/./y.go"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Docs) != 1 || x.Docs[0].Path != "a.go" {
		t.Errorf("docs %+v, want only a.go", x.Docs)
	}
	_, err = Parse(encode(t, &scip.Index{Documents: []*scip.Document{{RelativePath: "."}}}))
	if err == nil || !strings.Contains(err.Error(), `relative_path "." is not a file path`) {
		t.Errorf("err %v", err)
	}
}
