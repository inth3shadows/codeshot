package codeshot

import (
	"fmt"
	"strconv"
	"strings"
)

// GitHub renders Mermaid with its defaults, which refuse larger diagrams.
const (
	mermaidMaxEdges = 500
	mermaidMaxText  = 50000
)

// mermaidLabel escapes text for a quoted Mermaid label as "#N;" entities
// (one pass, so the '#' they start with is never re-escaped): the quote and
// HTML characters, and what Mermaid interprets inside a label (F1): '\' ("\n"
// is a line break), a backtick (a leading one opens a markdown string), '%'
// (%%{...}%% directives), '$' ($$ math), ':' (fa: icons). A line break is a
// space.
func mermaidLabel(s string) string {
	return strings.NewReplacer("#", "#35;", `"`, "#34;", "<", "#60;", ">", "#62;", "&", "#38;",
		`\`, "#92;", "`", "#96;", "%", "#37;", "$", "#36;", ":", "#58;", "\n", " ", "\r", " ").Replace(s)
}

// BuildArchitectureMermaid draws the same graph as BuildArchitectureDot as a
// Mermaid flowchart: the same files and edges (archDrawn), test files dashed,
// --forbid/--rules violations red, and, with several sources, edges only some
// saw dashed with each source's weight and a title naming the sources.
func BuildArchitectureMermaid(fileEdges []FileEdge, opt ArchOptions) string {
	kept, files := archDrawn(fileEdges, opt.MaxRender)
	var lines []string
	multi := len(opt.Sources) > 1
	if multi {
		title := fmt.Sprintf("Sources: %s. Dashed: not every source saw all of the edge; its label gives each source's own weight.", strings.Join(opt.Sources, ", "))
		lines = append(lines, "---", "title: "+strconv.Quote(title), "---")
	}
	lines = append(lines, "flowchart LR",
		"  classDef default fill:#f8fafc,stroke:#cbd5e1,color:#334155",
		"  classDef test fill:#f8fafc,stroke:#cbd5e1,color:#334155,stroke-dasharray:4 3")
	id := make(map[string]string, len(files))
	for i, f := range files {
		id[f] = fmt.Sprintf("n%d", i)
		class := ""
		if IsTestRef("", f) {
			class = ":::test"
		}
		lines = append(lines, fmt.Sprintf(`  %s("%s")%s`, id[f], mermaidLabel(f), class))
	}
	var red []string
	for i, e := range kept {
		label, arrow := fmt.Sprintf("%d", e.Weight), "-->"
		if multi && len(e.Full) < len(opt.Sources) {
			label, arrow = seenList(e.Seen, opt.Sources), "-.->"
		}
		lines = append(lines, fmt.Sprintf(`  %s %s|"%s"| %s`, id[e.From], arrow, mermaidLabel(label), id[e.To]))
		if opt.Violating[drawnEdgeKey(e.From, e.To)] {
			red = append(red, fmt.Sprintf("%d", i))
		}
	}
	if len(red) > 0 {
		lines = append(lines, "  linkStyle "+strings.Join(red, ",")+" stroke:#dc2626,color:#dc2626,stroke-width:2px")
	}
	return strings.Join(lines, "\n")
}

// mermaidWithTitle adds the accessible title (the alt text) after the
// flowchart line.
func mermaidWithTitle(text, title string) string {
	if title == "" {
		return text
	}
	return strings.Replace(text, "flowchart LR\n", "flowchart LR\n  accTitle: "+strings.ReplaceAll(title, "\n", " ")+"\n", 1)
}

// MermaidLimitWarning: GitHub won't render a Mermaid diagram past its limits.
func MermaidLimitWarning(text string) string {
	edges := strings.Count(text, "-->|") + strings.Count(text, "-.->|")
	if edges <= mermaidMaxEdges && len(text) <= mermaidMaxText {
		return ""
	}
	return fmt.Sprintf("codeshot: the Mermaid diagram has %d edges and %d characters; GitHub renders at most %d edges and %d characters, so it will show an error there. Narrow it with --max-render or --group-depth.", edges, len(text), mermaidMaxEdges, mermaidMaxText)
}
