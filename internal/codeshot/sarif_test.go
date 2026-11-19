package codeshot

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/codeshot/internal/codegraph"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// decodeSARIF reads back what BuildSARIF wrote, as generic JSON.
func decodeSARIF(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	return m
}

func sarifResults(t *testing.T, m map[string]any) []any {
	t.Helper()
	runs := m["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
	res, ok := runs[0].(map[string]any)["results"].([]any)
	if !ok {
		t.Fatalf("results is not a list: %v", runs[0].(map[string]any)["results"])
	}
	return res
}

func TestBuildSARIFWritesOneResultPerViolation(t *testing.T) {
	vs := []Violation{
		{Rule: "a/->b/", From: "a/x.go", To: "b/y.go", Weight: 3, Line: 12, SeenBy: "codegraph 3, scip-go 1"},
		{Rule: "core->cli", From: "core/c.go", To: "cmd/m.go", Weight: 1, Kind: "layers",
			Breaks: "the layers in .codeshot.yaml ('core' may not depend on 'cli', a layer above it)"},
	}
	data, err := BuildSARIF(vs, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeSARIF(t, data)
	if m["version"] != "2.1.0" || m["$schema"] != "https://json.schemastore.org/sarif-2.1.0.json" {
		t.Errorf("header: %v %v", m["version"], m["$schema"])
	}
	driver := m["runs"].([]any)[0].(map[string]any)["tool"].(map[string]any)["driver"].(map[string]any)
	if driver["name"] != "codeshot" || driver["version"] != "1.2.3" {
		t.Errorf("driver: %v", driver)
	}
	rules := driver["rules"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["id"] != "codeshot/forbid" || rules[1].(map[string]any)["id"] != "codeshot/layers" {
		t.Errorf("rules: %v", rules)
	}
	res := sarifResults(t, m)
	if len(res) != 2 {
		t.Fatalf("results: %d", len(res))
	}
	r0, r1 := res[0].(map[string]any), res[1].(map[string]any)
	if r0["ruleId"] != "codeshot/forbid" || r0["ruleIndex"] != 0.0 || r1["ruleId"] != "codeshot/layers" || r1["ruleIndex"] != 1.0 {
		t.Errorf("rule ids: %v %v / %v %v", r0["ruleId"], r0["ruleIndex"], r1["ruleId"], r1["ruleIndex"])
	}
	loc := func(r map[string]any) (string, float64) {
		pl := r["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
		return pl["artifactLocation"].(map[string]any)["uri"].(string), pl["region"].(map[string]any)["startLine"].(float64)
	}
	if uri, line := loc(r0); uri != "a/x.go" || line != 12 {
		t.Errorf("first: %s:%v, want a/x.go:12", uri, line)
	}
	if uri, line := loc(r1); uri != "core/c.go" || line != 1 {
		t.Errorf("second: %s:%v, want core/c.go:1 (line unknown)", uri, line)
	}
	wantMatch(t, `^a/x\.go -> b/y\.go \(weight 3\) breaks --forbid 'a/->b/' — seen by codegraph 3, scip-go 1$`, r0["message"].(map[string]any)["text"].(string))
	wantMatch(t, `breaks the layers in \.codeshot\.yaml`, r1["message"].(map[string]any)["text"].(string))
	rel := r0["relatedLocations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)["artifactLocation"].(map[string]any)["uri"]
	if rel != "b/y.go" {
		t.Errorf("related: %v", rel)
	}
	fp := func(r map[string]any) string {
		return r["partialFingerprints"].(map[string]any)["primaryLocationLineHash"].(string)
	}
	if fp(r0) == fp(r1) || len(fp(r0)) != 34 {
		t.Errorf("fingerprints: %q %q", fp(r0), fp(r1))
	}
	// The fingerprint ignores the line, so an alert survives the call moving.
	moved := vs[0]
	moved.Line = 99
	again, _ := BuildSARIF([]Violation{moved}, "1.2.3")
	if fp(sarifResults(t, decodeSARIF(t, again))[0].(map[string]any)) != fp(r0) {
		t.Error("fingerprint changed with the line")
	}
}

// Zero violations still write a log, with an empty results list (not null),
// so an upload closes alerts that were fixed.
func TestBuildSARIFWithNoViolationsHasAnEmptyResultsList(t *testing.T) {
	data, err := BuildSARIF(nil, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"results": []`) {
		t.Errorf("want an empty results list:\n%s", data)
	}
	if len(sarifResults(t, decodeSARIF(t, data))) != 0 {
		t.Error("results not empty")
	}
}

func TestSnapshotSiteLinesTakeTheSmallestDrawnKindLine(t *testing.T) {
	l := func(n int) *int { return &n }
	snap := &snapshot.Snapshot{
		Nodes: []snapshot.Node{
			{ID: "1", Kind: "function", Name: "A", File: "a.go"},
			{ID: "2", Kind: "function", Name: "B", File: "b.go"},
			{ID: "3", Kind: "file", Name: "c.go", File: "c.go"},
			{ID: "4", Kind: "function", Name: "A2", File: "a.go"},
		},
		Edges: []snapshot.Edge{
			{Kind: "calls", Src: "1", Dst: "2", Line: l(30)},
			{Kind: "calls", Src: "4", Dst: "2", Line: l(8)},
			{Kind: "imports", Src: "1", Dst: "2", Line: l(1)},
			{Kind: "calls", Src: "1", Dst: "3", Line: l(2)},
			{Kind: "calls", Src: "1", Dst: "4", Line: l(3)},
			{Kind: "overrides", Src: "1", Dst: "2", Line: l(4)},
			{Kind: "calls", Src: "2", Dst: "1"},
		},
	}
	got, _ := SnapshotSource{Snap: snap}.SiteLines()
	if len(got) != 1 || got[codegraph.FilePair{From: "a.go", To: "b.go"}] != (codegraph.SiteLine{Line: 8, Exact: true}) {
		t.Errorf("got %v, want only a.go -> b.go at 8", got)
	}
}

type linedSource struct {
	edgesSource
	lines   map[codegraph.FilePair]int
	inexact bool // the lines are start-line fallbacks
}

func (s linedSource) SiteLines() (map[codegraph.FilePair]codegraph.SiteLine, error) {
	out := map[codegraph.FilePair]codegraph.SiteLine{}
	for k, l := range s.lines {
		out[k] = codegraph.SiteLine{Line: l, Exact: !s.inexact}
	}
	return out, nil
}

func TestFillSiteLinesTakesTheSmallestLineAcrossSourcesAndRebases(t *testing.T) {
	a := linedSource{lines: map[codegraph.FilePair]int{{From: "sub/a.go", To: "sub/b.go"}: 9}}
	b := prefixedSource{Src: linedSource{lines: map[codegraph.FilePair]int{{From: "a.go", To: "b.go"}: 4, {From: "./c.go", To: "b.go"}: 7}}, Prefix: "sub/"}
	vs := []Violation{{From: "sub/a.go", To: "sub/b.go"}, {From: "sub/c.go", To: "sub/b.go"}, {From: "x.go", To: "y.go"}}
	var stderr strings.Builder
	fillSiteLines(vs, []LabeledSource{{Label: "cg", Src: a}, {Label: "snap", Src: b}}, &stderr)
	if vs[0].Line != 4 || vs[1].Line != 7 || vs[2].Line != 0 {
		t.Errorf("lines: %d %d %d, want 4 7 0", vs[0].Line, vs[1].Line, vs[2].Line)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr: %s", stderr.String())
	}
}

const sarifSnapshot = `{"schema":"graph-assay/snapshot@1","subject":{"name":"t"},
 "capabilities":{"siteLines":true,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[],"unresolved":[],
 "nodes":[{"id":"1","kind":"function","name":"A","qname":"A","file":"src/a.go","start":1,"end":20},
          {"id":"2","kind":"function","name":"B","qname":"B","file":"lib/b.go","start":1,"end":2}],
 "edges":[{"kind":"calls","src":"1","dst":"2","line":14}]}`

// End to end from a snapshot: the violation lands on its call-site line,
// the run still exits 1 after writing both files, and a clean run writes an
// empty log.
func TestCLISarifWritesViolationsAtTheirCallSite(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	dir := t.TempDir()
	snap := "snapshot:" + writeSnapshotFile(t, sarifSnapshot)
	sarif := filepath.Join(dir, "out.sarif")
	code, _, stderr := runCLI("--architecture", "--source", snap, "--forbid", "src/->lib/", "--sarif", sarif, "--format", "dot", "--out", filepath.Join(dir, "a.dot"))
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, stderr)
	}
	data, err := os.ReadFile(sarif)
	if err != nil {
		t.Fatal(err)
	}
	res := sarifResults(t, decodeSARIF(t, data))
	if len(res) != 1 {
		t.Fatalf("results: %d", len(res))
	}
	pl := res[0].(map[string]any)["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
	if pl["artifactLocation"].(map[string]any)["uri"] != "src/a.go" || pl["region"].(map[string]any)["startLine"] != 14.0 {
		t.Errorf("location: %v", pl)
	}

	clean := filepath.Join(dir, "clean.sarif")
	if code, _, stderr := runCLI("--architecture", "--source", snap, "--forbid", "lib/->src/", "--sarif", clean, "--format", "dot", "--out", filepath.Join(dir, "b.dot")); code != 0 {
		t.Fatalf("clean run: exit %d; stderr: %s", code, stderr)
	}
	data, _ = os.ReadFile(clean)
	if len(sarifResults(t, decodeSARIF(t, data))) != 0 {
		t.Error("clean run wrote results")
	}
}

func TestCLISarifIsArchitectureOnly(t *testing.T) {
	wantRejected(t, `--sarif only applies with --architecture`, "Foo", "--sarif", "x.sarif")
	wantRejected(t, `--sarif only applies with --architecture`, "--diff", "--sarif", "x.sarif")
	wantRejected(t, `--sarif must not be empty`, "--architecture", "--sarif", "")
	if !strings.Contains(usage, "[--sarif <file>]") {
		t.Error("usage does not mention --sarif")
	}
}

// F1/V1: GitHub keys alerts by primaryLocationLineHash; codeshot's own hash
// goes there, from the rule id, the rule and the two files only: not the
// line, not the rules-file path as typed, not the reason. The same rule
// twice on one edge (a flag and the file) is one result; a layer violation
// on that edge is another.
func TestSARIFFingerprintIsThePrimaryLocationLineHash(t *testing.T) {
	flag := Violation{Rule: "src/->lib/", From: "src/a.go", To: "lib/b.go", Weight: 1, Line: 3}
	file := flag
	file.Breaks = "forbid 'src/->lib/' in r.yaml (why)"
	layer := Violation{Rule: "src->lib", From: "src/a.go", To: "lib/b.go", Weight: 1, Kind: "layers", Breaks: "the layers in r.yaml (...)"}
	data, err := BuildSARIF([]Violation{flag, file, flag, layer}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	res := sarifResults(t, decodeSARIF(t, data))
	if len(res) != 2 {
		t.Fatalf("results: %d, want 2 (the forbid once, the layer once)", len(res))
	}
	fp := func(r any) string {
		v, _ := r.(map[string]any)["partialFingerprints"].(map[string]any)["primaryLocationLineHash"].(string)
		return v
	}
	if fp(res[0]) == "" || fp(res[0]) == fp(res[1]) {
		t.Errorf("fingerprints: %q %q", fp(res[0]), fp(res[1]))
	}
	wantMatch(t, `^[0-9a-f]{32}:1$`, fp(res[0]))
	// The rules file spelt ./r.yaml, a reworded reason: the same alert.
	moved := file
	moved.Breaks = "forbid 'src/->lib/' in ./r.yaml (reworded)"
	again, _ := BuildSARIF([]Violation{moved}, "dev")
	if fp(sarifResults(t, decodeSARIF(t, again))[0]) != fp(res[0]) {
		t.Error("the fingerprint depends on the rules-file path or the reason")
	}
}

// F2: uris are percent-encoded per segment; an absolute path is a file URI.
func TestSARIFURIsAreEncoded(t *testing.T) {
	data, err := BuildSARIF([]Violation{{Rule: "a->b", From: "src/a b#c%20d é.go", To: "/abs/x [1].go"}}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	r := sarifResults(t, decodeSARIF(t, data))[0].(map[string]any)
	uri := r["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)["artifactLocation"].(map[string]any)["uri"]
	rel := r["relatedLocations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)["artifactLocation"].(map[string]any)["uri"]
	if uri != "src/a%20b%23c%2520d%20%C3%A9.go" {
		t.Errorf("uri: %v", uri)
	}
	if rel != "file:///abs/x%20%5B1%5D.go" {
		t.Errorf("related uri: %v", rel)
	}
	// V5: a ':' in the first segment is not a scheme; a drive path is absolute.
	for p, want := range map[string]string{"a:b.go": "a%3Ab.go", "x/a:b.go": "x/a:b.go", "C:/repo/a.go": "file:///C:/repo/a.go", `C:\repo\a.go`: "file:///C:/repo/a.go"} {
		if got := sarifURI(p); got != want {
			t.Errorf("sarifURI(%q) = %q, want %q", p, got, want)
		}
	}
}

// A3: every rule has help text.
func TestSARIFRulesHaveHelpText(t *testing.T) {
	data, _ := BuildSARIF(nil, "dev")
	rules := decodeSARIF(t, data)["runs"].([]any)[0].(map[string]any)["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any)
	for _, r := range rules {
		h, _ := r.(map[string]any)["help"].(map[string]any)
		if h == nil || h["text"] == "" || h["text"] == nil {
			t.Errorf("%v: no help.text", r.(map[string]any)["id"])
		}
	}
}

// F4: a snapshot edge without a line falls back to its caller's start line,
// as codegraph does; a real line wins.
func TestSnapshotSiteLinesFallBackToTheCallersStart(t *testing.T) {
	l := func(n int) *int { return &n }
	snap := &snapshot.Snapshot{
		Nodes: []snapshot.Node{
			{ID: "1", Kind: "function", Name: "A", File: "a.go", Start: 40},
			{ID: "2", Kind: "function", Name: "B", File: "b.go", Start: 1},
			{ID: "3", Kind: "function", Name: "C", File: "c.go", Start: 1},
		},
		Edges: []snapshot.Edge{
			{Kind: "references", Src: "1", Dst: "2"},
			{Kind: "calls", Src: "1", Dst: "2", Line: l(55)},
			{Kind: "calls", Src: "1", Dst: "3"},
		},
	}
	got, _ := SnapshotSource{Snap: snap}.SiteLines()
	if got[codegraph.FilePair{From: "a.go", To: "b.go"}] != (codegraph.SiteLine{Line: 55, Exact: true}) || got[codegraph.FilePair{From: "a.go", To: "c.go"}] != (codegraph.SiteLine{Line: 40}) {
		t.Errorf("got %v, want a->b 55, a->c 40", got)
	}
}

type failingLiner struct{ edgesSource }

func (failingLiner) SiteLines() (map[codegraph.FilePair]codegraph.SiteLine, error) {
	return nil, errors.New("SQL logic error: no such column: e.line (1)")
}

// A4: the warning names what failed, without a dangling "its".
func TestSiteLinesWarningNamesTheSource(t *testing.T) {
	vs := []Violation{{From: "a.go", To: "b.go"}}
	var stderr strings.Builder
	fillSiteLines(vs, []LabeledSource{{Src: failingLiner{}}}, &stderr)
	wantMatch(t, `^codeshot: --sarif: no line numbers from the graph source \(the codegraph index has no line columns; a newer codegraph build writes them\); results point at line 1 of each file\.`, stderr.String())
}

// F6 + T: --sarif needs rules; a write failure stops the run before the
// diagram; a stale --check still leaves the SARIF file.
func TestCLISarifNeedsRulesAndFailsCleanly(t *testing.T) {
	wantRejected(t, `--sarif needs --forbid or --rules`, "--architecture", "--sarif", "x.sarif")
	requireBins(t, "dot")
	dir := t.TempDir()
	snap := "snapshot:" + writeSnapshotFile(t, sarifSnapshot)
	out := filepath.Join(dir, "a.dot")
	code, _, stderr := runCLI("--architecture", "--source", snap, "--forbid", "src/->lib/", "--sarif", filepath.Join(dir, "missing", "x.sarif"), "--format", "dot", "--out", out)
	if code != 1 {
		t.Errorf("write failure: exit %d, want 1", code)
	}
	wantMatch(t, `could not write --sarif file`, stderr)
	if _, err := os.Stat(out); err == nil {
		t.Error("the diagram was written after the SARIF write failed")
	}

	doc := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(doc, []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sarif := filepath.Join(dir, "check.sarif")
	code, _, _ = runCLI("--architecture", "--source", snap, "--forbid", "src/->lib/", "--sarif", sarif, "--format", "svg", "--embed", doc, "--check")
	if code != 1 {
		t.Errorf("stale --check: exit %d, want 1", code)
	}
	if data, err := os.ReadFile(sarif); err != nil || len(sarifResults(t, decodeSARIF(t, data))) != 1 {
		t.Errorf("stale --check: SARIF not written with its one result (%v)", err)
	}
}

// V11: across sources, a real call line beats another source's fallback
// start line, whichever is smaller.
func TestFillSiteLinesPrefersAnExactLineAcrossSources(t *testing.T) {
	a := linedSource{lines: map[codegraph.FilePair]int{{From: "a.go", To: "b.go"}: 40}, inexact: true}
	b := linedSource{lines: map[codegraph.FilePair]int{{From: "a.go", To: "b.go"}: 55}}
	vs := []Violation{{From: "a.go", To: "b.go"}}
	fillSiteLines(vs, []LabeledSource{{Label: "x", Src: a}, {Label: "y", Src: b}}, &strings.Builder{})
	if vs[0].Line != 55 {
		t.Errorf("line %d, want 55 (exact beats a start-line fallback)", vs[0].Line)
	}
}

// V10: in a list, the warning is about what that one source alone knew.
func TestSiteLinesWarningInAListSaysOnlyThatSourcesResults(t *testing.T) {
	var stderr strings.Builder
	fillSiteLines([]Violation{{From: "a.go", To: "b.go"}}, []LabeledSource{{Label: "codegraph", Src: failingLiner{}}, {Label: "scip-go", Src: linedSource{}}}, &stderr)
	wantMatch(t, `^codeshot: codegraph: --sarif: no line numbers from codegraph \(.*\); results only it knows of point at line 1 of their file\.`, stderr.String())
}

// V2: a --sarif run on a graph --max-symbols cut writes nothing and fails.
// V3: a rules file that checks nothing is refused with --sarif.
func TestCLISarifRefusesACutGraphAndARulesFileThatChecksNothing(t *testing.T) {
	requireBins(t, "dot")
	dir := t.TempDir()
	snap := "snapshot:" + writeSnapshotFile(t, sarifSnapshot)
	sarif, out := filepath.Join(dir, "x.sarif"), filepath.Join(dir, "a.dot")
	code, _, stderr := runCLI("--architecture", "--source", snap, "--forbid", "src/->lib/", "--max-symbols", "1", "--sarif", sarif, "--format", "dot", "--out", out)
	if code != 1 {
		t.Errorf("cut graph: exit %d, want 1", code)
	}
	wantMatch(t, `--sarif: the scan stopped at --max-symbols 1`, stderr)
	for _, f := range []string{sarif, out} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s written for a cut graph", f)
		}
	}
	for _, body := range []string{"version: 1\n", "version: 1\nallow:\n  - {from: a/, to: b/}\n", "version: 1\nlayers:\n  - {name: only, paths: [a/]}\n"} {
		wantRejected(t, `--sarif needs a rule to check`, "--architecture", "--rules", writeRulesFile(t, body), "--sarif", sarif)
	}
}
