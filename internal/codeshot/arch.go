package codeshot

import (
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
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
			out = append(out, FileEdge{From: from, To: to, Full: slices.Clone(e.Full)})
		} else {
			out[i].Full = intersect(out[i].Full, e.Full)
		}
		out[i].Weight += e.Weight
		out[i].Seen = mergeSeen(out[i].Seen, e.Seen)
	}
	// Multi-source: a group edge's weight is, like a file edge's, the highest
	// any one source gives it (that source's member edges summed), not the
	// sum of per-file maxima, which no source would report.
	for i := range out {
		if len(out[i].Seen) > 0 {
			out[i].Weight = maxSeen(out[i].Seen)
		}
	}
	return out
}

// SourceWeight is one source's weight for a file edge in a multi-source graph.
type SourceWeight struct {
	Source string
	Weight int
}

// mergeSeen adds b's per-source weights into a, keeping a's order and
// appending sources a lacks.
func mergeSeen(a, b []SourceWeight) []SourceWeight {
	for _, w := range b {
		found := false
		for j := range a {
			if a[j].Source == w.Source {
				a[j].Weight += w.Weight
				found = true
				break
			}
		}
		if !found {
			a = append(a, w)
		}
	}
	return a
}

// intersect keeps a's entries that are also in b, in a's order.
func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func maxSeen(seen []SourceWeight) int {
	m := 0
	for _, w := range seen {
		m = max(m, w.Weight)
	}
	return m
}

// UnionFileEdges merges per-source file edges (perSource[i] from labels[i],
// each already aggregated) into one graph: first-seen order across sources in
// --source order, weight = the highest any source gives the edge (the units
// differ — calls for codegraph, references for SCIP — so a sum would mix
// them), Seen = every source's own weight, and Full = the sources that saw
// it, both in source order.
func UnionFileEdges(perSource [][]FileEdge, labels []string) []FileEdge {
	idx := map[string]int{}
	var out []FileEdge
	for si, edges := range perSource {
		for _, e := range edges {
			k := drawnEdgeKey(e.From, e.To)
			i, ok := idx[k]
			if !ok {
				i = len(out)
				idx[k] = i
				out = append(out, FileEdge{From: e.From, To: e.To})
			}
			out[i].Seen = mergeSeen(out[i].Seen, []SourceWeight{{Source: labels[si], Weight: e.Weight}})
			if !slices.Contains(out[i].Full, labels[si]) {
				out[i].Full = append(out[i].Full, labels[si])
			}
			out[i].Weight = max(out[i].Weight, e.Weight)
		}
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

// ForbidRule is one FROM->TO rule, from a --forbid flag or a rules file. A
// side ending in "/" is a directory prefix; anything else is an exact file. A
// rules-file side may also be a pattern (Glob); a flag's side is literal.
type ForbidRule struct {
	From, To, Raw string
	// Breaks words the violation for a rules-file rule ("forbid 'a/->b/' in
	// .codeshot.yaml (reason)"); empty for a --forbid flag.
	Breaks string
	// Glob: the sides are rules-file patterns (globRuleSideMatches). A
	// --forbid flag's sides are literal, as they always were (F1).
	Glob bool
}

// Violation is a file edge a rule forbids.
type Violation struct {
	Rule, From, To string
	Weight         int
	SeenBy         string // multi-source: "codegraph 3, scip-go 5"
	Breaks         string // rules file: what the edge breaks; empty for a --forbid flag
	Kind           string // "layers" for a layer violation; "" for a forbid rule or flag
	Line           int    // --sarif: first call-site line in From; 0 when unknown or not asked
}

// ParseForbidRule parses FROM->TO; ok=false when malformed.
func ParseForbidRule(raw string) (ForbidRule, bool) {
	parts := strings.Split(raw, "->")
	if len(parts) != 2 {
		return ForbidRule{}, false
	}
	from, to := trimRuleSide(parts[0]), trimRuleSide(parts[1])
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

// hasRulePattern: a rules-file side with pattern characters (or an escape),
// matched by globRuleSideMatches rather than the literal rule.
func hasRulePattern(side string) bool { return strings.ContainsAny(side, "*?[\\") }

// trimRuleSide is the clean-up every rule side gets: spaces and a leading "./".
func trimRuleSide(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "./") }

// ruleMatches applies a rule's sides to an edge, literally or as patterns.
func ruleMatches(r ForbidRule, from, to string) bool {
	if r.Glob {
		return globRuleSideMatches(r.From, from) && globRuleSideMatches(r.To, to)
	}
	return pathMatchesRuleSide(r.From, from) && pathMatchesRuleSide(r.To, to)
}

// globRuleSideMatches matches a rules-file side: path.Match segment by
// segment ('*' stays within one segment, '\' escapes), a directory side
// against the file's leading directories. A side with no pattern characters
// is the literal prefix/exact rule.
func globRuleSideMatches(side, filePath string) bool {
	if !hasRulePattern(side) {
		return pathMatchesRuleSide(side, filePath)
	}
	sides := strings.Split(strings.TrimSuffix(side, "/"), "/")
	segs := strings.Split(filePath, "/")
	if strings.HasSuffix(side, "/") {
		if len(segs) <= len(sides) {
			return false
		}
		segs = segs[:len(sides)]
	} else if len(segs) != len(sides) {
		return false
	}
	for i := range sides {
		if ok, _ := path.Match(sides[i], segs[i]); !ok {
			return false
		}
	}
	return true
}

// ForbiddenEdges lists every file edge a rule forbids, heaviest first. Run on
// per-file edges before --group-depth/--max-render: a view must not hide one.
func ForbiddenEdges(fileEdges []FileEdge, rules []ForbidRule) []Violation {
	hits := forbiddenEdges(fileEdges, rules)
	sortViolations(hits)
	return hits
}

// forbiddenEdges is ForbiddenEdges unsorted, for RuleViolations to add to
// and sort once (V8).
func forbiddenEdges(fileEdges []FileEdge, rules []ForbidRule) []Violation {
	var hits []Violation
	for _, r := range rules {
		for _, e := range fileEdges {
			if ruleMatches(r, e.From, e.To) {
				hits = append(hits, Violation{Rule: r.Raw, From: e.From, To: e.To, Weight: e.Weight, SeenBy: seenList(e.Seen, nil), Breaks: r.Breaks})
			}
		}
	}
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
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		// Same file and name (methods on two types): the qualified name
		// decides, so the order never comes from the source's own listing.
		qa, qb := out[a].QualifiedName, out[b].QualifiedName
		if (qa == nil) != (qb == nil) {
			return qa == nil
		}
		return qa != nil && *qa < *qb
	})
	return out
}

// FileEdgesFromIndexRows derives each enumerated definition's callee edges
// from index rows. A definition is every node sharing name, file and qualified
// name (codegraph's overload grouping); its callees are the distinct targets,
// minus file-kind targets and targets reached only by `imports`. Each
// definition counts once, in enumeration order, and its targets are taken by
// target file, then target id. Row order is never used: the index read
// returns rows in SQLite's plan order and a snapshot in its file's order, so
// depending on it would draw one graph in two statement orders.
func FileEdgesFromIndexRows(symbols []Node, rows []codegraph.IndexRow) []SymbolEdge {
	rows = slices.Clone(rows)
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].TargetFile != rows[b].TargetFile {
			return rows[a].TargetFile < rows[b].TargetFile
		}
		return rows[a].Target < rows[b].Target
	})
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
	Rules                             *Rules // --rules; nil when not given
	Tooltips                          bool   // svg: per-source edge weights (multi-source only)
	SiteLines                         bool   // --sarif: look up each violation's first call-site line
}

// ArchSource is where --architecture's graph comes from: every symbol in
// enumeration order (SortSymbolsForEnumeration), and the cross-file callee
// edges of a chosen subset of them. Everything after these two calls is the
// same for every source.
type ArchSource interface {
	Symbols() ([]Node, error)
	FileEdges(symbols []Node) ([]SymbolEdge, error)
	// EmptyGraphWarning explains a blank diagram in this source's terms.
	EmptyGraphWarning() string
}

// budgetWarner is a source whose --max-symbols unit isn't the symbol; it
// words the cut warning in its own unit.
type budgetWarner interface{ BudgetWarning(budget int) string }

// CodegraphSource reads a codegraph index: symbols through the CLI, edges
// straight from .codegraph/codegraph.db.
type CodegraphSource struct {
	CG       *codegraph.Client
	RepoPath string
}

func (s CodegraphSource) Symbols() ([]Node, error) { return enumerateAllSymbols(s.CG, s.RepoPath) }

func (s CodegraphSource) FileEdges(symbols []Node) ([]SymbolEdge, error) {
	return collectFileEdges(symbols, s.RepoPath)
}

func (s CodegraphSource) EmptyGraphWarning() string { return EmptyArchitectureWarning }

// RunArchitectureMode builds the file-level graph and checks --forbid rules.
func RunArchitectureMode(src ArchSource, p ArchParams, stderr io.Writer) (string, []Violation, error) {
	rawFileEdges, err := sourceFileEdges(src, p.MaxSymbols, "", stderr)
	if err != nil {
		return "", nil, err
	}
	dot, vs, err := finishArchitecture(rawFileEdges, p, nil, func() { warn(stderr, src.EmptyGraphWarning()) }, stderr)
	if err == nil && p.SiteLines {
		fillSiteLines(vs, []LabeledSource{{Src: src}}, stderr)
	}
	return dot, vs, err
}

// LabeledSource is one entry of a multi-source --source list: the source and
// the short label its edges are attributed to in the diagram.
type LabeledSource struct {
	Label string
	Src   ArchSource
}

// RunMultiArchitectureMode builds one file-level graph from several sources
// (--source A,B,...): each runs the single-source pipeline up to its file
// edges, with its own --max-symbols budget, then UnionFileEdges merges them.
// --forbid checks the union, so an edge any one source sees can fail the run.
func RunMultiArchitectureMode(srcs []LabeledSource, p ArchParams, stderr io.Writer) (string, []Violation, error) {
	labels := make([]string, len(srcs))
	perSource := make([][]FileEdge, len(srcs))
	for i, s := range srcs {
		labels[i] = s.Label
		edges, err := sourceFileEdges(s.Src, p.MaxSymbols, s.Label, stderr)
		if err != nil {
			return "", nil, labelError(s.Label, err)
		}
		perSource[i] = edges
	}
	union := UnionFileEdges(perSource, labels)
	if len(union) > 0 {
		for i, s := range srcs {
			if len(perSource[i]) == 0 {
				warn(stderr, SourceFoundNoEdgesWarning(s.Label))
			}
		}
	}
	emptyGraph := func() {
		for _, s := range srcs {
			warn(stderr, labelWarning(s.Label, s.Src.EmptyGraphWarning()))
		}
	}
	dot, vs, err := finishArchitecture(union, p, labels, emptyGraph, stderr)
	if err == nil && p.SiteLines {
		fillSiteLines(vs, srcs, stderr)
	}
	return dot, vs, err
}

// sourceFileEdges enumerates one source's symbols, applies --max-symbols and
// aggregates its callee edges into file edges. A non-empty label prefixes the
// budget warning (multi-source runs).
func sourceFileEdges(src ArchSource, maxSymbols int, label string, stderr io.Writer) ([]FileEdge, error) {
	symbols, err := src.Symbols()
	if err != nil {
		return nil, err
	}
	if len(symbols) > maxSymbols {
		symbols = symbols[:maxSymbols]
		msg := SymbolBudgetWarning(maxSymbols)
		if bw, ok := src.(budgetWarner); ok {
			msg = bw.BudgetWarning(maxSymbols)
		}
		warn(stderr, labelWarning(label, msg))
	}
	symbolEdges, err := src.FileEdges(symbols)
	if err != nil {
		return nil, err
	}
	return AggregateFileEdges(symbolEdges), nil
}

// finishArchitecture is everything after the file edges, shared by single-
// and multi-source runs: --forbid, --group-depth rollup, warnings and DOT.
// sources is nil for a single source; emptyGraph warns about a blank diagram.
func finishArchitecture(rawFileEdges []FileEdge, p ArchParams, sources []string, emptyGraph func(), stderr io.Writer) (string, []Violation, error) {
	violations, err := RuleViolations(rawFileEdges, p.Forbid, p.Rules)
	if err != nil {
		return "", nil, err
	}
	warn(stderr, RuleViolationReport(violations, p.Rules != nil))

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
		emptyGraph()
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
	what := "a --forbid rule"
	if p.Rules != nil {
		what = "an architecture rule"
	}
	warn(stderr, HiddenRuleViolationNote(fileEdges, violating, p.MaxRender, what))
	return BuildArchitectureDot(fileEdges, ArchOptions{MaxRender: p.MaxRender, Violating: violating, Sources: sources, Tooltips: p.Tooltips}), violations, nil
}

// warn prints a non-empty message as one stderr line.
func warn(w io.Writer, msg string) {
	if msg != "" {
		fmt.Fprintln(w, msg)
	}
}
