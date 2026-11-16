package codeshot

import (
	"fmt"
	"path"
	"strconv"

	"github.com/inth3shadows/codeshot/internal/scipindex"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// NewScipCalls answers symbol mode and --diff from a SCIP index. Its call
// graph (scipindex.Calls: each reference drawn from the innermost
// definition around it, else its file) becomes an in-memory snapshot with
// one "references" edge per (caller, callee), so lookup, grouping, --limit
// and the warnings are the snapshot's own. SCIP doesn't tell a call from
// any other reference, so a type used in a signature is a callee too.
// The warning names the definitions with no enclosing range (scipRangeWarning).
func NewScipCalls(x *scipindex.Index) (*SnapshotCalls, string) {
	g := x.Calls()
	snap := &snapshot.Snapshot{Schema: snapshot.Schema}
	fileID := func(p string) string { return "f:" + p }
	for _, d := range x.Docs {
		snap.Nodes = append(snap.Nodes, snapshot.Node{ID: fileID(d.Path), Kind: "file", Name: path.Base(d.Path), File: d.Path, Start: 1, End: 1})
	}
	bySymbol := map[string][]string{}
	for i, d := range g.Defs {
		id := "d" + strconv.Itoa(i)
		qn := d.QName
		snap.Nodes = append(snap.Nodes, snapshot.Node{ID: id, Kind: d.Kind, Name: d.Name, QName: &qn, File: d.Path, Start: d.Line, End: d.End})
		bySymbol[d.Symbol] = append(bySymbol[d.Symbol], id)
	}
	for _, r := range g.Refs {
		src := fileID(r.Path)
		if r.Caller >= 0 {
			src = "d" + strconv.Itoa(r.Caller)
		}
		for _, dst := range bySymbol[r.Callee] {
			snap.Edges = append(snap.Edges, snapshot.Edge{Kind: "references", Src: src, Dst: dst})
		}
	}
	tool := scipLabel(x)
	if tool == "scip" {
		tool = "the SCIP indexer"
	}
	calls := indexCalls(snap, scipPhrase(x), Wording{
		Graph:   scipPhrase(x),
		Tool:    tool,
		Limit:   "--limit",
		StaleIs: "the index may be stale",
		Refresh: func(string) string { return "rerun " + tool + " on the current code" },
	})
	return calls, scipRangeWarning(x, g)
}

// scipRangeWarning names the definitions the indexer gave no enclosing
// range: references inside them can't be pinned to them, so they are drawn
// from their file (old-style JS `obj.m = function () {}` under
// scip-typescript, for example).
func scipRangeWarning(x *scipindex.Index, g *scipindex.CallGraph) string {
	if g.Unranged == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: %d of %d definitions in %s have no enclosing range, so the references inside them are drawn from their file rather than from them.",
		g.Unranged, len(g.Defs), scipPhrase(x))
}
