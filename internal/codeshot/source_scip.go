package codeshot

import (
	"fmt"
	"path"
	"strings"

	"github.com/inth3shadows/codeshot/internal/scipindex"
)

// ScipSource feeds --architecture from a SCIP index. A file depends on
// another when it refers to a symbol defined there. SCIP indexers such as
// scip-go write no enclosing ranges, so a reference can't be pinned to the
// function making it: edges are file to file, one per referenced symbol.
type ScipSource struct{ Index *scipindex.Index }

// Symbols is one file node per document. The edges are per file, so the
// file is the unit --max-symbols counts: a budget spent on struct fields and
// package clauses that never draw an edge (F12) would cut real files, and a
// file that defines nothing (a script of top-level calls) still draws.
func (s ScipSource) Symbols() ([]Node, error) {
	var out []Node
	for _, d := range s.Index.Docs {
		fileID := "\x00file:" + d.Path
		out = append(out, Node{Name: path.Base(d.Path), Kind: "file", FilePath: d.Path, QualifiedName: &fileID})
	}
	return SortSymbolsForEnumeration(out), nil
}

// FileEdges draws the edges of every file holding a chosen symbol.
func (s ScipSource) FileEdges(symbols []Node) ([]SymbolEdge, error) {
	from := map[string]bool{}
	for _, n := range symbols {
		from[n.FilePath] = true
	}
	var out []SymbolEdge
	for _, e := range s.Index.FileEdges(from) {
		out = append(out, SymbolEdge{FromFile: e.From, ToFile: e.To})
	}
	return out, nil
}

// BudgetWarning words the --max-symbols cut in files, the unit Symbols lists.
func (s ScipSource) BudgetWarning(budget int) string {
	return fmt.Sprintf("codeshot: --architecture stopped after %d file(s) (--max-symbols counts files with a SCIP source) — the graph is incomplete; rerun with a larger --max-symbols to cover the rest of the repo. The cut is a path-sorted prefix, not a sample: files later in path order are left out, so their outgoing edges are missing.", budget)
}

func (s ScipSource) EmptyGraphWarning() string {
	return fmt.Sprintf("codeshot: --architecture found no cross-file edges — the diagram is blank. %s has no references from one project file to a symbol another defines (package references alone don't count); check that the indexer ran over the whole project.",
		upperFirst(scipPhrase(s.Index)))
}

// scipLabel is "scip-<tool>" with the indexer's name made filename-safe,
// without doubling the prefix scip-go and its kin already carry.
func scipLabel(x *scipindex.Index) string {
	l := strings.Trim(unsafeToolNameRe.ReplaceAllString(x.Tool, "-"), "-")
	if len(l) > maxToolNameLen {
		l = strings.TrimRight(l[:maxToolNameLen], "-")
	}
	switch {
	case l == "" || l == "scip":
		return "scip"
	case strings.HasPrefix(l, "scip-"):
		return l
	default:
		return "scip-" + l
	}
}

// scipPhrase is "the <tool> index", e.g. "the scip-go index".
func scipPhrase(x *scipindex.Index) string {
	if l := scipLabel(x); l != "scip" {
		return "the " + l + " index"
	}
	return "the SCIP index"
}
