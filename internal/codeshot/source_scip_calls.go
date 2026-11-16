package codeshot

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

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
	calls := indexCalls(snap, Wording{
		Graph:   scipPhrase(x),
		Tool:    scipTool(x),
		Limit:   "--limit",
		StaleIs: "the index may be stale",
		Refresh: func(string) string { return "rerun " + scipTool(x) + " on the current code" },
	})
	return calls, scipRangeWarning(x, g)
}

// scipTool names the indexer in advice as its metadata does ("scip-go",
// "rust-analyzer"), not as the file-safe scipLabel, which may add a
// "scip-" no binary has; "the SCIP indexer" when the name is missing or
// carries control or format characters (a bidi override would reorder the
// advice on screen).
func scipTool(x *scipindex.Index) string {
	t := strings.TrimSpace(x.Tool)
	if t == "" || strings.ContainsFunc(t, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return "the SCIP indexer"
	}
	return t
}

// scipRangeWarning counts the functions, methods and types the indexer gave
// no enclosing range: references inside them can't be pinned to them, so
// they are drawn from the ranged definition around them, or their file.
// (Old-style JS `obj.m = function () {}`
// isn't counted: scip-typescript gives it neither a range nor a function
// kind, so it is no definition at all.)
func scipRangeWarning(x *scipindex.Index, g *scipindex.CallGraph) string {
	if g.Unranged == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: %d of %d definitions in %s have no enclosing range, so the references inside them are drawn from the definition or file around them.",
		g.Unranged, len(g.Defs), scipPhrase(x))
}
