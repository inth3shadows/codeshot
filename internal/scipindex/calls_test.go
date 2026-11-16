package scipindex

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
)

// at is an occurrence on 1-based line, with an enclosing range to end when
// end > 0.
func at(sym string, roles scip.SymbolRole, line, end int) *scip.Occurrence {
	o := &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
	o.SetSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), 5, 9}))
	if end > 0 {
		// Starts a line early, as scip-go's do at a doc comment.
		o.SetEnclosingSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 2), 0, int32(end - 1), 1}))
	}
	return o
}

const (
	goPkg    = "scip-go gomod m v1 `m/p`/"
	goRun    = goPkg + "Run()."
	goT      = goPkg + "T#"
	goTM     = goPkg + "T#M()."
	goField  = goPkg + "T#F."
	goConst  = goPkg + "Limit."
	goHelper = goPkg + "Helper()."
	goUnused = goPkg + "Bare()."
	tsArrow  = "scip-typescript npm m 1.0.0 src/`x.ts`/handler."
	tsVal    = "scip-typescript npm m 1.0.0 src/`x.ts`/value."
)

// p/a.go: Run (lines 3-9) calls Helper and T.M at line 5, reads Limit, and
// a nested literal at line 7 refers to T#F (a field, not drawn). T (11-20)
// holds M (13-16), which calls Helper and itself. Line 1 is module level
// and calls Run. Helper is in p/b.go; Bare has no enclosing range. x.ts has
// a function-valued const (ranged) calling Helper, and a plain const.
func callsFixture() *scip.Index {
	return &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-go"}},
		Documents: []*scip.Document{
			{RelativePath: "p/a.go", Language: "go",
				Symbols: []*scip.SymbolInformation{
					{Symbol: goConst, Kind: scip.SymbolInformation_Constant},
					{Symbol: goField, Kind: scip.SymbolInformation_Field},
				},
				Occurrences: []*scip.Occurrence{
					at(goPkg, scip.SymbolRole_Definition, 1, 0),
					at(goRun, 0, 1, 0),
					at(goConst, scip.SymbolRole_Definition, 2, 2),
					at(goRun, scip.SymbolRole_Definition, 3, 9),
					at(goHelper, 0, 5, 0),
					at(goTM, 0, 5, 0),
					at(goConst, 0, 6, 0),
					at(goField, 0, 7, 0),
					at("local 1", 0, 7, 0),
					at(goPkg, 0, 8, 0),
					at(goT, scip.SymbolRole_Definition, 11, 20),
					at(goField, scip.SymbolRole_Definition, 12, 12),
					at(goTM, scip.SymbolRole_Definition, 13, 16),
					at(goHelper, 0, 14, 0),
					at(goHelper, scip.SymbolRole_ReadAccess, 15, 0),
					at(goTM, 0, 15, 0),
					at(goUnused, scip.SymbolRole_Definition, 22, 0),
				}},
			{RelativePath: "p/b.go", Language: "go", Occurrences: []*scip.Occurrence{
				at(goHelper, scip.SymbolRole_Definition, 3, 5),
				at(goRun, scip.SymbolRole_Import, 1, 0),
			}},
			{RelativePath: "src/x.ts", Language: "TypeScript",
				Symbols: []*scip.SymbolInformation{
					{Symbol: tsArrow, Kind: scip.SymbolInformation_Constant},
					{Symbol: tsVal, Kind: scip.SymbolInformation_Constant},
				},
				Occurrences: []*scip.Occurrence{
					at(tsArrow, scip.SymbolRole_Definition, 2, 4),
					at(goHelper, 0, 3, 0),
					at(tsVal, scip.SymbolRole_Definition, 6, 0),
				}},
		},
	}
}

func readCalls(t *testing.T) *CallGraph {
	t.Helper()
	x, err := Parse(encode(t, callsFixture()))
	if err != nil {
		t.Fatal(err)
	}
	return x.Calls()
}

func TestCallsDrawsFunctionsMethodsTypesAndTopLevelValues(t *testing.T) {
	g := readCalls(t)
	var got []string
	for _, d := range g.Defs {
		got = append(got, fmt.Sprintf("%s %s %s %s %d-%d %v", d.Path, d.Name, d.QName, d.Kind, d.Line, d.End, d.Container))
	}
	want := []string{
		"p/a.go Limit Limit constant 2-2 false",
		"p/a.go Run Run function 3-9 true",
		"p/a.go T T type 11-20 true",
		"p/a.go M T.M method 13-16 true",
		"p/a.go Bare Bare function 22-22 false",
		"p/b.go Helper Helper function 3-5 true",
		"src/x.ts handler handler constant 2-4 true",
		"src/x.ts value value constant 6-6 false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant:\n%v", got, want)
	}
	if g.Unranged != 1 {
		t.Errorf("unranged %d, want 1 (Bare)", g.Unranged)
	}
}

// The caller is the innermost container from its name line on, else the
// file; imports, locals, packages, fields, self-references and repeats
// draw nothing.
func TestCallsAttributeEachReferenceToTheInnermostDefinition(t *testing.T) {
	got := refStrings(readCalls(t), goPkg)
	want := []string{
		"p/a.go -> Run().",
		"Run -> Helper().",
		"Run -> Limit.",
		"Run -> T#M().",
		"T.M -> Helper().",
		"handler -> Helper().",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs:\n%v\nwant:\n%v", got, want)
	}
}

// atc is an occurrence at 1-based line and 0-based column, with an
// enclosing range from there to (endLine, endCol) when endLine > 0.
func atc(sym string, roles scip.SymbolRole, line, col, endLine, endCol int) *scip.Occurrence {
	o := &scip.Occurrence{Symbol: sym, SymbolRoles: int32(roles)}
	o.SetSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), int32(col), int32(col + 1)}))
	if endLine > 0 {
		o.SetEnclosingSourceRange(scip.NewRangeUnchecked([]int32{int32(line - 1), int32(col), int32(endLine - 1), int32(endCol)}))
	}
	return o
}

func refStrings(g *CallGraph, prefix string) []string {
	var got []string
	for _, r := range g.Refs {
		from := r.Path
		if r.Caller >= 0 {
			from = g.Defs[r.Caller].QName
		}
		got = append(got, from+" -> "+r.Callee[len(prefix):])
	}
	return got
}

// F1/F2 (PR #68 review), in y.ts:
//
//	1 const a = () => h(); const b = () => a();
//	2 function f() {
//	3   g()
//	4 }; h()
//	5 function o(x: string)
//	6 function o(x: number)
//	7 function o(x) {
//	8   g()
//	9 }
//	10 function k() { o(1) }
//
// Two definitions on one line are told apart by column, code after a
// closing brace is the file's, and o's three definitions are one Def.
func TestCallsTellApartDefinitionsByColumnAndMergeRepeatedDefinitions(t *testing.T) {
	const ts = "scip-typescript npm m 1.0.0 src/`y.ts`/"
	fn := func(name string) *scip.SymbolInformation {
		return &scip.SymbolInformation{Symbol: ts + name + "().", Kind: scip.SymbolInformation_Function}
	}
	a, b, f, g, h, o, k := ts+"a.", ts+"b.", ts+"f().", ts+"g().", ts+"h().", ts+"o().", ts+"k()."
	x, err := Parse(encode(t, &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "scip-typescript"}},
		Documents: []*scip.Document{{RelativePath: "src/y.ts", Language: "TypeScript",
			Symbols: []*scip.SymbolInformation{
				{Symbol: a, Kind: scip.SymbolInformation_Constant}, {Symbol: b, Kind: scip.SymbolInformation_Constant},
				fn("f"), fn("g"), fn("h"), fn("o"), fn("k"),
			},
			Occurrences: []*scip.Occurrence{
				atc(a, scip.SymbolRole_Definition, 1, 6, 1, 19),
				atc(h, 0, 1, 16, 0, 0),
				atc(b, scip.SymbolRole_Definition, 1, 27, 1, 41),
				atc(a, 0, 1, 37, 0, 0),
				atc(f, scip.SymbolRole_Definition, 2, 9, 4, 1),
				atc(g, 0, 3, 2, 0, 0),
				atc(h, 0, 4, 3, 0, 0),
				atc(o, scip.SymbolRole_Definition, 5, 9, 5, 21),
				atc(o, scip.SymbolRole_Definition, 6, 9, 6, 21),
				atc(o, scip.SymbolRole_Definition, 7, 9, 9, 1),
				atc(g, 0, 8, 2, 0, 0),
				atc(k, scip.SymbolRole_Definition, 10, 9, 10, 22),
				atc(o, 0, 10, 15, 0, 0),
				atc(g, scip.SymbolRole_Definition, 11, 9, 11, 20),
				atc(h, scip.SymbolRole_Definition, 12, 9, 12, 20),
			}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	cg := x.Calls()
	want := []string{"src/y.ts -> h().", "a -> h().", "b -> a.", "f -> g().", "o -> g().", "k -> o()."}
	if got := refStrings(cg, ts); !reflect.DeepEqual(got, want) {
		t.Errorf("refs:\n%v\nwant:\n%v", got, want)
	}
	var defs []string
	for _, d := range cg.Defs {
		if d.Name == "o" {
			defs = append(defs, fmt.Sprintf("%d-%d", d.Line, d.End))
		}
	}
	if want := []string{"5-9"}; !reflect.DeepEqual(defs, want) {
		t.Errorf("o's defs %v, want %v (one Def for its three definitions)", defs, want)
	}
}
