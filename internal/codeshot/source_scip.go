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

// Symbols lists every definition plus one file node per document, the way
// codegraph's enumeration includes its file nodes, so a file that defines
// nothing (a script of top-level calls) still counts toward --max-symbols
// and still draws its edges.
func (s ScipSource) Symbols() ([]Node, error) {
	var out []Node
	for _, d := range s.Index.Docs {
		fileID := "\x00file:" + d.Path
		out = append(out, Node{Name: path.Base(d.Path), Kind: "file", FilePath: d.Path, QualifiedName: &fileID})
		for _, def := range d.Defs {
			sym := def.Symbol
			out = append(out, Node{Name: def.Name, Kind: def.Kind, FilePath: d.Path, QualifiedName: &sym})
		}
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
