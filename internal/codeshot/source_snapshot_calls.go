package codeshot

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// SnapshotCalls answers symbol mode and --diff from a graph-assay snapshot,
// the way the codegraph CLI answers `callers`/`callees --json` from its
// index: every definition a name has, grouped per (file, qualified name),
// with each group's callers or callees over the callee edge kinds, sorted
// and capped at --limit, and the same for the union of the groups.
type SnapshotCalls struct {
	snap   *snapshot.Snapshot
	byName map[string][]snapshot.Node
	byID   map[string]snapshot.Node
	in     map[string][]snapshot.Edge // by dst, callee kinds only, in file order
	out    map[string][]snapshot.Edge // by src
}

// NewSnapshotCalls indexes snap for lookups.
func NewSnapshotCalls(snap *snapshot.Snapshot) *SnapshotCalls {
	s := &SnapshotCalls{snap: snap, byName: map[string][]snapshot.Node{}, byID: map[string]snapshot.Node{},
		in: map[string][]snapshot.Edge{}, out: map[string][]snapshot.Edge{}}
	for _, n := range snap.Nodes {
		s.byID[n.ID] = n
		if n.Name != "" {
			s.byName[n.Name] = append(s.byName[n.Name], n)
		}
	}
	for _, e := range snap.Edges {
		if slices.Contains(codegraph.CalleeEdgeKinds, e.Kind) {
			s.in[e.Dst] = append(s.in[e.Dst], e)
			s.out[e.Src] = append(s.out[e.Src], e)
		}
	}
	return s
}

var scopeSepRe = regexp.MustCompile(`::|[/#]`)

// canonicalScope writes every scope separator as ".", so `pkg::f`,
// `pkg/f` and `pkg.f` compare equal.
func canonicalScope(s string) string { return scopeSepRe.ReplaceAllString(s, ".") }

func isQualified(q string) bool { return strings.ContainsAny(q, "./#") || strings.Contains(q, "::") }

// lookup is every node the query names: an exact name first (a name may
// itself hold a separator: `App.Fmt`, `a.go`), else for a qualified query
// (`T.m`, `pkg::f`, `T#m`) the nodes whose qualified name is it or ends
// with it. Never a fuzzy hit: a partial or mistyped name finds nothing, as
// in codegraph since #1473.
func (s *SnapshotCalls) lookup(query string) []snapshot.Node {
	if nodes := s.byName[query]; len(nodes) > 0 || !isQualified(query) {
		return nodes
	}
	want := canonicalScope(query)
	var out []snapshot.Node
	for _, n := range s.snap.Nodes {
		if n.Name == "" || n.QName == nil {
			continue
		}
		if q := canonicalScope(*n.QName); q == want || strings.HasSuffix(q, "."+want) {
			out = append(out, n)
		}
	}
	return out
}

// Resolve keeps the query as given: lookup matches it exactly or as a
// qualified name, so passing it on to Edges draws only what it names
// (`T.Run`, not every Run).
func (s *SnapshotCalls) Resolve(query string) (string, error) {
	if len(s.lookup(query)) == 0 {
		return "", &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: symbol '%s' not found in %s — check the spelling/casing. With a snapshot, codeshot matches an exact name, or a qualified one such as Type.method; it never guesses.", query, snapshotPhrase(s.snap))}
	}
	return query, nil
}

func (s *SnapshotCalls) node(n snapshot.Node) Node {
	return Node{Name: n.Name, Kind: n.Kind, FilePath: n.File, QualifiedName: n.QName}
}

// neighbours is target's callers or callees, deduped by node id in edge
// order, without target itself (codegraph marks it visited first).
func (s *SnapshotCalls) neighbours(direction string, target snapshot.Node) []snapshot.Node {
	edges, end := s.in[target.ID], func(e snapshot.Edge) string { return e.Src }
	if direction == "callees" {
		edges, end = s.out[target.ID], func(e snapshot.Edge) string { return e.Dst }
	}
	var out []snapshot.Node
	seen := map[string]bool{target.ID: true}
	for _, e := range edges {
		if id := end(e); !seen[id] {
			seen[id] = true
			out = append(out, s.byID[id])
		}
	}
	return out
}

func (s *SnapshotCalls) Edges(direction, name string, limit int, fatal bool) (*codegraph.Response, error) {
	targets := s.lookup(name)
	if len(targets) == 0 {
		if !fatal {
			return nil, nil
		}
		return nil, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: symbol '%s' not found in %s.", name, snapshotPhrase(s.snap))}
	}
	type group struct {
		file  string
		nodes []snapshot.Node
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, t := range targets {
		qn := ""
		if t.QName != nil {
			qn = *t.QName
		}
		k := t.File + "|" + qn
		g := byKey[k]
		if g == nil {
			g = &group{file: t.File}
			byKey[k] = g
			groups = append(groups, g)
		}
		seen := map[string]bool{}
		for _, n := range g.nodes {
			seen[n.ID] = true
		}
		for _, n := range s.neighbours(direction, t) {
			if !seen[n.ID] {
				seen[n.ID] = true
				g.nodes = append(g.nodes, n)
			}
		}
	}
	r := &codegraph.Response{Definitions: []codegraph.Definition{}}
	var union []snapshot.Node
	inUnion := map[string]bool{}
	for _, g := range groups {
		file := g.file
		list, total, trunc := s.capped(g.nodes, limit)
		d := codegraph.Definition{Definition: &codegraph.DefinitionRef{FilePath: &file}, Truncated: &trunc, Total: &total}
		if direction == "callers" {
			d.Callers = list
		} else {
			d.Callees = list
		}
		r.Definitions = append(r.Definitions, d)
		for _, n := range g.nodes {
			if !inUnion[n.ID] {
				inUnion[n.ID] = true
				union = append(union, n)
			}
		}
	}
	list, total, trunc := s.capped(union, limit)
	if direction == "callers" {
		r.Callers = list
	} else {
		r.Callees = list
	}
	r.Truncated, r.Total = &trunc, &total
	return r, nil
}

// capped is nodes sorted, then cut to limit, with the uncut total and
// whether it cut. Sorting first keeps which neighbours survive the cut
// independent of the snapshot's node and edge order.
func (s *SnapshotCalls) capped(nodes []snapshot.Node, limit int) ([]Node, float64, bool) {
	all := make([]Node, len(nodes))
	for i, n := range nodes {
		all[i] = s.node(n)
	}
	all = SortSymbolsForEnumeration(all)
	return all[:min(limit, len(all))], float64(len(all)), len(all) > limit
}

// Symbols is every named node in enumeration order, with its real qualified
// name (nil when the snapshot has none).
func (s *SnapshotCalls) Symbols() ([]Node, error) {
	var out []Node
	for _, n := range s.snap.Nodes {
		if n.Name != "" {
			out = append(out, s.node(n))
		}
	}
	return SortSymbolsForEnumeration(out), nil
}

func (s *SnapshotCalls) Wording() Wording {
	tool := toolLabel(s.snap)
	if tool == "" {
		tool = "the snapshot's tool"
	}
	return Wording{
		Graph:   snapshotPhrase(s.snap),
		Tool:    tool,
		Limit:   "--limit",
		StaleIs: "the snapshot may be stale",
		Refresh: func(string) string { return "export a new snapshot from the current code" },
	}
}
