package codeshot

import (
	"fmt"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// Every function here is pure and returns "" when there is nothing to say.

// Truncation is whether a callers/callees fetch left matches unshown. Total is
// -1 when codegraph didn't report it.
type Truncation struct {
	Truncated bool
	Total     int
}

// ReadTruncation prefers codegraph's reported `truncated`/`total`; older builds
// report neither, and the fallback is "the result count landed on --limit".
// `truncated: false` is real information, so it is read by presence.
func ReadTruncation(truncated *bool, total *float64, results []Node, limit int) Truncation {
	if truncated != nil {
		t := Truncation{Truncated: *truncated, Total: -1}
		if total != nil && *total == float64(int(*total)) {
			t.Total = int(*total)
		}
		return t
	}
	return Truncation{Truncated: len(results) >= limit, Total: -1}
}

// TruncationWarning reports a --limit cut for symbol mode.
func TruncationWarning(kind string, results []Node, limit int, t Truncation) string {
	if !t.Truncated {
		return ""
	}
	shown := len(results)
	if t.Total >= 0 {
		return fmt.Sprintf("codeshot: showing %d of %d %s — codegraph's --limit (%d) cut off %d; rerun with --limit %d to see the rest.", shown, t.Total, kind, limit, t.Total-shown, t.Total)
	}
	return fmt.Sprintf("codeshot: showing %d %s — codegraph's --limit (%d) may have cut off more; rerun with --limit <n> to see additional %s.", shown, kind, limit, kind)
}

func EmptyGraphWarning(symbol string, nCallers, nCallees int) string {
	if nCallers > 0 || nCallees > 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: '%s' has no callers or callees in codegraph's index — the diagram is just the symbol itself. It may be unused (dead code) or an entry point, or codegraph's index may be incomplete for its file.", symbol)
}

func RenderTruncationNote(kind string, distinct, maxRender int) string {
	if maxRender == Unlimited || distinct <= maxRender {
		return ""
	}
	return fmt.Sprintf("codeshot: rendering %d of %d distinct %s — image capped by --max-render; rerun with a larger --max-render (or omit it) to see the rest.", maxRender, distinct, kind)
}

var knownDotFormats = map[string]bool{"png": true, "svg": true, "svgz": true, "pdf": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "ps": true, "eps": true, "json": true, "dot": true, "xdot": true}

// FormatMismatchWarning: dot writes whatever -T says, whatever --out is named.
func FormatMismatchWarning(outFile, format string) string {
	if outFile == "" {
		return ""
	}
	parts := strings.Split(outFile, ".")
	ext := strings.ToLower(parts[len(parts)-1])
	if !knownDotFormats[ext] || ext == strings.ToLower(format) {
		return ""
	}
	return fmt.Sprintf("codeshot: --out ends in '.%s' but --format is '%s' — the file will contain %s data despite its name; pass --format %s to match, or rename --out.", ext, format, format, ext)
}

func DepthBudgetWarning(truncated bool, budget int) string {
	if !truncated {
		return ""
	}
	return fmt.Sprintf("codeshot: --depth traversal stopped early (safety cap of %d discovered nodes) — the graph beyond this point is incomplete; rerun with a smaller --depth or --limit, a more specific symbol, or a higher --max-depth-nodes to raise the cap.", budget)
}

// SymbolBudgetWarning names the shape of the cut: a path-sorted prefix, so
// later files can look like they call nothing.
func SymbolBudgetWarning(budget int) string {
	return fmt.Sprintf("codeshot: --architecture stopped enumerating after %d symbols (--max-symbols) — the graph is incomplete; rerun with a larger --max-symbols to cover the rest of the repo. Note the cut is a path-sorted prefix, not a sample: files later in path order went unprobed, so they may appear to call nothing when they do.", budget)
}

const EmptyArchitectureWarning = "codeshot: --architecture found no cross-file call edges — the diagram is blank. codegraph's index has no resolved calls between files in this repo (it may be small or single-file, or the index may be missing — run 'codegraph init <path>' to build it, then 'codegraph status' to confirm)."

// GroupCollapseWarning: a rollup that ate every edge. One group means go
// deeper; several mean there is genuinely no coupling between them.
func GroupCollapseWarning(before, after, depth, groupCount int) string {
	if depth == 0 || before == 0 || after > 0 {
		return ""
	}
	advice := "every file falls into a single group at this depth. Try a deeper --group-depth, or drop the flag for the per-file graph."
	if groupCount > 1 {
		advice = fmt.Sprintf("the %d groups at this depth have no calls between them, so there is genuinely no cross-module coupling to draw — a deeper --group-depth will stay blank; drop the flag for the per-file graph.", groupCount)
	}
	return fmt.Sprintf("codeshot: --group-depth %d left no edges to draw — all %d cross-file edge(s) collapsed within a group: %s", depth, before, advice)
}

func ForbidViolationReport(vs []Violation) string {
	if len(vs) == 0 {
		return ""
	}
	lines := make([]string, len(vs))
	for i, v := range vs {
		lines[i] = fmt.Sprintf("  %s -> %s (weight %d) breaks --forbid '%s'", v.From, v.To, v.Weight, v.Rule)
	}
	return fmt.Sprintf("codeshot: %d file edge(s) break a --forbid rule:\n%s", len(vs), strings.Join(lines, "\n"))
}

// HiddenViolationNote counts violating drawn edges --max-render leaves out.
func HiddenViolationNote(fileEdges []FileEdge, violating map[string]bool, maxRender int) string {
	keep := TopFilesByWeight(fileEdges, maxRender)
	if keep == nil || len(violating) == 0 {
		return ""
	}
	hidden := 0
	for _, e := range fileEdges {
		if violating[drawnEdgeKey(e.From, e.To)] && (!keep[e.From] || !keep[e.To]) {
			hidden++
		}
	}
	if hidden == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: %d edge(s) that break a --forbid rule fall outside --max-render %d and aren't drawn; they're listed above.", hidden, maxRender)
}

// IndexHealthWarning warns when codegraph's index was left mid-build, which
// yields silently partial callers/callees. A failing status never blocks.
func IndexHealthWarning(cg *codegraph.Client, repoPath string) string {
	out, ok := cg.Status(repoPath)
	if !ok {
		return ""
	}
	n := codegraph.ParseUnresolvedRefs(out)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: codegraph's index has %d unresolved reference(s) from an interrupted run — callers/callees may be incomplete, so this diagram can be silently partial. Fix with 'codegraph sync %s' (or 'codegraph index' for a full rebuild), then confirm 'codegraph status' shows 0 before trusting the diagram.", n, repoPath)
}

func refPhrase(diffRef string) string {
	if diffRef != "" {
		return fmt.Sprintf(" for '%s'", diffRef)
	}
	return " (working tree matches HEAD)"
}

func DiffNoChangesWarning(diffRef string) string {
	return fmt.Sprintf("codeshot: --diff found no changed files%s — nothing to diagram.", refPhrase(diffRef))
}

func DiffEmbedRefusal(diffRef, embedFile string) string {
	return fmt.Sprintf("codeshot: --diff found no changed files%s — refusing to overwrite the existing diagram embedded in '%s' with a blank one. Pass --diff-ref to target a specific range, or drop --embed to render a (blank) image on its own.", refPhrase(diffRef), embedFile)
}

func DiffNothingToCheck(diffRef, embedFile string) string {
	return fmt.Sprintf("codeshot: --diff found no changed files%s — nothing to diagram, so nothing to check. The diagram embedded in '%s' reflects a different invocation and is left untouched.", refPhrase(diffRef), embedFile)
}

func DiffEmbedRefusalNoSymbols(repoPath string, changed int, embedFile string) string {
	return fmt.Sprintf("codeshot: --diff found %d changed file(s) but no matching symbols in codegraph's index — refusing to overwrite the existing diagram embedded in '%s' with a blank one. They may not define top-level symbols, may be in a language codegraph doesn't index, or the index may be stale (run 'codegraph sync %s'); drop --embed to render a (blank) image on its own instead.", changed, embedFile, repoPath)
}

func DiffNothingToCheckNoSymbols(repoPath string, changed int, embedFile string) string {
	return fmt.Sprintf("codeshot: --diff found %d changed file(s) but no matching symbols in codegraph's index — nothing to diagram, so nothing to check. The diagram embedded in '%s' reflects a different invocation and is left untouched. (They may not define top-level symbols, may be in a language codegraph doesn't index, or the index may be stale — run 'codegraph sync %s' to confirm.)", changed, embedFile, repoPath)
}

func DiffNoSymbolsWarning(repoPath string, changed int) string {
	return fmt.Sprintf("codeshot: --diff found %d changed file(s) but no matching symbols in codegraph's index — they may not define top-level symbols, may be in a language codegraph doesn't index, or the index may be stale (run 'codegraph sync %s').", changed, repoPath)
}

func uniq(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func firstN(names []string, n int) string {
	if len(names) > n {
		names = names[:n]
	}
	return strings.Join(names, ", ")
}

func DiffDuplicateNameWarning(unresolved []string) string {
	names := uniq(unresolved)
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: --diff: %d changed symbol name(s) also exist in another file (e.g. %s) and codegraph gave no per-file answer for them (per-definition results need a codegraph with upstream #1801, which no npm release through 1.6.0 has; with it, this means no definition matched the root's file), so they fell back to the bare-name probe: codegraph's merged result for every same-named definition is attached to each such root in full, not split between them.", len(names), firstN(names, 3))
}

func DiffTruncationWarning(truncated []string, limit int) string {
	if len(truncated) == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: %d changed symbol(s) had a callers/callees fetch cut off by --limit (%d) (e.g. %s); rerun with a larger --limit to see the rest.", len(truncated), limit, firstN(truncated, 3))
}

func DiffEmptyRootsWarning(empty, total int) string {
	if empty == 0 {
		return ""
	}
	return fmt.Sprintf("codeshot: %d of %d changed symbol(s) have no callers or callees in codegraph's index — drawn as lone boxes. They may be unused (dead code) or entry points, or codegraph's index may be incomplete for their file.", empty, total)
}

func DiffSymbolBudgetWarning(matched, budget int) string {
	if matched <= budget {
		return ""
	}
	return fmt.Sprintf("codeshot: --diff matched %d changed symbols but only probing the first %d (--max-symbols) — the diagram is incomplete. The cut is a path-sorted prefix, not a sample: changed symbols in files that sort later are simply missing, not shown as having no callers/callees. Rerun with a larger --max-symbols to cover the rest of the diff.", matched, budget)
}

// BlankEmbedRefusal: the one guard against --embed replacing a committed
// diagram with a blank one (#27), --check included.
func BlankEmbedRefusal(dot, embedFile string, check bool) string {
	if embedFile == "" || !IsBlankDot(dot) {
		return ""
	}
	why := "codeshot: the graph is blank (see the warning above)."
	if check {
		return fmt.Sprintf("%s --check: a blank render is not compared against the diagram embedded in '%s' — fix the cause above; rerunning with --embed would be refused too.", why, embedFile)
	}
	return fmt.Sprintf("%s Refusing to overwrite the existing diagram embedded in '%s' with a blank one; drop --embed to render the (blank) image on its own.", why, embedFile)
}
