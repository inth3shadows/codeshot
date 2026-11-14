package codeshot

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// SymbolEdge is one callee edge attributed to files, before aggregation.
type SymbolEdge struct{ FromFile, ToFile string }

// AggregateFileEdges sums symbol edges into weighted file edges, in first-seen
// order, dropping self-file edges and any edge without a real file on both ends.
func AggregateFileEdges(edges []SymbolEdge) []FileEdge {
	idx := map[string]int{}
	var out []FileEdge
	for _, e := range edges {
		if e.FromFile == "" || e.ToFile == "" || e.FromFile == e.ToFile {
			continue
		}
		k := e.FromFile + " -> " + e.ToFile
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, FileEdge{From: e.FromFile, To: e.ToFile})
		}
		out[i].Weight++
	}
	return out
}

// GroupPath is the group a file rolls up into at --group-depth: its first
// `depth` directory segments plus "/". A repo-root file stays itself.
func GroupPath(filePath string, depth int) string {
	var parts []string
	for _, p := range pathSepRe.Split(filePath, -1) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return filePath
	}
	dirs := parts[:len(parts)-1]
	if depth < len(dirs) {
		dirs = dirs[:depth]
	}
	return strings.Join(dirs, "/") + "/"
}

// RollupFileEdges rewrites file edges as group edges, summing weights and
// dropping self-group edges. depth 0 (unset) returns the input.
func RollupFileEdges(fileEdges []FileEdge, depth int) []FileEdge {
	if depth == 0 {
		return fileEdges
	}
	idx := map[string]int{}
	var out []FileEdge
	for _, e := range fileEdges {
		from, to := GroupPath(e.From, depth), GroupPath(e.To, depth)
		if from == to {
			continue
		}
		k := from + " -> " + to
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, FileEdge{From: from, To: to})
		}
		out[i].Weight += e.Weight
	}
	return out
}

// TopFilesByWeight returns the top maxRender files by total in+out weight,
// ties in first-seen order; nil means no cap.
func TopFilesByWeight(fileEdges []FileEdge, maxRender int) map[string]bool {
	if maxRender == Unlimited {
		return nil
	}
	type fw struct {
		file string
		w    int
	}
	idx := map[string]int{}
	var ws []fw
	add := func(f string, w int) {
		i, ok := idx[f]
		if !ok {
			i = len(ws)
			idx[f] = i
			ws = append(ws, fw{file: f})
		}
		ws[i].w += w
	}
	for _, e := range fileEdges {
		add(e.From, e.Weight)
		add(e.To, e.Weight)
	}
	sort.SliceStable(ws, func(a, b int) bool { return ws[a].w > ws[b].w })
	keep := map[string]bool{}
	for i := 0; i < len(ws) && i < maxRender; i++ {
		keep[ws[i].file] = true
	}
	return keep
}

func drawnEdgeKey(from, to string) string { return from + "\x00" + to }

// ForbidRule is one --forbid FROM->TO rule. A side ending in "/" is a
// directory prefix; anything else is an exact file.
type ForbidRule struct{ From, To, Raw string }

// Violation is a file edge a rule forbids.
type Violation struct {
	Rule, From, To string
	Weight         int
}

// ParseForbidRule parses FROM->TO; ok=false when malformed.
func ParseForbidRule(raw string) (ForbidRule, bool) {
	parts := strings.Split(raw, "->")
	if len(parts) != 2 {
		return ForbidRule{}, false
	}
	clean := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "./") }
	from, to := clean(parts[0]), clean(parts[1])
	if from == "" || to == "" {
		return ForbidRule{}, false
	}
	return ForbidRule{From: from, To: to, Raw: from + "->" + to}, true
}

func pathMatchesRuleSide(side, filePath string) bool {
	if strings.HasSuffix(side, "/") {
		return strings.HasPrefix(filePath, side)
	}
	return filePath == side
}

// ForbiddenEdges lists every file edge a rule forbids, heaviest first. Run on
// per-file edges before --group-depth/--max-render: a view must not hide one.
func ForbiddenEdges(fileEdges []FileEdge, rules []ForbidRule) []Violation {
	var hits []Violation
	for _, r := range rules {
		for _, e := range fileEdges {
			if pathMatchesRuleSide(r.From, e.From) && pathMatchesRuleSide(r.To, e.To) {
				hits = append(hits, Violation{Rule: r.Raw, From: e.From, To: e.To, Weight: e.Weight})
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Weight != hits[b].Weight {
			return hits[a].Weight > hits[b].Weight
		}
		if hits[a].From != hits[b].From {
			return hits[a].From < hits[b].From
		}
		return hits[a].To < hits[b].To
	})
	return hits
}

// ViolatingDrawnKeys maps violations onto the drawn (possibly grouped) edges.
func ViolatingDrawnKeys(violations []Violation, groupDepth int) map[string]bool {
	at := func(f string) string {
		if groupDepth != 0 {
			return GroupPath(f, groupDepth)
		}
		return f
	}
	keys := map[string]bool{}
	for _, v := range violations {
		if from, to := at(v.From), at(v.To); from != to {
			keys[drawnEdgeKey(from, to)] = true
		}
	}
	return keys
}

// SortSymbolsForEnumeration orders symbols by (filePath, name) byte-wise, so
// a --max-symbols prefix is a deterministic function of the index alone.
func SortSymbolsForEnumeration(symbols []Node) []Node {
	out := append([]Node{}, symbols...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].FilePath != out[b].FilePath {
			return out[a].FilePath < out[b].FilePath
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// FileEdgesFromIndexRows derives each enumerated definition's callee edges
// from index rows. A definition is every node sharing name, file and qualified
// name (codegraph's overload grouping); its callees are the distinct targets,
// minus file-kind targets and targets reached only by `imports`. Each
// definition counts once, in enumeration order.
func FileEdgesFromIndexRows(symbols []Node, rows []codegraph.IndexRow) []SymbolEdge {
	defKey := func(name, file, qn string) string { return name + "\x00" + file + "\x00" + qn }
	symKey := func(s Node) string {
		qn := ""
		if s.QualifiedName != nil {
			qn = *s.QualifiedName
		}
		return defKey(s.Name, s.FilePath, qn)
	}
	type target struct {
		file, kind string
		called     bool
	}
	wanted := map[string]bool{}
	for _, s := range symbols {
		wanted[symKey(s)] = true
	}
	type targets struct {
		order []string
		by    map[string]*target
	}
	defs := map[string]*targets{}
	for _, r := range rows {
		k := defKey(r.Name, r.FilePath, r.QualifiedName)
		if !wanted[k] {
			continue
		}
		d := defs[k]
		if d == nil {
			d = &targets{by: map[string]*target{}}
			defs[k] = d
		}
		t := d.by[r.Target]
		if t == nil {
			t = &target{file: r.TargetFile, kind: r.TargetKind}
			d.by[r.Target] = t
			d.order = append(d.order, r.Target)
		}
		if r.EdgeKind != "imports" {
			t.called = true
		}
	}
	var edges []SymbolEdge
	counted := map[string]bool{}
	for _, s := range symbols {
		k := symKey(s)
		if counted[k] {
			continue
		}
		counted[k] = true
		d := defs[k]
		if d == nil {
			continue
		}
		for _, id := range d.order {
			t := d.by[id]
			if t.kind == "file" || !t.called {
				continue
			}
			edges = append(edges, SymbolEdge{FromFile: s.FilePath, ToFile: t.file})
		}
	}
	return edges
}

// enumerationQueryLimit pushes `query ""` into its return-everything mode; an
// empty query without --limit silently caps near 50. The real cap is
// --max-symbols, applied client-side.
const enumerationQueryLimit = 100000

func enumerateAllSymbols(cg *codegraph.Client, repoPath string) ([]Node, error) {
	nodes, err := cg.Query(repoPath, enumerationQueryLimit, "")
	if err != nil {
		return nil, err
	}
	return SortSymbolsForEnumeration(nodes), nil
}

// ArchOutputBaseName is the checkout directory's name, for the default
// non-embed image name.
func ArchOutputBaseName(repoPath string) string {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}
	if abs == string(filepath.Separator) {
		return ""
	}
	return SanitizeForFilename(filepath.Base(abs))
}

// EmbedPathSuffix is --path relative to its git repository's root, sanitized
// for a filename, or "" at the root or outside git. It keeps the default
// --embed image names of two subdirectory targets apart (F12) while staying
// the same in every clone (#51), since it never includes the checkout's name.
func EmbedPathSuffix(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	top, err := filepath.EvalSymlinks(filepath.FromSlash(strings.TrimSpace(string(out))))
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return SanitizeForFilename(filepath.ToSlash(rel))
}

// collectFileEdges reads the callee edges for the enumerated symbols straight
// from codegraph's index: one read-only query instead of one CLI process per
// symbol. It needs each symbol's qualifiedName (codegraph 1.6.0+).
func collectFileEdges(symbols []Node, repoPath string) ([]SymbolEdge, error) {
	for _, s := range symbols {
		if s.QualifiedName == nil {
			return nil, &codegraph.FatalError{Msg: "codeshot: --architecture reads codegraph's index directly and needs codegraph 1.6.0 or later — this codegraph's `query --json` carries no qualifiedName. Upgrade codegraph and rerun."}
		}
	}
	db, err := codegraph.FindIndex(repoPath)
	if err != nil {
		return nil, &codegraph.FatalError{Msg: fmt.Sprintf("codeshot: --architecture could not find codegraph's index (%v) — run 'codegraph init %s' first.", err, repoPath)}
	}
	rows, err := codegraph.ReadIndexRows(db)
	if codegraph.IsSchemaError(err) {
		return nil, &codegraph.FatalError{Msg: fmt.Sprintf("codeshot: --architecture could not read codegraph's index at '%s' (%v) — this codegraph build's index schema isn't one codeshot knows; please report it with your 'codegraph --version'.", db, err)}
	}
	if err != nil {
		return nil, &codegraph.FatalError{Msg: fmt.Sprintf("codeshot: --architecture could not read codegraph's index at '%s' (%v). If codegraph is still writing it, rerun once that finishes; if its directory is read-only, run 'codegraph sync' somewhere it can write first.", db, err)}
	}
	return FileEdgesFromIndexRows(symbols, rows), nil
}

// ArchParams are --architecture's inputs.
type ArchParams struct {
	MaxSymbols, MaxRender, GroupDepth int
	Forbid                            []ForbidRule
}

// RunArchitectureMode builds the file-level graph and checks --forbid rules.
func RunArchitectureMode(cg *codegraph.Client, repoPath string, p ArchParams, stderr io.Writer) (string, []Violation, error) {
	all, err := enumerateAllSymbols(cg, repoPath)
	if err != nil {
		return "", nil, err
	}
	symbols := all
	if len(symbols) > p.MaxSymbols {
		symbols = symbols[:p.MaxSymbols]
		warn(stderr, SymbolBudgetWarning(p.MaxSymbols))
	}
	symbolEdges, err := collectFileEdges(symbols, repoPath)
	if err != nil {
		return "", nil, err
	}
	rawFileEdges := AggregateFileEdges(symbolEdges)
	violations := ForbiddenEdges(rawFileEdges, p.Forbid)
	warn(stderr, ForbidViolationReport(violations))

	// Rolled up after aggregation, so probing stays file-exact and
	// --group-depth is a pure view over the same data.
	fileEdges := RollupFileEdges(rawFileEdges, p.GroupDepth)
	groupCount := 0
	if p.GroupDepth != 0 {
		groups := map[string]bool{}
		for _, e := range rawFileEdges {
			groups[GroupPath(e.From, p.GroupDepth)] = true
			groups[GroupPath(e.To, p.GroupDepth)] = true
		}
		groupCount = len(groups)
	}
	collapse := GroupCollapseWarning(len(rawFileEdges), len(fileEdges), p.GroupDepth, groupCount)
	warn(stderr, collapse)
	if collapse == "" && len(fileEdges) == 0 {
		warn(stderr, EmptyArchitectureWarning)
	}

	files := map[string]bool{}
	for _, e := range fileEdges {
		files[e.From], files[e.To] = true, true
	}
	kind := "files"
	if p.GroupDepth != 0 {
		kind = "groups"
	}
	warn(stderr, RenderTruncationNote(kind, len(files), p.MaxRender))

	violating := ViolatingDrawnKeys(violations, p.GroupDepth)
	warn(stderr, HiddenViolationNote(fileEdges, violating, p.MaxRender))
	return BuildArchitectureDot(fileEdges, ArchOptions{MaxRender: p.MaxRender, Violating: violating}), violations, nil
}

// warn prints a non-empty message as one stderr line.
func warn(w io.Writer, msg string) {
	if msg != "" {
		fmt.Fprintln(w, msg)
	}
}
