package codeshot

import (
	"fmt"
	"io"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// resolveSymbol maps a possibly partial query to codegraph's canonical name:
// callers/callees fuzzy-match but echo the raw query back.
func resolveSymbol(cg *codegraph.Client, query, repoPath string) (string, error) {
	nodes, err := cg.Query(repoPath, 1, query)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: symbol '%s' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.", query)}
	}
	return nodes[0].Name, nil
}

// collectTransitive walks hops 2..maxDepth in one direction, sharing the
// discovered set (and its node budget) across both directions. One node's
// exec failure is skipped; a fatal codegraph answer still ends the run.
func collectTransitive(cg *codegraph.Client, direction, repoPath string, limit, maxDepth int, seeds []Node, discovered map[string]bool, budget int) ([]Edge, bool, error) {
	var edges []Edge
	frontier := seeds
	truncated := len(discovered) >= budget
	for hop := 2; hop <= maxDepth && len(frontier) > 0 && !truncated; hop++ {
		var next []Node
		for _, node := range frontier {
			r, err := cg.Edges(direction, repoPath, limit, node.Name, true)
			if err != nil {
				if codegraph.IsFatal(err) {
					return nil, false, err
				}
				continue
			}
			for _, x := range DedupeNodes(r.List(direction)) {
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
func RunSymbolMode(cg *codegraph.Client, repoPath, symbol string, p SymbolParams, stderr io.Writer) (string, string, error) {
	resolved, err := resolveSymbol(cg, symbol, repoPath)
	if err != nil {
		return "", "", err
	}
	callersR, err := cg.Edges("callers", repoPath, p.Limit, resolved, true)
	if err != nil {
		return "", "", err
	}
	calleesR, err := cg.Edges("callees", repoPath, p.Limit, resolved, true)
	if err != nil {
		return "", "", err
	}
	callers, callees := callersR.List("callers"), calleesR.List("callees")
	warn(stderr, TruncationWarning("callers", callers, p.Limit, ReadTruncation(callersR.Truncated, callersR.Total, callers, p.Limit)))
	warn(stderr, TruncationWarning("callees", callees, p.Limit, ReadTruncation(calleesR.Truncated, calleesR.Total, callees, p.Limit)))
	warn(stderr, EmptyGraphWarning(resolved, len(callers), len(callees)))

	var trans []Edge
	if p.Depth > 1 {
		discovered := map[string]bool{}
		for _, n := range DedupeNodes(append(append([]Node{}, callers...), callees...)) {
			discovered[NodeKey(n)] = true
		}
		ce, ct, err := collectTransitive(cg, "callers", repoPath, p.Limit, p.Depth, DedupeNodes(callers), discovered, p.MaxDepthNodes)
		if err != nil {
			return "", "", err
		}
		ee, et, err := collectTransitive(cg, "callees", repoPath, p.Limit, p.Depth, DedupeNodes(callees), discovered, p.MaxDepthNodes)
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
