package codeshot

import (
	"slices"
	"testing"
)

func TestIsTestRef(t *testing.T) {
	tests := []struct {
		name      string
		sym, file string
		want      bool
	}{
		{"matches on name", "shouldDoThingTest", "src/thing.js", true},
		{"matches on filePath", "doThing", "test/thing.spec.js", true},
		{"false for production code", "doThing", "src/thing.js", false},
		// does not misfire on "test"/"contest" as a substring
		{"substring: AttestationService", "AttestationService", "src/attestation.js", false},
		{"substring: ContestWinner", "ContestWinner", "src/contest.js", false},
		{"substring: Attest", "Attest", "src/attest.js", false},
		{"recognizes a spec/ directory layout", "Thing", "spec/thing.js", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTestRef(tt.sym, tt.file); got != tt.want {
				t.Errorf("IsTestRef(%q, %q) = %v, want %v", tt.sym, tt.file, got, tt.want)
			}
		})
	}
}

func TestIdentitiesLeavesSingleFileNameAsIDWithNoLabel(t *testing.T) {
	ids := newIdentities([]Node{n("solo", "a.js")})
	if got := ids.idOf(n("solo", "a.js")); got != "solo" {
		t.Errorf("idOf = %q, want \"solo\"", got)
	}
	if name, base, ok := ids.labelOf(n("solo", "a.js")); ok {
		t.Errorf("labelOf = (%q, %q, true), want no label", name, base)
	}
}

// Root "handle" (no file) + a caller "handle" from a real file must not share
// an id, or the caller draws as a self-loop on the root box. (JS passed a null
// filePath for the root; Go marks it with the rootFile sentinel.)
func TestIdentitiesDisambiguatesCallerSharingTheRootsName(t *testing.T) {
	ids := newIdentities([]Node{n("handle", rootFile), n("handle", "b.js")})
	if got := ids.idOf(n("handle", "b.js")); got != "handle@@b.js" {
		t.Errorf("idOf = %q, want \"handle@@b.js\"", got)
	}
}

func TestDedupeEdgesCollapsesSameFromToNameAndFilePath(t *testing.T) {
	a, b := n("A", "a.js"), n("B", "b.js")
	edges := DedupeEdges([]Edge{{From: a, To: b, Depth: 2}, {From: a, To: b, Depth: 2}})
	if len(edges) != 1 {
		t.Errorf("len = %d, want 1", len(edges))
	}
}

func TestDedupeEdgesKeepsEdgesThatShareANameButDifferInFilePath(t *testing.T) {
	edges := DedupeEdges([]Edge{
		{From: n("A", "a.js"), To: n("B", "b.js"), Depth: 2},
		{From: n("A", "other.js"), To: n("B", "b.js"), Depth: 2},
	})
	if len(edges) != 2 {
		t.Errorf("len = %d, want 2", len(edges))
	}
}

func TestDepthColorFadesLighterAndClampsAtTheLastShade(t *testing.T) {
	two, three, deep := DepthColor(2), DepthColor(3), DepthColor(50)
	if two == three {
		t.Errorf("DepthColor(2) == DepthColor(3) == %q, want distinct shades", two)
	}
	if deep != DepthColor(4) {
		t.Errorf("DepthColor(50) = %q, want it clamped to the palette's last shade %q", deep, DepthColor(4))
	}
}

func TestAllocateRenderBudgetSpendsASharedAllowanceInPriorityOrder(t *testing.T) {
	tests := []struct {
		name      string
		maxRender int
		want      []int
	}{
		{"spent in priority order", 5, []int{3, 2, 0}},
		{"a budget larger than the total should not truncate anything", 20, []int{3, 4, 2}},
		{"no maxRender means no cap at all", Unlimited, []int{3, 4, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AllocateRenderBudget(tt.maxRender, []int{3, 4, 2}); !slices.Equal(got, tt.want) {
				t.Errorf("AllocateRenderBudget(%d, [3 4 2]) = %v, want %v", tt.maxRender, got, tt.want)
			}
		})
	}
}

func TestNodeKeyPairsNameAndFilePath(t *testing.T) {
	if got := NodeKey(n("foo", "a.js")); got != "foo a.js" {
		t.Errorf("NodeKey = %q, want \"foo a.js\"", got)
	}
	if NodeKey(n("foo", "a.js")) == NodeKey(n("foo", "b.js")) {
		t.Error("distinct symbols must not collide by name alone")
	}
}
