package codeshot

import (
	"fmt"
	"slices"
	"sort"
	"strings"

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

func (s SnapshotSource) EmptyGraphWarning() string {
	return fmt.Sprintf("codeshot: --architecture found no cross-file call edges — the diagram is blank. The %s snapshot has no %s edges between files; check the tool that exported it.",
		s.Snap.Subject.Name, drawnKindsList())
}

// structuralKinds are edge kinds graph-assay's adapters export that are not a
// file depending on another, so leaving them out of the diagram needs no
// warning. codegraph's own --architecture leaves them out too.
var structuralKinds = []string{"contains", "extends", "implements", "decorates"}

func drawnKindsList() string {
	kinds := slices.Sorted(slices.Values(codegraph.CalleeEdgeKinds))
	return strings.Join(kinds[:len(kinds)-1], ", ") + " or " + kinds[len(kinds)-1]
}

// UndrawnEdgeKindsWarning names the snapshot's edge kinds that --architecture
// neither draws nor knows to be structural, with counts. Another tool's kind
// (code-graph-rag's "overrides", or any kind it passes through) would
// otherwise drop out of the diagram, and out of --forbid, silently.
func UndrawnEdgeKindsWarning(snap *snapshot.Snapshot) string {
	counts := map[string]int{}
	for _, e := range snap.Edges {
		if !slices.Contains(codegraph.CalleeEdgeKinds, e.Kind) && !slices.Contains(structuralKinds, e.Kind) {
			counts[e.Kind]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%s (%d)", k, counts[k])
	}
	return fmt.Sprintf("codeshot: the %s snapshot has edge kinds --architecture doesn't draw: %s. It draws %s edges, so dependencies carried only by these kinds are missing from the diagram and from --forbid.",
		snap.Subject.Name, strings.Join(parts, ", "), strings.ReplaceAll(drawnKindsList(), " or ", " and "))
}

// SnapshotMarkerSuffix is "snapshot-<tool>", the part a snapshot adds to the
// --embed marker id and default image name.
func SnapshotMarkerSuffix(snap *snapshot.Snapshot) string {
	if snap.Subject.Name == "" {
		return "snapshot"
	}
	return "snapshot-" + SanitizeForFilename(snap.Subject.Name)
}
