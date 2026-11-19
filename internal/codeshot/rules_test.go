package codeshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const goodRules = `# layers top to bottom
version: 1
layers:
  - name: cli
    paths: [cmd/]
  - name: core
    paths: [internal/core/]
  - name: sources
    paths: [internal/src/, "internal/*/adapters/"]
forbid:
  - from: internal/src/a/
    to: internal/src/b/
    reason: readers stay independent
allow:
  - from: internal/src/legacy.go
    to: cmd/
`

func TestParseRulesReadsLayersForbidAndAllow(t *testing.T) {
	r, err := ParseRules(".codeshot.yaml", []byte(goodRules))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Layers) != 3 || r.Layers[2].Name != "sources" || len(r.Layers[2].Paths) != 2 {
		t.Errorf("layers: %+v", r.Layers)
	}
	if len(r.Forbid) != 1 || r.Forbid[0].Breaks != "forbid 'internal/src/a/->internal/src/b/' in .codeshot.yaml (readers stay independent)" {
		t.Errorf("forbid: %+v", r.Forbid)
	}
	if len(r.Allow) != 1 || r.Allow[0].From != "internal/src/legacy.go" {
		t.Errorf("allow: %+v", r.Allow)
	}
}

func TestParseRulesRefusesMistakesNamingTheEntry(t *testing.T) {
	for body, pattern := range map[string]string{
		"":                                       `is empty; it needs at least 'version: 1'`,
		"layers: []\n":                           `missing 'version: 1'`,
		"version: 2\n":                           `version is 2; this codeshot reads version 1`,
		"version: 1\nlayer: []\n":                `field layer not found`,
		"version: 1\nlayers:\n  - paths: [a/]\n": `layers\[0\] has no name`,
		"version: 1\nlayers:\n  - {name: a, paths: [a/]}\n  - {name: a, paths: [b/]}\n": `two layers are named 'a'`,
		"version: 1\nlayers:\n  - {name: a, paths: []}\n":                               `layer 'a' has no paths`,
		"version: 1\nlayers:\n  - {name: a, paths: [main.go]}\n":                        `path 'main\.go' must be a directory ending in '/'`,
		"version: 1\nforbid:\n  - {from: 'a/**/', to: b/}\n":                            `forbid\[0\] from: 'a/\*\*/' uses '\*\*'`,
		"version: 1\nallow:\n  - {from: a/, to: '[x'}\n":                                `allow\[0\] to: '\[x' has a bad pattern`,
		"version: 1\nforbid:\n  - {from: a/}\n":                                         `forbid\[0\] to: is empty`,
		"version: 1\nforbid: {from: a/}\n":                                              `cannot unmarshal`,
	} {
		_, err := ParseRules("r.yaml", []byte(body))
		if err == nil {
			t.Errorf("%q: no error", body)
			continue
		}
		wantMatch(t, `^rules file 'r\.yaml': `, err.Error())
		wantMatch(t, pattern, err.Error())
	}
}

func TestRuleSideGlobsMatchSegmentBySegment(t *testing.T) {
	for _, c := range []struct {
		side, file string
		want       bool
	}{
		{"internal/*/adapters/", "internal/x/adapters/a.go", true},
		{"internal/*/adapters/", "internal/x/y/adapters/a.go", false},
		{"internal/*/adapters/", "internal/x/adapters", false},
		{"*_test.go", "a_test.go", true},
		{"*_test.go", "pkg/a_test.go", false},
		{"pkg/*_test.go", "pkg/a_test.go", true},
		{"pkg/", "pkg/a.go", true}, // no glob: today's prefix rule
		{"pkg/a.go", "pkg/a.go", true},
		{"pkg/[ab].go", "pkg/c.go", false},
	} {
		if got := globRuleSideMatches(c.side, c.file); got != c.want {
			t.Errorf("globRuleSideMatches(%q, %q) = %v, want %v", c.side, c.file, got, c.want)
		}
	}
}

func TestRuleViolationsChecksLayersForbidAndAllow(t *testing.T) {
	r, err := ParseRules(".codeshot.yaml", []byte(goodRules))
	if err != nil {
		t.Fatal(err)
	}
	edges := []FileEdge{
		{From: "cmd/main.go", To: "internal/core/run.go", Weight: 1},            // down: fine
		{From: "internal/core/run.go", To: "internal/core/x.go", Weight: 1},     // same layer: fine
		{From: "internal/src/r.go", To: "internal/core/run.go", Weight: 3},      // up: layer violation
		{From: "internal/q/adapters/a.go", To: "cmd/main.go", Weight: 2},        // glob layer, up two
		{From: "internal/src/legacy.go", To: "cmd/main.go", Weight: 5},          // up, but allowed
		{From: "internal/src/a/x.go", To: "internal/src/b/y.go", Weight: 4},     // forbid rule
		{From: "tools/gen.go", To: "cmd/main.go", Weight: 1},                    // unlayered: fine
		{From: "internal/core/run.go", To: "internal/src/legacy.go", Weight: 1}, // down: fine
	}
	got, err := RuleViolations(edges, []ForbidRule{{From: "tools/", To: "cmd/", Raw: "tools/->cmd/"}}, r)
	if err != nil {
		t.Fatal(err)
	}
	report := ForbidViolationReport(got)
	for _, want := range []string{
		`codeshot: 4 file edge\(s\) break an architecture rule:`,
		`internal/src/a/x\.go -> internal/src/b/y\.go \(weight 4\) breaks forbid 'internal/src/a/->internal/src/b/' in \.codeshot\.yaml \(readers stay independent\)`,
		`internal/src/r\.go -> internal/core/run\.go \(weight 3\) breaks the layers in \.codeshot\.yaml \('sources' may not depend on 'core', a layer above it\)`,
		`internal/q/adapters/a\.go -> cmd/main\.go \(weight 2\) breaks the layers in \.codeshot\.yaml \('sources' may not depend on 'cli', a layer above it\)`,
		`tools/gen\.go -> cmd/main\.go \(weight 1\) breaks --forbid 'tools/->cmd/'`,
	} {
		wantMatch(t, want, report)
	}
	wantNoMatch(t, `legacy\.go -> cmd`, report)
	// Heaviest first.
	if got[0].Weight != 4 || got[len(got)-1].Weight != 1 {
		t.Errorf("order: %+v", got)
	}
}

func TestRuleViolationsRefusesAFileInTwoLayers(t *testing.T) {
	// A pattern layer path can't be checked for overlap when the file is
	// read; the overlap is caught file by file.
	r, err := ParseRules("r.yaml", []byte("version: 1\nlayers:\n  - {name: a, paths: [pkg/]}\n  - {name: b, paths: [\"p*/sub/\"]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RuleViolations([]FileEdge{{From: "pkg/sub/x.go", To: "other.go", Weight: 1}}, nil, r)
	if err == nil {
		t.Fatal("no error")
	}
	wantMatch(t, `'pkg/sub/x\.go' is in two layers, 'a' and 'b'`, err.Error())
}

// An allow rule also covers a --forbid flag.
func TestAllowRuleCoversAForbidFlag(t *testing.T) {
	r, err := ParseRules("r.yaml", []byte("version: 1\nallow:\n  - {from: a/x.go, to: b/}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RuleViolations([]FileEdge{{From: "a/x.go", To: "b/y.go", Weight: 1}, {From: "a/z.go", To: "b/y.go", Weight: 1}},
		[]ForbidRule{{From: "a/", To: "b/", Raw: "a/->b/"}}, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "a/z.go" {
		t.Errorf("got %+v, want only a/z.go -> b/y.go", got)
	}
}

func writeRulesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// End to end from a snapshot: the layer violation fails the run after the
// diagram is written, and is drawn red.
func TestCLIRulesFileFailsTheRunOnALayerViolation(t *testing.T) {
	requireBins(t, "dot")
	dot, _ := exec.LookPath("dot")
	t.Setenv("PATH", filepath.Dir(dot))
	if _, err := exec.LookPath("codegraph"); err == nil {
		t.Skip("codegraph shares a directory with dot; cannot hide it")
	}
	rules := writeRulesFile(t, "version: 1\nlayers:\n  - {name: lib, paths: [lib/]}\n  - {name: src, paths: [src/]}\n")
	out := filepath.Join(t.TempDir(), "arch.dot")
	code, _, stderr := runCLI("--architecture", "--source", "snapshot:"+writeSnapshotFile(t, cliSnapshot), "--rules", rules, "--format", "dot", "--out", out)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, stderr)
	}
	wantMatch(t, `src/a\.go -> lib/b\.go \(weight 1\) breaks the layers in .*rules\.yaml \('src' may not depend on 'lib', a layer above it\)`, stderr)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wantMatch(t, `"src/a\.go" -> "lib/b\.go"[^;]*#dc2626`, string(got))

	// The same graph obeying the layers passes.
	ok := writeRulesFile(t, "version: 1\nlayers:\n  - {name: src, paths: [src/]}\n  - {name: lib, paths: [lib/]}\n")
	if code, _, stderr := runCLI("--architecture", "--source", "snapshot:"+writeSnapshotFile(t, cliSnapshot), "--rules", ok, "--format", "dot", "--out", out); code != 0 {
		t.Fatalf("passing rules: exit %d; stderr: %s", code, stderr)
	}
}

func TestCLIRulesIsArchitectureOnlyAndValidatedFirst(t *testing.T) {
	rules := writeRulesFile(t, "version: 1\n")
	wantRejected(t, `--rules only applies with --architecture`, "Foo", "--rules", rules)
	wantRejected(t, `--rules only applies with --architecture`, "--diff", "--rules", rules)
	wantRejected(t, `--rules must not be empty`, "--architecture", "--rules", "")
	wantRejected(t, `could not read rules file 'missing\.yaml'`, "--architecture", "--rules", "missing.yaml")
	// The rules file fails before the snapshot is read.
	stderr := wantRejected(t, `rules file '.*': version is 2`, "--architecture", "--rules", writeRulesFile(t, "version: 2\n"), "--source", "snapshot:missing.json")
	wantNoMatch(t, `snapshot`, stderr)
	if !strings.Contains(usage, "[--rules <file>]") {
		t.Error("usage does not mention --rules")
	}
}

// F1/F2: --forbid flags match literally, as before rules files existed: a
// bracketed directory (Next.js app/[slug]/) is a directory, not a pattern.
func TestForbidFlagSidesStayLiteral(t *testing.T) {
	edges := []FileEdge{
		{From: "app/[slug]/page.tsx", To: "lib/db/x.ts", Weight: 1},
		{From: "app/s/page.tsx", To: "lib/db/x.ts", Weight: 1},
		{From: "cmd/[a.go", To: "lib/db/x.ts", Weight: 1},
	}
	for raw, want := range map[string][]string{
		"app/[slug]/->lib/db/": {"app/[slug]/page.tsx"},
		"cmd/[->lib/db/":       nil, // "cmd/[" is an exact file, and there is none
		"cmd/*->lib/db/":       nil,
	} {
		r, ok := ParseForbidRule(raw)
		if !ok {
			t.Fatalf("%q: not parsed", raw)
		}
		var got []string
		for _, v := range ForbiddenEdges(edges, []ForbidRule{r}) {
			got = append(got, v.From)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("--forbid %q: got %v, want %v", raw, got, want)
		}
	}
}

// R2: in a rules file a side is a pattern, and '\' escapes a literal bracket.
func TestRulesFileEscapesALiteralBracket(t *testing.T) {
	r, err := ParseRules("r.yaml", []byte("version: 1\nforbid:\n  - {from: 'app/\\[slug\\]/', to: lib/db/}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := RuleViolations([]FileEdge{
		{From: "app/[slug]/page.tsx", To: "lib/db/x.ts", Weight: 1},
		{From: "app/s/page.tsx", To: "lib/db/x.ts", Weight: 1},
	}, nil, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "app/[slug]/page.tsx" {
		t.Errorf("got %+v, want only app/[slug]/page.tsx", got)
	}
}

// F3, F5, F6, F7: refused when the file is read, before any graph work.
func TestParseRulesRefusesOverlapsDocumentsFloatsAndAbsolutePaths(t *testing.T) {
	for body, pattern := range map[string]string{
		"version: 1\nlayers:\n  - {name: a, paths: [internal/]}\n  - {name: b, paths: [internal/src/x/]}\n": `layers 'a' and 'b' overlap: 'internal/' contains 'internal/src/x/'`,
		"version: 1\nlayers:\n  - {name: a, paths: [x/]}\n  - {name: b, paths: [x/]}\n":                     `layers 'a' and 'b' overlap: 'x/' contains 'x/'`,
		"version: 1\nlayers:\n  - {name: a, paths: [x/]}\n---\nforbid: []\n":                                `more than one YAML document`,
		"version: 1.9\n": `version must be the integer 1, got '1\.9'`,
		"version: 1.0\n": `version must be the integer 1, got '1\.0'`,
		"version: 1\nforbid:\n  - {from: /cmd/, to: x/}\n": `forbid\[0\] from: '/cmd/' starts with '/'; sides are relative to the repo root`,
	} {
		_, err := ParseRules("r.yaml", []byte(body))
		if err == nil {
			t.Errorf("%q: no error", body)
			continue
		}
		wantMatch(t, pattern, err.Error())
	}
	// Overlap within one layer is fine.
	if _, err := ParseRules("r.yaml", []byte("version: 1\nlayers:\n  - {name: a, paths: [x/, x/y/]}\n")); err != nil {
		t.Errorf("same-layer overlap: %v", err)
	}
}

// F4: one edge breaking two rules is one edge; the header gives both counts.
func TestReportCountsEdgesAndViolationsApartInRulesMode(t *testing.T) {
	r, err := ParseRules("r.yaml", []byte("version: 1\nlayers:\n  - {name: cli, paths: [cmd/]}\n  - {name: core, paths: [core/]}\nforbid:\n  - {from: core/, to: cmd/}\n"))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := RuleViolations([]FileEdge{{From: "core/a.go", To: "cmd/m.go", Weight: 2}}, nil, r)
	if err != nil {
		t.Fatal(err)
	}
	wantMatch(t, `^codeshot: 1 file edge\(s\) break an architecture rule \(2 violations\):`, ForbidViolationReport(vs))
}

// F8: the hidden-violation note says what kind of rule.
func TestHiddenViolationNoteNamesRulesFileViolations(t *testing.T) {
	edges := []FileEdge{{From: "a", To: "b", Weight: 5}, {From: "c", To: "d", Weight: 1}, {From: "a", To: "e", Weight: 4}}
	violating := ViolatingDrawnKeys([]Violation{{From: "c", To: "d"}}, 0)
	wantMatch(t, `^codeshot: 1 edge\(s\) that break an architecture rule fall outside --max-render 2`, HiddenRuleViolationNote(edges, violating, 2, "an architecture rule"))
}

// T: rules on a two-source union and under --group-depth: provenance in the
// report and the group edge drawn red.
func TestCLIRulesWithTwoSourcesAndGroupDepth(t *testing.T) {
	requireBins(t, "dot")
	rules := writeRulesFile(t, "version: 1\nlayers:\n  - {name: lib, paths: [lib/]}\n  - {name: src, paths: [src/]}\n")
	src := "snapshot:" + writeSnapshotFile(t, cliSnapshot) + ",snapshot:" + writeSnapshotFile(t, cliSnapshotU)
	out := filepath.Join(t.TempDir(), "arch.gv")
	code, _, stderr := runCLI("--architecture", "--source", src, "--rules", rules, "--group-depth", "1", "--format", "canon", "--out", out)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, stderr)
	}
	wantMatch(t, `src/a\.go -> lib/b\.go \(weight 1\) breaks the layers in .*'src' may not depend on 'lib'.* — seen by snapshot-t 1, snapshot-u 1`, stderr)
	wantMatch(t, `src/a\.go -> lib/c\.go \(weight 1\) breaks the layers .* — seen by snapshot-u 1`, stderr)
	got, _ := os.ReadFile(out)
	wantMatch(t, `"src/" -> "lib/"[^;]*#dc2626`, string(got))
}
