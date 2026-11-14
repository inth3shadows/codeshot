package codeshot

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// Exit ends the run with a code and an optional message on stdout or stderr.
type Exit struct {
	Code           int
	Stdout, Stderr string
}

func (e *Exit) Error() string { return e.Stderr + e.Stdout }

// DuplicateNames is the set of names defined in more than one distinct FILE.
// Same-file collisions have no attribution ambiguity, so they don't count.
func DuplicateNames(symbols []Node) map[string]bool {
	files := map[string]map[string]bool{}
	for _, s := range symbols {
		if files[s.Name] == nil {
			files[s.Name] = map[string]bool{}
		}
		files[s.Name][s.FilePath] = true
	}
	out := map[string]bool{}
	for name, f := range files {
		if len(f) > 1 {
			out[name] = true
		}
	}
	return out
}

// MatchRootSymbols returns the enumerated symbols defined in a changed file.
// File and import nodes are excluded: a file node would draw the file itself
// as a root, and an import's text probed as a name borrows another symbol's edges.
func MatchRootSymbols(symbols []Node, changedFiles []string) []Node {
	changed := map[string]bool{}
	for _, f := range changedFiles {
		changed[strings.ReplaceAll(f, `\`, "/")] = true
	}
	var out []Node
	for _, s := range symbols {
		if s.Kind != "file" && s.Kind != "import" && s.FilePath != "" && changed[strings.ReplaceAll(s.FilePath, `\`, "/")] {
			out = append(out, s)
		}
	}
	return out
}

// PickDefinitionResult keeps the slice of a bare-name callers/callees response
// that belongs to definitions in filePath (codegraph with upstream #1801).
// nil means "no per-file answer": no definitions[] at all, or none in filePath.
// Truncated is set only when every kept definition reports it; Total only when
// every one reports a number.
func PickDefinitionResult(r *codegraph.Response, key, filePath string) *codegraph.Response {
	if r == nil || r.Definitions == nil {
		return nil
	}
	var mine []codegraph.Definition
	for _, d := range r.Definitions {
		if d.Definition != nil && d.Definition.FilePath != nil && *d.Definition.FilePath == filePath {
			mine = append(mine, d)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	out := &codegraph.Response{}
	allTrunc, anyTrunc, allTotal, sum := true, false, true, 0.0
	for _, d := range mine {
		if key == "callers" {
			out.Callers = append(out.Callers, d.Callers...)
		} else {
			out.Callees = append(out.Callees, d.Callees...)
		}
		if d.Truncated == nil {
			allTrunc = false
		} else if *d.Truncated {
			anyTrunc = true
		}
		if d.Total == nil {
			allTotal = false
		} else {
			sum += *d.Total
		}
	}
	if allTrunc {
		out.Truncated = &anyTrunc
	}
	if allTotal {
		out.Total = &sum
	}
	return out
}

// ResolveRootResults narrows each direction independently to filePath's own
// definitions. A direction without a per-file answer keeps the union and marks
// the root unresolved; a nil response adds nothing either way.
func ResolveRootResults(callers, callees *codegraph.Response, filePath string) (*codegraph.Response, *codegraph.Response, bool) {
	unresolved := false
	pick := func(r *codegraph.Response, key string) *codegraph.Response {
		if r == nil {
			return nil
		}
		if own := PickDefinitionResult(r, key, filePath); own != nil {
			return own
		}
		unresolved = true
		return r
	}
	return pick(callers, "callers"), pick(callees, "callees"), unresolved
}

// gitDiffFiles lists changed files: against HEAD by default (staged and
// unstaged), or a ref/range. --end-of-options keeps a ref starting with '-'
// from parsing as a flag; -z keeps non-ASCII names unquoted.
func gitDiffFiles(repoPath, ref string) ([]string, error) {
	target := ref
	if target == "" {
		target = "HEAD"
	}
	args := []string{"diff", "--name-only", "-z", "--end-of-options", target}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		refNote := ""
		if ref != "" {
			refNote = fmt.Sprintf(" and '%s' is a valid ref/range", ref)
		}
		detail := strings.SplitN(strings.TrimSpace(errb.String()), "\n", 2)[0]
		if detail == "" {
			detail = err.Error()
		}
		return nil, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: 'git %s' failed in '%s' — confirm it's a git repo%s. (%s)", strings.Join(args, " "), repoPath, refNote, detail)}
	}
	var files []string
	for _, f := range strings.Split(out.String(), "\x00") {
		if f != "" { // no trimming: a filename may legally carry spaces
			files = append(files, f)
		}
	}
	return files, nil
}

// DiffParams are --diff's inputs.
type DiffParams struct {
	DiffRef                      string
	Limit, MaxSymbols, MaxRender int
	Tooltips                     bool
	EmbedFile                    string
	Check                        bool
}

// handleEmptyRoots: with --embed --check there is nothing to verify (exit 0);
// with --embed, refuse to overwrite a committed diagram with a blank one;
// otherwise warn and render the blank image.
func handleEmptyRoots(p DiffParams, refusal, warnMsg, nothingToCheck string, stderr io.Writer) error {
	if p.EmbedFile != "" && p.Check {
		return &Exit{Code: 0, Stdout: nothingToCheck}
	}
	if p.EmbedFile != "" {
		return &Exit{Code: 1, Stderr: refusal}
	}
	warn(stderr, warnMsg)
	return nil
}

// RunDiffMode diagrams the symbols defined in changed files with their direct
// callers and callees. Probes run sequentially: concurrent codegraph calls
// against one index race on its schema_versions table.
func RunDiffMode(cg *codegraph.Client, repoPath string, p DiffParams, stderr io.Writer) (string, error) {
	changed, err := gitDiffFiles(repoPath, p.DiffRef)
	if err != nil {
		return "", err
	}
	if len(changed) == 0 {
		if err := handleEmptyRoots(p, DiffEmbedRefusal(p.DiffRef, p.EmbedFile), DiffNoChangesWarning(p.DiffRef), DiffNothingToCheck(p.DiffRef, p.EmbedFile), stderr); err != nil {
			return "", err
		}
		return BuildDiffDot(nil, nil, p.MaxRender, p.Tooltips), nil
	}
	all, err := enumerateAllSymbols(cg, repoPath)
	if err != nil {
		return "", err
	}
	matched := MatchRootSymbols(all, changed)
	if len(matched) == 0 {
		if err := handleEmptyRoots(p, DiffEmbedRefusalNoSymbols(repoPath, len(changed), p.EmbedFile), DiffNoSymbolsWarning(repoPath, len(changed)), DiffNothingToCheckNoSymbols(repoPath, len(changed), p.EmbedFile), stderr); err != nil {
			return "", err
		}
		return BuildDiffDot(nil, nil, p.MaxRender, p.Tooltips), nil
	}
	warn(stderr, DiffSymbolBudgetWarning(len(matched), p.MaxSymbols))
	if len(matched) > p.MaxSymbols {
		matched = matched[:p.MaxSymbols]
	}
	roots := DedupeNodes(matched)

	// Collisions are checked against the whole index, not just other roots.
	rootNames := map[string]bool{}
	for _, r := range roots {
		rootNames[r.Name] = true
	}
	var sameNamed []Node
	for _, s := range all {
		if rootNames[s.Name] {
			sameNamed = append(sameNamed, s)
		}
	}
	dupes := DuplicateNames(sameNamed)

	var edges []Edge
	var unresolved, truncated []string
	for _, root := range roots {
		callers, err := cg.Edges("callers", repoPath, p.Limit, root.Name, false)
		if err != nil {
			return "", err
		}
		callees, err := cg.Edges("callees", repoPath, p.Limit, root.Name, false)
		if err != nil {
			return "", err
		}
		if dupes[root.Name] {
			var un bool
			callers, callees, un = ResolveRootResults(callers, callees, root.FilePath)
			if un {
				unresolved = append(unresolved, root.Name)
			}
		}
		callerList, calleeList := callers.List("callers"), callees.List("callees")
		for _, c := range callerList {
			edges = append(edges, Edge{From: c, To: root})
		}
		for _, c := range calleeList {
			edges = append(edges, Edge{From: root, To: c})
		}
		if truncOf(callers, callerList, p.Limit) || truncOf(callees, calleeList, p.Limit) {
			truncated = append(truncated, root.Name)
		}
	}
	warn(stderr, DiffDuplicateNameWarning(unresolved))
	warn(stderr, DiffTruncationWarning(truncated, p.Limit))

	dEdges := DedupeEdges(edges)
	rootKeys := map[string]bool{}
	for _, r := range roots {
		rootKeys[NodeKey(r)] = true
	}
	var pool []Node
	for _, e := range dEdges {
		pool = append(pool, e.From, e.To)
	}
	nonRoot := 0
	for _, n := range DedupeNodes(pool) {
		if !rootKeys[NodeKey(n)] {
			nonRoot++
		}
	}
	warn(stderr, RenderTruncationNote("callers/callees", nonRoot, p.MaxRender))

	withEdges := map[string]bool{}
	for _, e := range dEdges {
		for _, k := range []string{NodeKey(e.From), NodeKey(e.To)} {
			if rootKeys[k] {
				withEdges[k] = true
			}
		}
	}
	warn(stderr, DiffEmptyRootsWarning(len(roots)-len(withEdges), len(roots)))
	return BuildDiffDot(roots, edges, p.MaxRender, p.Tooltips), nil
}

func truncOf(r *codegraph.Response, list []Node, limit int) bool {
	if r == nil {
		return ReadTruncation(nil, nil, list, limit).Truncated
	}
	return ReadTruncation(r.Truncated, r.Total, list, limit).Truncated
}
