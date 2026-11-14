package codeshot

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// fiveFns is Fn0..Fn4 in src/fn0.js..src/fn4.js.
func fiveFns() []Node {
	var out []Node
	for i := 0; i < 5; i++ {
		d := string(rune('0' + i))
		out = append(out, n("Fn"+d, "src/fn"+d+".js"))
	}
	return out
}

func TestBuildDotEmitsDigraphHeaderAndHighlightedSymbolNode(t *testing.T) {
	dot := BuildDot("RollAutoSnapshot", nil, nil, unlimitedDot)
	wantMatch(t, `^digraph callgraph \{`, dot)
	wantMatch(t, `"RollAutoSnapshot" \[fillcolor="#e2e8f0"`, dot)
}

func TestBuildDotDrawsCallerToSymbolAndSymbolToCalleeEdges(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "src/caller.js")}, []Node{n("Callee", "src/callee.js")}, unlimitedDot)
	wantMatch(t, `"Caller" -> "Target";`, dot)
	wantMatch(t, `"Target" -> "Callee";`, dot)
}

func TestBuildDotDashesEdgesFromTestCallers(t *testing.T) {
	dot := BuildDot("Target", []Node{n("CallerTest", "src/caller.test.js")}, nil, unlimitedDot)
	wantMatch(t, `"CallerTest" -> "Target" \[style=dashed, label="test"\];`, dot)
}

func TestBuildDotEscapesDoubleQuotesInNames(t *testing.T) {
	dot := BuildDot(`Weird"Name`, nil, nil, unlimitedDot)
	wantMatch(t, `"Weird\\"Name"`, dot)
}

func TestBuildDotDedupesRepeatedCallerCalleeEntries(t *testing.T) {
	caller, callee := n("Caller", "src/caller.js"), n("Callee", "src/callee.js")
	dot := BuildDot("Target", []Node{caller, caller}, []Node{callee, callee}, unlimitedDot)
	if got := countMatches(`"Caller" -> "Target"`, dot); got != 1 {
		t.Errorf(`"Caller" -> "Target" appears %d times, want 1`, got)
	}
	if got := countMatches(`"Target" -> "Callee"`, dot); got != 1 {
		t.Errorf(`"Target" -> "Callee" appears %d times, want 1`, got)
	}
}

// Two callers named "Caller" in different files are distinct symbols. Graphviz
// keys a node by the string in the edge, so emitting both as `"Caller" -> "Target"`
// would collapse them into ONE box. Each must therefore get a file-qualified id
// AND a disambiguating label.
func TestBuildDotKeepsDistinctCallersWithTheSameNameButDifferentFilePath(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "a.js"), n("Caller", "b.js")}, nil, unlimitedDot)
	wantMatch(t, `"Caller@@a\.js" -> "Target";`, dot)
	wantMatch(t, `"Caller@@b\.js" -> "Target";`, dot)
	wantNoMatch(t, `"Caller" -> "Target"`, dot) // the bare shared id is the collapse this guards against
	wantMatch(t, `"Caller@@a\.js" \[label="Caller\\n\(a\.js\)"\];`, dot)
	wantMatch(t, `"Caller@@b\.js" \[label="Caller\\n\(b\.js\)"\];`, dot)
}

// The render-level guard: BuildDot's DOT text can look right while graphviz
// still merges nodes. Prove the fix by counting nodes graphviz actually draws.
func TestBuildDotRendersSameNamedCallersAsTwoDistinctBoxesInTheRealImage(t *testing.T) {
	requireBins(t, "dot")
	dot := BuildDot("Target", []Node{n("handle", "a.js"), n("handle", "b.js")}, nil, unlimitedDot)
	svg := runDotSvg(t, dot)
	// 3 nodes: the root "Target" plus the two distinct "handle" callers.
	if got := countMatches(`class="node"`, svg); got != 3 {
		t.Errorf("rendered %d node boxes, want 3 (Target + two distinct handles), not a collapsed 2", got)
	}
}

func runDotSvg(t *testing.T, dot string) string {
	t.Helper()
	cmd := exec.Command("dot", "-Tsvg")
	cmd.Stdin = strings.NewReader(dot)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("dot rejected the DOT: %v\n%s", err, errb.String())
	}
	return out.String()
}

func TestBuildDotOmitsTooltipsByDefault(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "src/caller.js")}, nil, unlimitedDot)
	wantNoMatch(t, `tooltip=`, dot)
}

func TestBuildDotWithTooltipsDeclaresEachNodeWithItsFilePath(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "src/caller.js")}, []Node{n("Callee", "src/callee.js")},
		DotOptions{MaxRender: Unlimited, Tooltips: true})
	wantMatch(t, `"Caller" \[tooltip="src/caller\.js"\];`, dot)
	wantMatch(t, `"Callee" \[tooltip="src/callee\.js"\];`, dot)
}

func TestBuildDotTooltipsCombineWithADisambiguatingLabelOnACollidingNode(t *testing.T) {
	dot := BuildDot("Target", []Node{n("handle", "a.js"), n("handle", "b.js")}, nil,
		DotOptions{MaxRender: Unlimited, Tooltips: true})
	wantMatch(t, `"handle@@a\.js" \[label="handle\\n\(a\.js\)", tooltip="a\.js"\];`, dot)
}

func TestBuildDotTooltipsReachTheRenderedSvgAsHoverableTitles(t *testing.T) {
	requireBins(t, "dot")
	dot := BuildDot("Target", []Node{n("Caller", "src/caller.js")}, nil, DotOptions{MaxRender: Unlimited, Tooltips: true})
	svg := runDotSvg(t, dot)
	wantMatch(t, `xlink:title="src/caller\.js"`, svg)
}

func TestBuildDotRendersEveryCallerCalleeWhenMaxRenderIsOmitted(t *testing.T) {
	many := fiveFns()
	dot := BuildDot("Target", many, many, unlimitedDot)
	if got := countMatches(`-> "Target"`, dot); got != 5 {
		t.Errorf(`callers drawn = %d, want 5`, got)
	}
	if got := countMatches(`"Target" ->`, dot); got != 5 {
		t.Errorf(`callees drawn = %d, want 5`, got)
	}
}

func TestBuildDotTreatsMaxRenderAsOneSharedBudgetNotACapPerDirection(t *testing.T) {
	many := fiveFns()
	dot := BuildDot("Target", many, many, DotOptions{MaxRender: 2})
	// callers are spent first (priority order: direct trail before anything
	// else), so with a shared budget of 2 the callees get none.
	if got := countMatches(`-> "Target"`, dot); got != 2 {
		t.Errorf("callers drawn = %d, want 2", got)
	}
	if got := countMatches(`"Target" ->`, dot); got != 0 {
		t.Errorf("callees drawn = %d, want 0", got)
	}
	wantMatch(t, `"Fn0" -> "Target"`, dot)
	wantMatch(t, `"Fn1" -> "Target"`, dot)
	wantNoMatch(t, `"Fn2" -> "Target"`, dot)
}

func TestBuildDotSharedMaxRenderBudgetSpendsWhatCallersLeaveOverOnCallees(t *testing.T) {
	var callers, callees []Node
	for i := 0; i < 3; i++ {
		d := string(rune('0' + i))
		callers = append(callers, n("Caller"+d, "src/c"+d+".js"))
	}
	for i := 0; i < 5; i++ {
		d := string(rune('0' + i))
		callees = append(callees, n("Callee"+d, "src/e"+d+".js"))
	}
	dot := BuildDot("Target", callers, callees, DotOptions{MaxRender: 5})
	if got := countMatches(`-> "Target"`, dot); got != 3 {
		t.Errorf("callers drawn = %d, want 3 (all 3 fit)", got)
	}
	if got := countMatches(`"Target" ->`, dot); got != 2 {
		t.Errorf("callees drawn = %d, want 2 (only 2 of 5 fit the remaining budget)", got)
	}
}

func TestBuildDotMaxRenderAppliesAfterDedupNotBefore(t *testing.T) {
	dupe := n("Caller", "src/caller.js")
	dot := BuildDot("Target", []Node{dupe, dupe, n("Other", "src/other.js")}, nil, DotOptions{MaxRender: 2})
	wantMatch(t, `"Caller" -> "Target"`, dot)
	wantMatch(t, `"Other" -> "Target"`, dot)
}

func TestBuildDotRendersTransitiveEdgesAlongsideDirectOnes(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "src/caller.js")}, nil, DotOptions{
		MaxRender:       Unlimited,
		TransitiveEdges: []Edge{{From: n("GrandCaller", "src/gc.js"), To: n("Caller", "src/caller.js"), Depth: 2}},
	})
	wantMatch(t, `"Caller" -> "Target";`, dot)
	wantMatch(t, `"GrandCaller" -> "Caller" \[color="#b8c2d0"\];`, dot)
}

func TestBuildDotDashesTransitiveEdgesFromTestCallersSameAsDirectOnes(t *testing.T) {
	dot := BuildDot("Target", nil, nil, DotOptions{
		MaxRender:       Unlimited,
		TransitiveEdges: []Edge{{From: n("HelperTest", "src/helper.test.js"), To: n("Direct", "src/direct.js"), Depth: 2}},
	})
	wantMatch(t, `"HelperTest" -> "Direct" \[color="#b8c2d0", style=dashed, label="test"\];`, dot)
}

func TestBuildDotStylesAFileKindCallerCalleeDistinctlyFromARealCall(t *testing.T) {
	caller := Node{Name: "some.js", Kind: "file", FilePath: "src/some.js"}
	callee := Node{Name: "other.js", Kind: "file", FilePath: "src/other.js"}
	dot := BuildDot("Target", []Node{caller}, []Node{callee}, unlimitedDot)
	wantMatch(t, `"some\.js" -> "Target" \[style=dotted, color="#9ca3af", label="file"\];`, dot)
	wantMatch(t, `"Target" -> "other\.js" \[style=dotted, color="#9ca3af", label="file"\];`, dot)
}

func TestBuildDotFileKindStylingTakesPrecedenceOverTestDashStyling(t *testing.T) {
	dot := BuildDot("Target", []Node{{Name: "weird.test.js", Kind: "file", FilePath: "src/weird.test.js"}}, nil, unlimitedDot)
	wantMatch(t, `"weird\.test\.js" -> "Target" \[style=dotted, color="#9ca3af", label="file"\];`, dot)
	wantNoMatch(t, `label="test"`, dot)
}

func TestBuildDotWithNoTransitiveEdgesHasNoDepthColour(t *testing.T) {
	dot := BuildDot("Target", []Node{n("Caller", "a.js")}, []Node{n("Callee", "b.js")}, unlimitedDot)
	wantNoMatch(t, `color="#b8c2d0"`, dot)
}

func TestBuildDotOutputIsValidDotThatTheRealDotBinaryAccepts(t *testing.T) {
	requireBins(t, "dot")
	dot := BuildDot(`Weird "Name" \ <html>`, []Node{
		n("CallerTest", "src/caller.test.js"),
		n("Unicode λ Caller", "src/caller2.js"),
	}, []Node{
		n("Callee", "src/callee.js"),
	}, unlimitedDot)
	cmd := exec.Command("dot", "-Tpng")
	cmd.Stdin = strings.NewReader(dot)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("dot rejected BuildDot's output: %v\n%s", err, errb.String())
	}
	if out.Len() == 0 {
		t.Error("expected dot to produce non-empty PNG output")
	}
}

func TestIsBlankDotIsTrueOnlyForADotThatDrawsNoNodeAcrossEveryBuilder(t *testing.T) {
	if !IsBlankDot(BuildArchitectureDot(nil, ArchOptions{MaxRender: 50})) {
		t.Error("an empty architecture DOT must be blank")
	}
	if !IsBlankDot(BuildDiffDot(nil, nil, 50, false)) {
		t.Error("an empty diff DOT must be blank")
	}
	if IsBlankDot(BuildArchitectureDot([]FileEdge{{From: "a.js", To: "b.js", Weight: 1}}, ArchOptions{MaxRender: 50})) {
		t.Error("an architecture DOT with an edge must not be blank")
	}
	// symbol mode always draws the queried symbol, even with no callers/callees
	if IsBlankDot(BuildDot("foo", nil, nil, DotOptions{MaxRender: 50})) {
		t.Error("a symbol DOT always draws its root and must not be blank")
	}
}

func TestBuildArchitectureDotRendersAFileNodePerEndpointAndAWeightedEdge(t *testing.T) {
	dot := BuildArchitectureDot([]FileEdge{{From: "src/a.js", To: "src/b.js", Weight: 2}}, ArchOptions{MaxRender: Unlimited})
	wantMatch(t, `^digraph architecture \{`, dot)
	wantMatch(t, `"src/a\.js";`, dot)
	wantMatch(t, `"src/b\.js";`, dot)
	wantMatch(t, `"src/a\.js" -> "src/b\.js" \[label="2"\];`, dot)
}

func TestBuildArchitectureDotDashesFileNodesThatLookLikeTestFiles(t *testing.T) {
	dot := BuildArchitectureDot([]FileEdge{{From: "test/a.spec.js", To: "src/b.js", Weight: 1}}, ArchOptions{MaxRender: Unlimited})
	wantMatch(t, `"test/a\.spec\.js" \[style="rounded,filled,dashed"\];`, dot)
}

func TestBuildArchitectureDotRespectsMaxRenderByDroppingEdgesOutsideTheTopNFiles(t *testing.T) {
	edges := []FileEdge{
		{From: "busy.js", To: "a.js", Weight: 5},
		{From: "quiet.js", To: "c.js", Weight: 1},
	}
	dot := BuildArchitectureDot(edges, ArchOptions{MaxRender: 2})
	wantMatch(t, `"busy\.js" -> "a\.js"`, dot)
	wantNoMatch(t, `"quiet\.js" -> "c\.js"`, dot)
}

func TestBuildArchitectureDotStillDashesARolledUpTestDirectoryGroup(t *testing.T) {
	// IsTestRef reads path segments, so the group's trailing slash must not defeat it.
	dot := BuildArchitectureDot(RollupFileEdges([]FileEdge{{From: "test/run.js", To: "render/callgraph.js", Weight: 1}}, 1), ArchOptions{MaxRender: Unlimited})
	wantMatch(t, `"test/"\s*\[style="rounded,filled,dashed"\]`, dot)
}

func TestBuildArchitectureDotDrawsOnlyViolatingEdgesRedAndNothingChangesWithoutViolations(t *testing.T) {
	edges := []FileEdge{{From: "ui/page.js", To: "db/query.js", Weight: 2}, {From: "ui/page.js", To: "core/api.js", Weight: 1}}
	plain := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited})
	if got := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Violating: map[string]bool{}}); got != plain {
		t.Error("no violations must render byte-identically")
	}
	wantNoMatch(t, `#dc2626`, plain)
	red := BuildArchitectureDot(edges, ArchOptions{MaxRender: Unlimited, Violating: ViolatingDrawnKeys([]Violation{{From: "ui/page.js", To: "db/query.js"}}, 0)})
	wantMatch(t, `"ui/page\.js" -> "db/query\.js" \[label="2", color="#dc2626", fontcolor="#dc2626", penwidth=1\.8\];`, red)
	wantMatch(t, `"ui/page\.js" -> "core/api\.js" \[label="1"\];`, red)
}

func TestBuildDiffDotBoldsEveryRootAndDrawsCallerToRootRootToCalleeEdges(t *testing.T) {
	root := n("Root", "src/root.js")
	edges := []Edge{
		{From: n("caller", "a.js"), To: root},
		{From: root, To: n("callee", "b.js")},
	}
	dot := BuildDiffDot([]Node{root}, edges, Unlimited, false)
	wantMatch(t, `"Root" \[fillcolor="#e2e8f0".*fontname="Helvetica-Bold"`, dot)
	wantMatch(t, `"caller" -> "Root"`, dot)
	wantMatch(t, `"Root" -> "callee"`, dot)
}

func TestBuildDiffDotDashesAnEdgeWhoseActualSourceIsTestCode(t *testing.T) {
	root := n("Root", "test/root.spec.js")
	edges := []Edge{
		{From: n("callerTest", "test/caller.spec.js"), To: root},
		{From: root, To: n("callee", "b.js")},
	}
	dot := BuildDiffDot([]Node{root}, edges, Unlimited, false)
	wantMatch(t, `"callerTest" -> "Root" \[style=dashed, label="test"\];`, dot)
	// Root itself lives in test/root.spec.js, so a call it makes IS a test
	// calling production code — unlike BuildDot's single-root mode, this is
	// deliberately not suppressed here.
	wantMatch(t, `"Root" -> "callee" \[style=dashed, label="test"\];`, dot)
}

func TestBuildDiffDotDottedStylesAFileKindEndpointOnEitherSide(t *testing.T) {
	root := n("Root", "src/root.js")
	edges := []Edge{
		{From: Node{Name: "root.js", FilePath: "src/root.js", Kind: "file"}, To: root},
		{From: root, To: Node{Name: "unresolved.js", FilePath: "lib/unresolved.js", Kind: "file"}},
	}
	dot := BuildDiffDot([]Node{root}, edges, Unlimited, false)
	wantMatch(t, `"root\.js" -> "Root" \[style=dotted, color="#9ca3af", label="file"\];`, dot)
	wantMatch(t, `"Root" -> "unresolved\.js" \[style=dotted, color="#9ca3af", label="file"\];`, dot)
}

func TestBuildDiffDotAlwaysDrawsEveryRootEvenWhenMaxRenderCapsNonRootNodes(t *testing.T) {
	roots := []Node{n("R1", "a.js"), n("R2", "b.js")}
	edges := []Edge{
		{From: n("c1", "c1.js"), To: roots[0]},
		{From: n("c2", "c2.js"), To: roots[1]},
	}
	dot := BuildDiffDot(roots, edges, 1, false)
	wantMatch(t, `"R1"`, dot)
	wantMatch(t, `"R2"`, dot)
	// exactly one of the two callers survives the maxRender=1 cut, not both
	kept := 0
	for _, name := range []string{"c1", "c2"} {
		if strings.Contains(dot, `"`+name+`"`) {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("%d callers survived --max-render 1, want exactly 1", kept)
	}
}

func TestBuildDiffDotDedupesARootToRootEdgeDiscoveredViaBothProbes(t *testing.T) {
	r1, r2 := n("R1", "a.js"), n("R2", "b.js")
	// {R1 -> R2} is the exact same real call whether it is found as R2's caller
	// or R1's callee, so the from/to-only key collapses both however they were pushed.
	for _, edges := range [][]Edge{{{From: r1, To: r2}, {From: r1, To: r2}}, {{From: r1, To: r2}, {From: r1, To: r2}}} {
		dot := BuildDiffDot([]Node{r1, r2}, edges, Unlimited, false)
		if got := countMatches(`"R1" -> "R2"`, dot); got != 1 {
			t.Errorf(`"R1" -> "R2" appears %d times, want 1`, got)
		}
	}
}

func TestBuildDiffDotStylesARootToRootEdgeTheSameWayRegardlessOfPushOrder(t *testing.T) {
	// R1 lives in a test file — a call it makes should read as dashed "test"
	// however the edge was assembled.
	r1, r2 := n("R1", "test/r1.spec.js"), n("R2", "b.js")
	edges := []Edge{{From: r1, To: r2}}
	dotA := BuildDiffDot([]Node{r1, r2}, edges, Unlimited, false)
	dotB := BuildDiffDot([]Node{r2, r1}, edges, Unlimited, false) // roots array order flipped
	for _, dot := range []string{dotA, dotB} {
		wantMatch(t, `"R1" -> "R2" \[style=dashed, label="test"\];`, dot)
	}
}
