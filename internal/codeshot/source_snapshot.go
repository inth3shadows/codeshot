package codeshot

import (
	"fmt"
	"regexp"
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
	return fmt.Sprintf("codeshot: --architecture found no cross-file call edges — the diagram is blank. %s has no %s edges between files (an imports edge counts only alongside one of those); check the tool that exported it.",
		upperFirst(snapshotPhrase(s.Snap)), drawnKindsList(" or "))
}

// structuralKinds are edge kinds graph-assay's adapters export that are not a
// file depending on another, so leaving them out of the diagram needs no
// warning. codegraph's own --architecture leaves them out too.
var structuralKinds = []string{"contains", "extends", "implements", "decorates"}

// drawnKindsList names the kinds that draw an edge on their own: every callee
// kind but imports, which FileEdgesFromIndexRows counts only when the same
// target is also reached by another kind (codegraph's own rule).
func drawnKindsList(last string) string { return calleeKindsList(last, false) }

// calleeKindsList is the sorted callee kinds as prose, "a, b<last>c", with
// or without imports.
func calleeKindsList(last string, imports bool) string {
	var kinds []string
	for _, k := range slices.Sorted(slices.Values(codegraph.CalleeEdgeKinds)) {
		if imports || k != "imports" {
			kinds = append(kinds, k)
		}
	}
	return strings.Join(kinds[:len(kinds)-1], ", ") + last + kinds[len(kinds)-1]
}

func upperFirst(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// undrawnKinds lists the snapshot's edge kinds that are neither callee
// kinds nor known to be structural, with counts ("overrides (3)"), sorted.
func undrawnKinds(snap *snapshot.Snapshot) string {
	counts := map[string]int{}
	for _, e := range snap.Edges {
		if !slices.Contains(codegraph.CalleeEdgeKinds, e.Kind) && !slices.Contains(structuralKinds, e.Kind) {
			counts[e.Kind]++
		}
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
	return strings.Join(parts, ", ")
}

// UndrawnEdgeKindsWarning names the snapshot's edge kinds that --architecture
// neither draws nor knows to be structural, with counts. Another tool's kind
// (code-graph-rag's "overrides", or any kind it passes through) would
// otherwise drop out of the diagram, and out of --forbid, silently.
func UndrawnEdgeKindsWarning(snap *snapshot.Snapshot) string {
	kinds := undrawnKinds(snap)
	if kinds == "" {
		return ""
	}
	return fmt.Sprintf("codeshot: %s has edge kinds --architecture doesn't draw: %s. It draws %s edges (imports only alongside one of those), so dependencies carried only by these kinds are missing from the diagram and from --forbid.",
		snapshotPhrase(snap), kinds, drawnKindsList(" and "))
}

// UnfollowedEdgeKindsWarning is the same check for symbol mode and --diff,
// which follow every callee kind: a snapshot whose calls are "invokes"
// would otherwise draw lone boxes with no word why.
func UnfollowedEdgeKindsWarning(snap *snapshot.Snapshot) string {
	kinds := undrawnKinds(snap)
	if kinds == "" {
		return ""
	}
	return fmt.Sprintf("codeshot: %s has edge kinds codeshot doesn't follow: %s. Callers and callees come from %s edges, so any reached only through these kinds are missing.",
		snapshotPhrase(snap), kinds, calleeKindsList(" and ", true))
}

var unsafeToolNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

const maxToolNameLen = 40

// toolLabel is the snapshot's subject.name cut down to characters safe in a
// filename, a marker id and a markdown link or alt text: anything else
// becomes "-", and it is capped at 40 characters. It may be "".
func toolLabel(snap *snapshot.Snapshot) string {
	l := strings.Trim(unsafeToolNameRe.ReplaceAllString(snap.Subject.Name, "-"), "-")
	if len(l) > maxToolNameLen {
		l = strings.TrimRight(l[:maxToolNameLen], "-")
	}
	return l
}

// snapshotPhrase is "the <tool> snapshot", or "the snapshot" without a name.
func snapshotPhrase(snap *snapshot.Snapshot) string {
	if l := toolLabel(snap); l != "" {
		return "the " + l + " snapshot"
	}
	return "the snapshot"
}

// SnapshotMarkerSuffix is "snapshot-<tool>", the part a snapshot adds to the
// --embed marker id and default image name.
func SnapshotMarkerSuffix(snap *snapshot.Snapshot) string {
	if l := toolLabel(snap); l != "" {
		return "snapshot-" + l
	}
	return "snapshot"
}
