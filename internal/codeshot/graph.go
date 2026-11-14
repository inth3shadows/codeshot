// Package codeshot renders CodeGraph data as diagrams and checks: symbol
// trails, file-level architecture graphs, diff-scoped graphs, --embed/--check
// and --forbid rules.
package codeshot

import (
	"regexp"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// Node is a symbol as the graph source reports it.
type Node = codegraph.Node

// Unlimited marks an unset --max-render.
const Unlimited = -1

// Edge is a directed call between two symbols. Depth is set only for
// transitive (hop >= 2) edges in symbol mode.
type Edge struct {
	From, To Node
	Depth    int
}

// NodeKey is a symbol's identity for dedup and set membership: (name, file),
// since neither alone is unique.
func NodeKey(n Node) string { return n.Name + " " + n.FilePath }

// DedupeNodes keeps the first node per NodeKey, in order.
func DedupeNodes(nodes []Node) []Node {
	seen := map[string]bool{}
	out := []Node{}
	for _, n := range nodes {
		k := NodeKey(n)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

// DedupeEdges keeps the first edge per (from, to) identity, in order.
func DedupeEdges(edges []Edge) []Edge {
	seen := map[string]bool{}
	out := []Edge{}
	for _, e := range edges {
		k := NodeKey(e.From) + " -> " + NodeKey(e.To)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

var (
	camelRe       = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	wordSplitRe   = regexp.MustCompile(`[\s_-]+`)
	pathSepRe     = regexp.MustCompile(`[\\/]`)
	testFirstRe   = regexp.MustCompile(`(?i)^test$`)
	testLastRe    = regexp.MustCompile(`(?i)^tests?$`)
	specLastRe    = regexp.MustCompile(`(?i)^specs?$`)
	testDirRe     = regexp.MustCompile(`(?i)^(tests?|__tests__|spec)$`)
	testFilenameR = regexp.MustCompile(`(?i)[._-](tests?|specs?)\.[^.]+$`)
)

func splitWords(s string) []string {
	var out []string
	for _, w := range wordSplitRe.Split(camelRe.ReplaceAllString(s, "$1 $2"), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// IsTestRef reports whether a symbol looks like test code, by its name's first
// or last word or by a test directory/filename in its path.
func IsTestRef(name, filePath string) bool {
	words := splitWords(name)
	first, last := "", ""
	if len(words) > 0 {
		first, last = words[0], words[len(words)-1]
	}
	if testFirstRe.MatchString(first) || testLastRe.MatchString(last) || specLastRe.MatchString(last) {
		return true
	}
	segs := pathSepRe.Split(filePath, -1)
	filename := segs[len(segs)-1]
	for _, s := range segs {
		if testDirRe.MatchString(s) {
			return true
		}
	}
	return testFilenameR.MatchString(filename)
}

// rootFile marks the queried symbol in nodeIdentities: it has no file, and must
// still count as a distinct file so a same-named caller doesn't merge into it.
const rootFile = "\x00root"

// identities assigns each drawn node a graphviz id unique per (name, file).
// A name living in one file keeps name-as-id; a name shared across files gets
// "name@@file" and a label carrying the file's basename. NUL is never used in
// an id: graphviz is C and would truncate at it, re-merging the nodes.
type identities struct{ files map[string]map[string]bool }

func newIdentities(nodes []Node) identities {
	files := map[string]map[string]bool{}
	for _, n := range nodes {
		if files[n.Name] == nil {
			files[n.Name] = map[string]bool{}
		}
		files[n.Name][n.FilePath] = true
	}
	return identities{files}
}

func (ids identities) collides(name string) bool { return len(ids.files[name]) > 1 }

func (ids identities) idOf(n Node) string {
	if ids.collides(n.Name) {
		return n.Name + "@@" + n.FilePath
	}
	return n.Name
}

// labelOf returns the disambiguating (name, basename) for a colliding node.
func (ids identities) labelOf(n Node) (name, base string, ok bool) {
	if !ids.collides(n.Name) {
		return "", "", false
	}
	base = n.FilePath
	for _, s := range pathSepRe.Split(n.FilePath, -1) {
		if s != "" {
			base = s
		}
	}
	return n.Name, base, true
}

// AllocateRenderBudget spends one shared --max-render allowance across
// dimensions in priority order. Unlimited returns the counts unchanged.
func AllocateRenderBudget(maxRender int, counts []int) []int {
	out := make([]int, len(counts))
	if maxRender == Unlimited {
		copy(out, counts)
		return out
	}
	remaining := maxRender
	for i, c := range counts {
		a := c
		if remaining < a {
			a = remaining
		}
		if a < 0 {
			a = 0
		}
		out[i] = a
		remaining -= a
	}
	return out
}

var depthColors = []string{"#b8c2d0", "#cbd5e1", "#dde3ec"}

// DepthColor fades transitive edges lighter per hop (depth >= 2).
func DepthColor(depth int) string {
	i := depth - 2
	if i > len(depthColors)-1 {
		i = len(depthColors) - 1
	}
	if i < 0 {
		i = 0
	}
	return depthColors[i]
}

// edgeStyleAttrs: a file-kind node is an unresolved module-level reference
// (dotted gray "file"); a test-looking node gets dashed "test".
func edgeStyleAttrs(n Node, colorOverride string) []string {
	if n.Kind == "file" {
		return []string{"style=dotted", `color="#9ca3af"`, `label="file"`}
	}
	var attrs []string
	if colorOverride != "" {
		attrs = append(attrs, `color="`+colorOverride+`"`)
	}
	if IsTestRef(n.Name, n.FilePath) {
		attrs = append(attrs, "style=dashed", `label="test"`)
	}
	return attrs
}

var unsafeFilenameRe = regexp.MustCompile(`[\\/:*?"<>|]`)

// SanitizeForFilename replaces characters that are unsafe in a filename.
func SanitizeForFilename(s string) string { return unsafeFilenameRe.ReplaceAllString(s, "_") }

func esc(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
