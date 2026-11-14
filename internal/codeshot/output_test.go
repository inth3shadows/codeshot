package codeshot

import (
	"regexp"
	"strings"
	"testing"
)

func TestEmbedMarkersKeysStartEndCommentsByTheMarkerID(t *testing.T) {
	start, end := EmbedMarkers("arch")
	if start != "<!-- codeshot:arch:start -->" || end != "<!-- codeshot:arch:end -->" {
		t.Errorf("EmbedMarkers = %q, %q", start, end)
	}
}

func mustApply(t *testing.T, content, id, md string) string {
	t.Helper()
	out, err := ApplyEmbed(content, id, md)
	if err != nil {
		t.Fatalf("ApplyEmbed(%q, %q) = %v", id, md, err)
	}
	return out
}

func TestApplyEmbedAppendsAFreshBlockAfterABlankLineWhenNoMarkersExist(t *testing.T) {
	got := mustApply(t, "# Title\n\nSome prose.\n", "arch", "![x](a.svg)")
	want := "# Title\n\nSome prose.\n\n<!-- codeshot:arch:start -->\n![x](a.svg)\n<!-- codeshot:arch:end -->\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyEmbedReplacesTheExistingBlockInPlaceIdempotentOnReRun(t *testing.T) {
	base := "intro\n\n<!-- codeshot:arch:start -->\n![old](old.svg)\n<!-- codeshot:arch:end -->\n\noutro\n"
	once := mustApply(t, base, "arch", "![new](new.svg)")
	wantMatch(t, `!\[new\]\(new\.svg\)`, once)
	wantNoMatch(t, `old\.svg`, once)
	wantMatch(t, `^intro\n`, once)
	wantMatch(t, `outro\n$`, once)
	// re-applying the same markdown is a no-op
	if again := mustApply(t, once, "arch", "![new](new.svg)"); again != once {
		t.Errorf("re-applying changed the doc:\n%q\n%q", once, again)
	}
}

func TestApplyEmbedKeepsTwoDifferentEmbedIDsIndependentInOneDoc(t *testing.T) {
	doc := "# Doc\n"
	doc = mustApply(t, doc, "arch", "![arch](arch.svg)")
	doc = mustApply(t, doc, "buildDot", "![bd](bd.svg)")
	wantMatch(t, `codeshot:arch:start`, doc)
	wantMatch(t, `codeshot:buildDot:start`, doc)
	// updating arch must not touch the buildDot block
	updated := mustApply(t, doc, "arch", "![arch2](arch2.svg)")
	wantMatch(t, `arch2\.svg`, updated)
	wantMatch(t, `bd\.svg`, updated)
}

func TestApplyEmbedErrorsOnAMalformedLoneMarkerRatherThanManglingTheDoc(t *testing.T) {
	_, err := ApplyEmbed("x\n<!-- codeshot:arch:start -->\ny\n", "arch", "")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("err = %v, want a 'malformed' error", err)
	}
}

func TestApplyEmbedTreatsADollarInTheMarkdownAsALiteralNotAReplacementToken(t *testing.T) {
	base := "<!-- codeshot:arch:start -->\nold\n<!-- codeshot:arch:end -->\n"
	out := mustApply(t, base, "arch", "![a]($1 price.svg)")
	wantMatch(t, `\$1 price\.svg`, out)
}

func TestEmbedRelLinkResolvesTheImagePathRelativeToTheDocForwardSlashed(t *testing.T) {
	if got := EmbedRelLink("/repo/TECHNICAL.md", "/repo/docs/arch.svg"); got != "docs/arch.svg" {
		t.Errorf("got %q, want docs/arch.svg", got)
	}
	if got := EmbedRelLink("/repo/docs/TECHNICAL.md", "/repo/docs/arch.svg"); got != "arch.svg" {
		t.Errorf("got %q, want arch.svg", got)
	}
}

func TestDecodeXMLEntitiesUndoesTheEntitiesGraphvizEmitsInATitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a&#45;&gt;b", "a->b"},
		{"x &amp;&amp; y", "x && y"},
		{"&lt;tag&gt; &quot;q&quot; &apos;a&apos;", `<tag> "q" 'a'`},
		{"&#x41;&#x42;", "AB"},
		// '&amp;' undone last so an already-encoded entity isn't double-decoded
		{"&amp;lt;", "&lt;"},
	}
	for _, tt := range tests {
		if got := DecodeXMLEntities(tt.in); got != tt.want {
			t.Errorf("DecodeXMLEntities(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A minimal graphviz-shaped svg: the graph carries its own <title>, then each
// node/edge is a <g class="node|edge"> whose <title> is the id codeshot wrote.
const svgFixture = `<?xml version="1.0"?>
<svg><g id="graph0" class="graph"><title>callgraph</title>
<g id="node1" class="node"><title>main</title><ellipse/></g>
<g id="node2" class="node"><title>buildDot</title><ellipse/></g>
<g id="edge1" class="edge"><title>main&#45;&gt;buildDot</title><path/></g>
</g></svg>`

func TestSVGStructureExtractsNodeEdgeTitlesAndSkipsTheGraphTitle(t *testing.T) {
	want := "nodes:\nbuildDot\nmain\nedges:\nmain->buildDot"
	if got := SVGStructure(svgFixture); got != want {
		t.Errorf("SVGStructure = %q, want %q", got, want)
	}
}

func TestSVGStructureIsOrderInsensitive(t *testing.T) {
	reordered := strings.Replace(svgFixture, `<g id="node1" class="node"><title>main</title><ellipse/></g>`+"\n", "", 1)
	reordered = strings.Replace(reordered, `<g id="node2" class="node"><title>buildDot</title><ellipse/></g>`,
		`<g id="node2" class="node"><title>buildDot</title><ellipse/></g>`+"\n"+`<g id="node1" class="node"><title>main</title><ellipse/></g>`, 1)
	if reordered == svgFixture {
		t.Fatal("test bug: the fixture was not reordered")
	}
	if SVGStructure(reordered) != SVGStructure(svgFixture) {
		t.Errorf("reordered nodes changed the signature:\n%q\n%q", SVGStructure(reordered), SVGStructure(svgFixture))
	}
}

func TestSVGStructureToleratesClassBeforeIDAttributeOrder(t *testing.T) {
	flipped := regexp.MustCompile(`<g id="([^"]*)" class="([^"]*)">`).ReplaceAllString(svgFixture, `<g class="$2" id="$1">`)
	if flipped == svgFixture {
		t.Fatal("test bug: the fixture was not flipped")
	}
	if SVGStructure(flipped) != SVGStructure(svgFixture) {
		t.Errorf("attribute order changed the signature:\n%q\n%q", SVGStructure(flipped), SVGStructure(svgFixture))
	}
}

func TestSVGStructureIgnoresCosmeticVersionBytesButCatchesADroppedNodeOrEdge(t *testing.T) {
	// version stamp + coordinate jitter must NOT change the signature
	cosmetic := "<!-- Generated by graphviz version 2.42.2 -->\n" + strings.Replace(svgFixture, "<ellipse/>", `<ellipse cx="9" cy="9"/>`, 1)
	if SVGStructure(cosmetic) != SVGStructure(svgFixture) {
		t.Error("cosmetic bytes changed the signature")
	}
	// but removing the edge is real drift
	noEdge := regexp.MustCompile(`<g id="edge1"[\s\S]*?</g>\n`).ReplaceAllString(svgFixture, "")
	if SVGStructure(noEdge) == SVGStructure(svgFixture) {
		t.Error("dropping the edge did not change the signature")
	}
}

func TestSVGStructureMatchesRealGraphvizOutput(t *testing.T) {
	requireBins(t, "dot")
	dot := BuildDot("Target", []Node{n("caller", "a.js")}, []Node{n("callee", "b.js")}, unlimitedDot)
	sig := SVGStructure(runDotSvg(t, dot))
	// every node the DOT declared appears, and both directed edges are recovered
	for _, name := range []string{"Target", "caller", "callee"} {
		wantMatch(t, `(^|\n)`+name+`(\n|$)`, sig)
	}
	wantMatch(t, `caller->Target`, sig)
	wantMatch(t, `Target->callee`, sig)
}
