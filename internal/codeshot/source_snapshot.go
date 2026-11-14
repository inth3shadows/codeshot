package codeshot

import (
	"slices"

	"github.com/inth3shadows/codeshot/internal/codegraph"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// SnapshotSource feeds --architecture from a graph-assay snapshot v1. It maps
// the snapshot onto the rows the codegraph index read produces and hands them
// to the same FileEdgesFromIndexRows, so a snapshot exported from a codegraph
// index draws the same diagram as reading that index directly.
type SnapshotSource struct{ Snap *snapshot.Snapshot }

// identity is the join key FileEdgesFromIndexRows matches a symbol to its
// edges by. The subject's qualified name is used when it has one, as the
// codegraph index read does; otherwise the node id, which a snapshot
// guarantees is unique. The two can't collide: no qname starts with NUL.
func (s SnapshotSource) identity(n snapshot.Node) string {
	if n.QName != nil {
		return *n.QName
	}
	return "\x00id:" + n.ID
}

func (s SnapshotSource) Symbols() ([]Node, error) {
	var out []Node
	for _, n := range s.Snap.Nodes {
		if n.Name == "" {
			continue // as UnwrapQueryNodes drops nameless hits
		}
		qn := s.identity(n)
		out = append(out, Node{Name: n.Name, Kind: n.Kind, FilePath: n.File, QualifiedName: &qn})
	}
	return SortSymbolsForEnumeration(out), nil
}

func (s SnapshotSource) FileEdges(symbols []Node) ([]SymbolEdge, error) {
	byID := make(map[string]snapshot.Node, len(s.Snap.Nodes))
	for _, n := range s.Snap.Nodes {
		byID[n.ID] = n
	}
	var rows []codegraph.IndexRow
	for _, e := range s.Snap.Edges {
		if !slices.Contains(codegraph.CalleeEdgeKinds, e.Kind) {
			continue
		}
		src, dst := byID[e.Src], byID[e.Dst] // Parse guarantees both exist
		rows = append(rows, codegraph.IndexRow{
			Name: src.Name, FilePath: src.File, QualifiedName: s.identity(src),
			Target: e.Dst, EdgeKind: e.Kind, TargetKind: dst.Kind, TargetFile: dst.File,
		})
	}
	return FileEdgesFromIndexRows(symbols, rows), nil
}
