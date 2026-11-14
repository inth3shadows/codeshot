package codeshot

import (
	"testing"
)

func TestDepthBudgetWarningFiresOnlyWhenTraversalWasTruncated(t *testing.T) {
	if got := DepthBudgetWarning(false, 200); got != "" {
		t.Errorf("not truncated: got %q, want none", got)
	}
	wantMatch(t, `safety cap of 200 discovered nodes`, DepthBudgetWarning(true, 200))
}

func TestFormatMismatchWarning(t *testing.T) {
	wantMatch(t, `--out ends in '\.svg' but --format is 'png'`, FormatMismatchWarning("diagram.svg", "png"))
	tests := []struct {
		name, out, format string
	}{
		{"matching extension and format should not warn", "diagram.svg", "svg"},
		{"an unrecognized extension should not false-positive", "diagram.dot.bak", "png"},
		{"no --out (auto-generated path) can never mismatch", "", "png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatMismatchWarning(tt.out, tt.format); got != "" {
				t.Errorf("FormatMismatchWarning(%q, %q) = %q, want none", tt.out, tt.format, got)
			}
		})
	}
}

func TestRenderTruncationNoteFiresWhenDistinctCountExceedsMaxRender(t *testing.T) {
	wantMatch(t, `rendering 5 of 10 distinct callers`, RenderTruncationNote("callers", 10, 5))
}

func TestRenderTruncationNoteIsEmptyWhenMaxRenderIsUnsetOrNotExceeded(t *testing.T) {
	for _, tt := range []struct{ distinct, maxRender int }{{10, Unlimited}, {5, 5}, {3, 5}} {
		if got := RenderTruncationNote("callers", tt.distinct, tt.maxRender); got != "" {
			t.Errorf("RenderTruncationNote(callers, %d, %d) = %q, want none", tt.distinct, tt.maxRender, got)
		}
	}
}

func manyNodes(count int) []Node {
	out := make([]Node, count)
	for i := range out {
		out[i] = Node{Name: "x"}
	}
	return out
}

func TestTruncationWarningFiresWhenResultsHitTheLimit(t *testing.T) {
	results := manyNodes(20)
	got := TruncationWarning("callers", results, 20, ReadTruncation(nil, nil, results, 20))
	wantMatch(t, `showing 20 callers`, got)
}

func TestTruncationWarningPrefersCodegraphsReportedTruncatedTotalOverTheCountHeuristic(t *testing.T) {
	results := manyNodes(20)
	// Authoritative yes: says how many were cut and what --limit would show them.
	cut := TruncationWarning("callers", results, 20, ReadTruncation(boolp(true), f64p(57), results, 20))
	wantMatch(t, `showing 20 of 57 callers`, cut)
	wantMatch(t, `cut off 37`, cut)
	wantMatch(t, `--limit 57`, cut)
	// Authoritative no: exactly --limit results that ARE all of them. The
	// heuristic warns here; the reported field is what stops the false warning.
	if got := TruncationWarning("callers", results, 20, ReadTruncation(boolp(false), f64p(20), results, 20)); got != "" {
		t.Errorf("truncated:false still warned: %q", got)
	}
	// A version that reports neither field falls back to the heuristic.
	wantMatch(t, `may have cut off more`, TruncationWarning("callers", results, 20, ReadTruncation(nil, nil, results, 20)))
}

func TestReadTruncationReadsTruncatedByPresenceSoFalseIsNotMistakenForAbsent(t *testing.T) {
	results := manyNodes(5)
	tests := []struct {
		name      string
		truncated *bool
		total     *float64
		results   []Node
		want      Truncation
	}{
		{"false with total", boolp(false), f64p(5), results, Truncation{Truncated: false, Total: 5}},
		{"true with total", boolp(true), f64p(9), results, Truncation{Truncated: true, Total: 9}},
		// total missing -> unknown, but `truncated` still stands.
		{"true without total", boolp(true), nil, results, Truncation{Truncated: true, Total: -1}},
		// No fields at all -> heuristic.
		{"no fields, count hits the limit", nil, nil, results, Truncation{Truncated: true, Total: -1}},
		{"no fields, count under the limit", nil, nil, results[:2], Truncation{Truncated: false, Total: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadTruncation(tt.truncated, tt.total, tt.results, 5); got != tt.want {
				t.Errorf("ReadTruncation = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTruncationWarningIsEmptyWhenUnderTheLimit(t *testing.T) {
	results := manyNodes(3)
	if got := TruncationWarning("callers", results, 20, ReadTruncation(nil, nil, results, 20)); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

func TestEmptyGraphWarningFiresOnlyWhenASymbolHasNeitherCallersNorCallees(t *testing.T) {
	wantMatch(t, `'Foo' has no callers or callees`, EmptyGraphWarning("Foo", 0, 0))
	if got := EmptyGraphWarning("Foo", 1, 0); got != "" {
		t.Errorf("one caller: got %q, want none", got)
	}
	if got := EmptyGraphWarning("Foo", 0, 1); got != "" {
		t.Errorf("one callee: got %q, want none", got)
	}
}

// Ported from "blankEmbedRefusal refuses a blank --embed and a blank --check, and nothing else (#27)".
func TestBlankEmbedRefusalRefusesABlankEmbedAndABlankCheckAndNothingElse(t *testing.T) {
	blank := BuildArchitectureDot(nil, ArchOptions{MaxRender: 50})
	real := BuildArchitectureDot([]FileEdge{{From: "a.js", To: "b.js", Weight: 1}}, ArchOptions{MaxRender: 50})
	wantMatch(t, `(?i)refusing to overwrite the existing diagram embedded in 'docs\.md'`, BlankEmbedRefusal(blank, "docs.md", false))
	// --check must not send the user to the --embed invocation that is refused
	wantMatch(t, `would be refused too`, BlankEmbedRefusal(blank, "docs.md", true))
	if got := BlankEmbedRefusal(blank, "", false); got != "" {
		t.Errorf("a plain render may be blank; got %q", got)
	}
	if got := BlankEmbedRefusal(real, "docs.md", false); got != "" {
		t.Errorf("real --embed: got %q, want none", got)
	}
	if got := BlankEmbedRefusal(real, "docs.md", true); got != "" {
		t.Errorf("real --check: got %q, want none", got)
	}
}

// The JS emptyArchitectureWarning(edges) also decided *whether* to fire; in Go
// that condition lives in RunArchitectureMode, so only the text is unit-testable.
func TestEmptyArchitectureWarningText(t *testing.T) {
	wantMatch(t, `no cross-file call edges — the diagram is blank`, EmptyArchitectureWarning)
}

// The JS symbolBudgetWarning(truncated, budget) also decided whether to fire;
// in Go RunArchitectureMode makes that decision, so only the text is testable.
func TestSymbolBudgetWarningNamesTheBudgetAndTheCutAsAPathSortedPrefixNotASample(t *testing.T) {
	w := SymbolBudgetWarning(500)
	wantMatch(t, `stopped enumerating after 500 symbols`, w)
	// Otherwise "the graph is incomplete" reads as "a few edges missing", hiding
	// that unprobed late-path files render as sinks that appear to call nothing.
	wantMatch(t, `path-sorted prefix, not a sample`, w)
	wantMatch(t, `may appear to call nothing when they do`, w)
}

func TestGroupCollapseWarningFiresOnlyWhenTheRollupAteEveryEdge(t *testing.T) {
	wantMatch(t, `--group-depth 1 left no edges to draw — all 7 cross-file edge\(s\)`, GroupCollapseWarning(7, 0, 1, 1))
	tests := []struct {
		name                         string
		before, after, depth, groups int
	}{
		{"edges survived — not a collapse", 7, 3, 1, 2},
		{"nothing to collapse — EmptyArchitectureWarning owns this case", 0, 0, 1, 0},
		{"no --group-depth — not the flag's doing", 7, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GroupCollapseWarning(tt.before, tt.after, tt.depth, tt.groups); got != "" {
				t.Errorf("got %q, want none", got)
			}
		})
	}
}

func TestGroupCollapseWarningGivesOppositeAdviceForOneGroupVsSeveral(t *testing.T) {
	// One group: the depth is too coarse, going deeper helps.
	wantMatch(t, `(?s)every file falls into a single group.*Try a deeper --group-depth`, GroupCollapseWarning(3, 0, 1, 1))
	// Several groups: the modules genuinely don't call each other, so a deeper
	// --group-depth stays blank.
	many := GroupCollapseWarning(3, 0, 1, 2)
	wantMatch(t, `the 2 groups at this depth have no calls between them`, many)
	wantMatch(t, `a deeper --group-depth will stay blank`, many)
	wantNoMatch(t, `within one directory`, many) // must not assert a single directory when there are several
}

func TestHiddenViolationNoteCountsViolatingEdgesMaxRenderLeavesOutAndIsSilentOtherwise(t *testing.T) {
	edges := []FileEdge{
		{From: "a.js", To: "b.js", Weight: 9},
		{From: "c.js", To: "d.js", Weight: 1},
	}
	violating := ViolatingDrawnKeys([]Violation{{From: "c.js", To: "d.js"}}, 0)
	wantMatch(t, `^codeshot: 1 edge\(s\) that break a --forbid rule fall outside --max-render 2`, HiddenViolationNote(edges, violating, 2))
	if got := HiddenViolationNote(edges, violating, 4); got != "" {
		t.Errorf("all four files fit: got %q, want none", got)
	}
	if got := HiddenViolationNote(edges, violating, Unlimited); got != "" {
		t.Errorf("no cap: got %q, want none", got)
	}
	if got := HiddenViolationNote(edges, map[string]bool{}, 2); got != "" {
		t.Errorf("no violations: got %q, want none", got)
	}
}

func TestForbidViolationReportListsEachViolatingEdgeWithItsWeightAndRuleAndIsSilentWithNone(t *testing.T) {
	if got := ForbidViolationReport(nil); got != "" {
		t.Errorf("no violations: got %q, want none", got)
	}
	got := ForbidViolationReport([]Violation{{Rule: "a/->b/", From: "a/x.js", To: "b/y.js", Weight: 3}})
	want := "codeshot: 1 file edge(s) break a --forbid rule:\n  a/x.js -> b/y.js (weight 3) breaks --forbid 'a/->b/'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDiffNoChangesWarningNamesTheRefWhenGivenAndMatchesHEADWhenNot(t *testing.T) {
	wantMatch(t, `working tree matches HEAD`, DiffNoChangesWarning(""))
	wantMatch(t, `'origin/main\.\.\.HEAD'`, DiffNoChangesWarning("origin/main...HEAD"))
}

func TestDiffNoSymbolsWarningReportsTheChangedFileCountAndACodegraphSyncHint(t *testing.T) {
	msg := DiffNoSymbolsWarning("/repo", 3)
	wantMatch(t, `3 changed file`, msg)
	wantMatch(t, `codegraph sync /repo`, msg)
}

func TestDiffSymbolBudgetWarningIsEmptyUnderBudgetAndFiresNamingTheCutOverBudget(t *testing.T) {
	if got := DiffSymbolBudgetWarning(3, 5); got != "" {
		t.Errorf("under budget: got %q, want none", got)
	}
	wantMatch(t, `matched 7 changed symbols but only probing the first 5`, DiffSymbolBudgetWarning(7, 5))
}

func TestDiffEmbedRefusalNamesTheEmbedTargetAndTheRefOrHEADItFoundNothingFor(t *testing.T) {
	wantMatch(t, `working tree matches HEAD`, DiffEmbedRefusal("", "docs.md"))
	wantMatch(t, `'docs\.md'`, DiffEmbedRefusal("", "docs.md"))
	wantMatch(t, `'origin/main\.\.\.HEAD'`, DiffEmbedRefusal("origin/main...HEAD", "docs.md"))
}

func TestDiffEmptyRootsWarningIsEmptyWhenEveryRootHasAnEdgeAndFiresWithTheAggregateCountOtherwise(t *testing.T) {
	if got := DiffEmptyRootsWarning(0, 5); got != "" {
		t.Errorf("got %q, want none", got)
	}
	wantMatch(t, `2 of 5 changed symbol\(s\) have no callers or callees`, DiffEmptyRootsWarning(2, 5))
}

func TestDiffEmbedRefusalNoSymbolsReportsTheCountTheTargetAndASyncHintButNotTheNoChangedFilesWording(t *testing.T) {
	msg := DiffEmbedRefusalNoSymbols("/repo", 3, "docs.md")
	wantMatch(t, `3 changed file\(s\) but no matching symbols`, msg)
	wantMatch(t, `refusing to overwrite the existing diagram embedded in 'docs\.md'`, msg)
	wantMatch(t, `codegraph sync /repo`, msg)
	wantNoMatch(t, `found no changed files`, msg)
}

func TestDiffDuplicateNameWarningNamesOnlyTheRootsThatFellBackDedupedAndIsEmptyWhenNoneDid(t *testing.T) {
	msg := DiffDuplicateNameWarning([]string{"parse", "parse", "run"})
	wantMatch(t, `2 changed symbol name\(s\).*parse, run`, msg)
	wantMatch(t, `1\.6\.0`, msg)
	wantNoMatch(t, `re-probes`, msg)
	if got := DiffDuplicateNameWarning(nil); got != "" {
		t.Errorf("none fell back: got %q, want none", got)
	}
}

func TestDiffTruncationWarningIsEmptyWithNoTruncatedRootsAndFiresWithTheAggregateCountAndExamples(t *testing.T) {
	if got := DiffTruncationWarning(nil, 50); got != "" {
		t.Errorf("got %q, want none", got)
	}
	msg := DiffTruncationWarning([]string{"foo", "bar"}, 50)
	wantMatch(t, `2 changed symbol\(s\)`, msg)
	wantMatch(t, `--limit \(50\)`, msg)
	wantMatch(t, `foo, bar`, msg)
}

func TestDiffNothingToCheckNamesTheEmbedTargetAndNeverClaimsRefusingToOverwrite(t *testing.T) {
	a := DiffNothingToCheck("", "docs.md")
	wantMatch(t, `nothing to diagram, so nothing to check`, a)
	wantMatch(t, `'docs\.md'`, a)
	wantNoMatch(t, `refusing to overwrite`, a)

	b := DiffNothingToCheckNoSymbols("/repo", 3, "docs.md")
	wantMatch(t, `3 changed file\(s\) but no matching symbols`, b)
	wantMatch(t, `nothing to diagram, so nothing to check`, b)
	wantNoMatch(t, `refusing to overwrite`, b)
}
