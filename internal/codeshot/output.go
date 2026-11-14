package codeshot

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// renderDot runs graphviz on the DOT, via stdin. To a file when outFile is
// set, else returned. dot's svg embeds no timestamp or input path, so both
// routes produce the same bytes under one graphviz version.
func renderDot(dot, format, outFile string) ([]byte, error) {
	args := []string{"-T" + format}
	if outFile != "" {
		args = append(args, "-o", outFile)
	}
	cmd := exec.Command("dot", args...)
	cmd.Stdin = strings.NewReader(dot)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Command failed: dot %s\n%s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

var (
	hexEntRe = regexp.MustCompile(`&#[xX]([0-9a-fA-F]+);`)
	decEntRe = regexp.MustCompile(`&#(\d+);`)
	svgGRe   = regexp.MustCompile(`<g\b[^>]*\bclass="(node|edge)"[^>]*>\s*<title>([\s\S]*?)</title>`)
)

// DecodeXMLEntities undoes graphviz's entity encoding in <title>s; &amp; last
// so an encoded "&amp;lt;" isn't double-decoded.
func DecodeXMLEntities(s string) string {
	s = hexEntRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseInt(hexEntRe.FindStringSubmatch(m)[1], 16, 32)
		return string(rune(n))
	})
	s = decEntRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.ParseInt(decEntRe.FindStringSubmatch(m)[1], 10, 32)
		return string(rune(n))
	})
	// One left-to-right pass, so "&amp;lt;" becomes "&lt;", not "<".
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}

// SVGStructure reduces a graphviz svg to its graph structure: the sorted sets
// of node ids and "from->to" edge ids from each <g class="node|edge"> title.
// Layout, colors and the graphviz version stamp are discarded, so --check
// agrees across graphviz builds; only a node or call appearing or vanishing
// changes it.
func SVGStructure(svg string) string {
	nodes, edges := map[string]bool{}, map[string]bool{}
	for _, m := range svgGRe.FindAllStringSubmatch(svg, -1) {
		title := DecodeXMLEntities(strings.TrimSpace(m[2]))
		if m[1] == "node" {
			nodes[title] = true
		} else {
			edges[title] = true
		}
	}
	return "nodes:\n" + strings.Join(sortedKeys(nodes), "\n") + "\nedges:\n" + strings.Join(sortedKeys(edges), "\n")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EmbedMarkers are the HTML-comment markers around an embedded diagram.
func EmbedMarkers(id string) (start, end string) {
	return "<!-- codeshot:" + id + ":start -->", "<!-- codeshot:" + id + ":end -->"
}

// ApplyEmbed sets the codeshot block for id to markdown: replaced in place
// when both markers exist, appended after a blank line when neither does. A
// lone marker is malformed and is an error rather than a risk to the doc.
func ApplyEmbed(content, id, markdown string) (string, error) {
	start, end := EmbedMarkers(id)
	block := start + "\n" + markdown + "\n" + end
	hasStart, hasEnd := strings.Contains(content, start), strings.Contains(content, end)
	if hasStart && hasEnd {
		i := strings.Index(content, start)
		j := strings.Index(content[i+len(start):], end)
		if j < 0 {
			return content, nil
		}
		return content[:i] + block + content[i+len(start)+j+len(end):], nil
	}
	if hasStart != hasEnd {
		which := "end marker with no matching start"
		if hasStart {
			which = "start marker with no matching end"
		}
		return "", fmt.Errorf("--embed: markers for '%s' are malformed in the target doc — found a %s. Fix or remove the stray '<!-- codeshot:%s:... -->' comment and retry.", id, which, id)
	}
	trimmed := strings.TrimRightFunc(content, unicode.IsSpace)
	if trimmed != "" {
		trimmed += "\n\n"
	}
	return trimmed + block + "\n", nil
}

// EmbedRelLink is the image path relative to the doc, with forward slashes.
func EmbedRelLink(embedFile, imagePath string) string {
	docDir, _ := filepath.Abs(filepath.Dir(embedFile))
	img, _ := filepath.Abs(imagePath)
	rel, err := filepath.Rel(docDir, img)
	if err != nil || rel == "" || rel == "." {
		rel = filepath.Base(imagePath)
	}
	return filepath.ToSlash(rel)
}

// OutputParams are the shared output tail's inputs.
type OutputParams struct {
	Format, OutFile, EmbedFile, MarkerID, Alt string
	Check                                     bool
}

func missingEmbed(embedFile string) *Exit {
	return &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --embed target '%s' does not exist — --embed refreshes a diagram inside an existing doc, it does not create one.", embedFile)}
}

// FinishOutput renders the image; with --embed also refreshes the doc block;
// with --embed --check regenerates in memory and verifies both, writing nothing.
func FinishOutput(dot string, p OutputParams, stdout, stderr io.Writer) error {
	if p.EmbedFile == "" {
		if _, err := renderDot(dot, p.Format, p.OutFile); err != nil {
			return err
		}
		fmt.Fprintln(stdout, p.OutFile)
		return nil
	}
	markdown := fmt.Sprintf("![%s](%s)", p.Alt, EmbedRelLink(p.EmbedFile, p.OutFile))
	raw, err := os.ReadFile(p.EmbedFile)
	if err != nil {
		return missingEmbed(p.EmbedFile)
	}
	doc := string(raw)
	expected, err := ApplyEmbed(doc, p.MarkerID, markdown)
	if err != nil {
		return &Exit{Code: 1, Stderr: "codeshot: " + err.Error()}
	}
	if refusal := BlankEmbedRefusal(dot, p.EmbedFile, p.Check); refusal != "" {
		return &Exit{Code: 1, Stderr: refusal}
	}

	if p.Check {
		fresh, err := renderDot(dot, p.Format, "")
		if err != nil {
			return err
		}
		committed, readErr := os.ReadFile(p.OutFile)
		var imageStale bool
		switch {
		case readErr != nil:
			imageStale = true
		case strings.EqualFold(p.Format, "svg"):
			// Structure, not bytes: a laptop and CI with different graphviz
			// builds agree. Other formats have no recoverable structure.
			imageStale = SVGStructure(string(committed)) != SVGStructure(string(fresh))
		default:
			imageStale = !bytes.Equal(committed, fresh)
		}
		docStale := doc != expected
		if imageStale || docStale {
			var msgs []string
			if imageStale {
				why := "differs from a fresh render"
				if readErr != nil {
					why = "missing"
				}
				msgs = append(msgs, fmt.Sprintf("codeshot: --check: diagram '%s' is out of date (%s) — rerun 'codeshot ... --embed %s' and commit the result.", p.OutFile, why, p.EmbedFile))
			}
			if docStale {
				msgs = append(msgs, fmt.Sprintf("codeshot: --check: the codeshot:%s block in '%s' is out of date or missing — rerun 'codeshot ... --embed %s' and commit the result.", p.MarkerID, p.EmbedFile, p.EmbedFile))
			}
			return &Exit{Code: 1, Stderr: strings.Join(msgs, "\n")}
		}
		fmt.Fprintf(stdout, "codeshot: up to date — '%s' and the codeshot:%s block in '%s' match a fresh render.\n", p.OutFile, p.MarkerID, p.EmbedFile)
		return nil
	}

	if _, err := renderDot(dot, p.Format, p.OutFile); err != nil {
		return err
	}
	if expected != doc {
		if err := os.WriteFile(p.EmbedFile, []byte(expected), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "codeshot: updated the codeshot:%s block in %s\n", p.MarkerID, p.EmbedFile)
	}
	fmt.Fprintln(stdout, p.OutFile)
	return nil
}
