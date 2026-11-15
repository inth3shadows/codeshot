package codeshot

import (
	"io"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// collectTransitive walks hops 2..maxDepth in one direction, sharing the
// discovered set (and its node budget) across both directions. One node's
// exec failure is skipped; a fatal codegraph answer still ends the run.
func collectTransitive(src CallSource, direction string, limit, maxDepth int, seeds []Node, discovered map[string]bool, budget int) ([]Edge, bool, error) {
	var edges []Edge
	frontier := seeds
	truncated := len(discovered) >= budget
	for hop := 2; hop <= maxDepth && len(frontier) > 0 && !truncated; hop++ {
		var next []Node
		for _, node := range frontier {
			r, err := src.Edges(direction, node.Name, limit, true)
			if err != nil {
				if codegraph.IsFatal(err) {
					return nil, false, err
				}
				continue
			}
			for _, x := range DedupeNodes(SortSymbolsForEnumeration(r.List(direction))) {
				if direction == "callers" {
					edges = append(edges, Edge{From: x, To: node, Depth: hop})
				} else {
					edges = append(edges, Edge{From: node, To: x, Depth: hop})
				}
				k := NodeKey(x)
				if discovered[k] {
					continue
				}
				if len(discovered) >= budget {
					truncated = true
					continue
				}
				discovered[k] = true
				next = append(next, x)
			}
		}
		frontier = next
	}
	return edges, truncated, nil
}

// SymbolParams are symbol mode's inputs.
type SymbolParams struct {
	Limit, MaxRender, Depth, MaxDepthNodes int
	Tooltips                               bool
}

// RunSymbolMode draws one symbol's caller/callee trail. Returns the DOT and
// the resolved symbol name.
func RunSymbolMode(src CallSource, symbol string, p SymbolParams, stderr io.Writer) (string, string, error) {
	resolved, err := src.Resolve(symbol)
	if err != nil {
		return "", "", err
	}
	w := src.Wording()
	callersR, err := src.Edges("callers", resolved, p.Limit, true)
	if err != nil {
		return "", "", err
	}
	calleesR, err := src.Edges("callees", resolved, p.Limit, true)
	if err != nil {
		return "", "", err
	}
	// Sorted, so the diagram never depends on the order a source lists
	// neighbours in (codegraph's SQL has no ORDER BY; a snapshot keeps its
	// file's order).
	callers, callees := SortSymbolsForEnumeration(callersR.List("callers")), SortSymbolsForEnumeration(calleesR.List("callees"))
	warn(stderr, truncationWarning(w, "callers", callers, p.Limit, ReadTruncation(callersR.Truncated, callersR.Total, callers, p.Limit)))
	warn(stderr, truncationWarning(w, "callees", callees, p.Limit, ReadTruncation(calleesR.Truncated, calleesR.Total, callees, p.Limit)))
	warn(stderr, emptyGraphWarning(w, resolved, len(callers), len(callees)))

	var trans []Edge
	if p.Depth > 1 {
		discovered := map[string]bool{}
		for _, n := range DedupeNodes(append(append([]Node{}, callers...), callees...)) {
			discovered[NodeKey(n)] = true
		}
		ce, ct, err := collectTransitive(src, "callers", p.Limit, p.Depth, DedupeNodes(callers), discovered, p.MaxDepthNodes)
		if err != nil {
			return "", "", err
		}
		ee, et, err := collectTransitive(src, "callees", p.Limit, p.Depth, DedupeNodes(callees), discovered, p.MaxDepthNodes)
		if err != nil {
			return "", "", err
		}
		trans = append(ce, ee...)
		warn(stderr, DepthBudgetWarning(ct || et, p.MaxDepthNodes))
	}

	// Same shared budget BuildDot uses, so the notes report what is drawn.
	nCallers, nCallees, nTrans := len(DedupeNodes(callers)), len(DedupeNodes(callees)), len(DedupeEdges(trans))
	b := AllocateRenderBudget(p.MaxRender, []int{nCallers, nCallees, nTrans})
	warn(stderr, RenderTruncationNote("callers", nCallers, b[0]))
	warn(stderr, RenderTruncationNote("callees", nCallees, b[1]))
	if len(trans) > 0 {
		warn(stderr, RenderTruncationNote("transitive edges", nTrans, b[2]))
	}
	dot := BuildDot(resolved, callers, callees, DotOptions{MaxRender: p.MaxRender, TransitiveEdges: trans, Tooltips: p.Tooltips})
	return dot, resolved, nil
}
