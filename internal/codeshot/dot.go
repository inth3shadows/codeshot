package codeshot

import (
	"fmt"
	"regexp"
	"strings"
)

// House style: calm and documentation-grade. Straight edges, muted slate, and
// a solid white background so committed images read in GitHub dark mode too.
const (
	graphAttrs = `  rankdir=LR; bgcolor="white"; splines=polyline; nodesep=0.35; ranksep=0.75; pad=0.2;`
	nodeAttrs  = `  node [shape=box, style="rounded,filled", fillcolor="#f8fafc", color="#cbd5e1", fontcolor="#334155", fontname="Helvetica", fontsize=11, penwidth=1.1, margin="0.20,0.11"];`
	edgeAttrs  = `  edge [color="#94a3b8", arrowsize=0.6, penwidth=1.0];`
	rootStyle  = `fillcolor="#e2e8f0", color="#94a3b8", fontcolor="#0f172a", fontname="Helvetica-Bold", penwidth=1.5`
)

func styleSuffix(attrs []string) string {
	if len(attrs) == 0 {
		return ""
	}
	return " [" + strings.Join(attrs, ", ") + "]"
}

// nodeDecl returns the extra attributes a drawn node needs: a disambiguating
// label for a colliding name and/or a file-path tooltip (svg only).
func nodeDecl(ids identities, n Node, tooltips bool) []string {
	var attrs []string
	if name, base, ok := ids.labelOf(n); ok {
		attrs = append(attrs, fmt.Sprintf(`label="%s\n(%s)"`, esc(name), esc(base)))
	}
	if tooltips && n.FilePath != "" {
		attrs = append(attrs, fmt.Sprintf(`tooltip="%s"`, esc(n.FilePath)))
	}
	return attrs
}

// DotOptions are the view options shared by the symbol and diff builders.
type DotOptions struct {
	MaxRender       int // Unlimited when unset
	TransitiveEdges []Edge
	Tooltips        bool
}

// BuildDot draws one symbol's callers and callees, plus transitive edges at
// --depth > 1. --max-render is one budget spent callers, callees, transitive.
func BuildDot(symbol string, callers, callees []Node, opt DotOptions) string {
	dCallers := DedupeNodes(callers)
	dCallees := DedupeNodes(callees)
	dTrans := DedupeEdges(opt.TransitiveEdges)
	b := AllocateRenderBudget(opt.MaxRender, []int{len(dCallers), len(dCallees), len(dTrans)})
	drawnCallers, drawnCallees, drawnTrans := dCallers[:b[0]], dCallees[:b[1]], dTrans[:b[2]]

	// The root keeps a stable id but joins the collision set, so a same-named
	// caller is pushed onto a distinct id instead of merging into the root.
	all := []Node{{Name: symbol, FilePath: rootFile}}
	all = append(all, drawnCallers...)
	all = append(all, drawnCallees...)
	for _, e := range drawnTrans {
		all = append(all, e.From, e.To)
	}
	ids := newIdentities(all)
	rootID := esc(symbol)

	lines := []string{"digraph callgraph {", graphAttrs, nodeAttrs, edgeAttrs,
		fmt.Sprintf(`  "%s" [%s];`, rootID, rootStyle)}

	declared := map[string]bool{rootID: true}
	for _, n := range all[1:] {
		id := esc(ids.idOf(n))
		if declared[id] {
			continue
		}
		declared[id] = true
		if attrs := nodeDecl(ids, n, opt.Tooltips); len(attrs) > 0 {
			lines = append(lines, fmt.Sprintf(`  "%s" [%s];`, id, strings.Join(attrs, ", ")))
		}
	}
	for _, c := range drawnCallers {
		lines = append(lines, fmt.Sprintf(`  "%s" -> "%s"%s;`, esc(ids.idOf(c)), rootID, styleSuffix(edgeStyleAttrs(c, ""))))
	}
	for _, c := range drawnCallees {
		// Callees never get the dashed test style; a file-kind callee still
		// gets the "not a real call" look.
		var attrs []string
		if c.Kind == "file" {
			attrs = edgeStyleAttrs(c, "")
		}
		lines = append(lines, fmt.Sprintf(`  "%s" -> "%s"%s;`, rootID, esc(ids.idOf(c)), styleSuffix(attrs)))
	}
	for _, e := range drawnTrans {
		attrs := edgeStyleAttrs(e.From, DepthColor(e.Depth))
		lines = append(lines, fmt.Sprintf(`  "%s" -> "%s" [%s];`, esc(ids.idOf(e.From)), esc(ids.idOf(e.To)), strings.Join(attrs, ", ")))
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n")
}

// FileEdge is a weighted edge between two files (or groups).
type FileEdge struct {
	From, To string
	Weight   int
}

// ArchOptions are BuildArchitectureDot's view options.
type ArchOptions struct {
	MaxRender int             // Unlimited when unset
	Violating map[string]bool // drawnEdgeKey set of --forbid violations
}

// BuildArchitectureDot draws the file-level dependency graph, keeping only the
// top --max-render files by edge weight, with --forbid violations in red.
func BuildArchitectureDot(fileEdges []FileEdge, opt ArchOptions) string {
	keep := TopFilesByWeight(fileEdges, opt.MaxRender)
	kept := fileEdges
	if keep != nil {
		kept = nil
		for _, e := range fileEdges {
			if keep[e.From] && keep[e.To] {
				kept = append(kept, e)
			}
		}
	}
	var files []string
	seen := map[string]bool{}
	for _, e := range kept {
		for _, f := range []string{e.From, e.To} {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	lines := []string{"digraph architecture {", graphAttrs, nodeAttrs, edgeAttrs}
	for _, f := range files {
		attrs := ""
		if IsTestRef("", f) {
			attrs = ` [style="rounded,filled,dashed"]`
		}
		lines = append(lines, fmt.Sprintf(`  "%s"%s;`, esc(f), attrs))
	}
	for _, e := range kept {
		alarm := ""
		if opt.Violating[drawnEdgeKey(e.From, e.To)] {
			alarm = `, color="#dc2626", fontcolor="#dc2626", penwidth=1.8`
		}
		lines = append(lines, fmt.Sprintf(`  "%s" -> "%s" [label="%d"%s];`, esc(e.From), esc(e.To), e.Weight, alarm))
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n")
}

// BuildDiffDot draws every root (a symbol in a changed file) bold, with its
// direct callers/callees. Roots are always drawn; --max-render bounds only
// the non-root context around them.
func BuildDiffDot(roots []Node, edges []Edge, maxRender int, tooltips bool) string {
	rootKeys := map[string]bool{}
	for _, r := range roots {
		rootKeys[NodeKey(r)] = true
	}
	dEdges := DedupeEdges(edges)
	pool := append([]Node{}, roots...)
	for _, e := range dEdges {
		pool = append(pool, e.From, e.To)
	}
	allNodes := DedupeNodes(pool)
	var keepNonRoot map[string]bool
	if maxRender != Unlimited {
		keepNonRoot = map[string]bool{}
		n := 0
		for _, x := range allNodes {
			if rootKeys[NodeKey(x)] {
				continue
			}
			if n < maxRender {
				keepNonRoot[NodeKey(x)] = true
			}
			n++
		}
	}
	keep := func(k string) bool { return rootKeys[k] || keepNonRoot == nil || keepNonRoot[k] }

	var drawnNodes []Node
	for _, n := range allNodes {
		if keep(NodeKey(n)) {
			drawnNodes = append(drawnNodes, n)
		}
	}
	var drawnEdges []Edge
	for _, e := range dEdges {
		if keep(NodeKey(e.From)) && keep(NodeKey(e.To)) {
			drawnEdges = append(drawnEdges, e)
		}
	}
	ids := newIdentities(drawnNodes)

	lines := []string{"digraph callgraph {", graphAttrs, nodeAttrs, edgeAttrs}
	for _, n := range drawnNodes {
		attrs := nodeDecl(ids, n, tooltips)
		if rootKeys[NodeKey(n)] {
			attrs = append(attrs, `fillcolor="#e2e8f0"`, `color="#94a3b8"`, `fontcolor="#0f172a"`, `fontname="Helvetica-Bold"`, "penwidth=1.5")
		}
		lines = append(lines, fmt.Sprintf(`  "%s"%s;`, esc(ids.idOf(n)), styleSuffix(attrs)))
	}
	for _, e := range drawnEdges {
		// Styled off the edge's endpoints only, never off which probe found it,
		// so a root-to-root edge looks the same whatever the probe order.
		var attrs []string
		if e.To.Kind == "file" && e.From.Kind != "file" {
			attrs = edgeStyleAttrs(e.To, "")
		} else {
			attrs = edgeStyleAttrs(e.From, "")
		}
		lines = append(lines, fmt.Sprintf(`  "%s" -> "%s"%s;`, esc(ids.idOf(e.From)), esc(ids.idOf(e.To)), styleSuffix(attrs)))
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n")
}

var blankDotRe = regexp.MustCompile(`(?m)^\s*"`)

// IsBlankDot reports a DOT that declares no node: every builder starts a node
// statement with its quoted id.
func IsBlankDot(dot string) bool { return !blankDotRe.MatchString(dot) }
