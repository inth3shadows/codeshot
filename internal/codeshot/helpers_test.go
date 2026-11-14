package codeshot

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// wantMatch fails unless the regexp pattern matches s.
func wantMatch(t *testing.T, pattern, s string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(s) {
		t.Errorf("expected /%s/ to match:\n%s", pattern, s)
	}
}

// wantNoMatch fails if the regexp pattern matches s.
func wantNoMatch(t *testing.T, pattern, s string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(s) {
		t.Errorf("expected /%s/ NOT to match:\n%s", pattern, s)
	}
}

func countMatches(pattern, s string) int {
	return len(regexp.MustCompile(pattern).FindAllStringIndex(s, -1))
}

// n builds a symbol node.
func n(name, file string) Node { return Node{Name: name, FilePath: file} }

func strp(s string) *string   { return &s }
func boolp(b bool) *bool      { return &b }
func f64p(f float64) *float64 { return &f }

// unlimited are the "option not given" values the CLI hands the builders.
var unlimitedDot = DotOptions{MaxRender: Unlimited}

// requireBins skips the test when any binary is missing from PATH.
func requireBins(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("`%s` not on PATH", name)
		}
	}
}

// repoRoot is this repository's own checkout, two directories above the package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// requireSelfIndex skips unless codegraph and dot are on PATH and this repo has
// been indexed (`codegraph init`), mirroring run.js's self-skip. Returns the root.
func requireSelfIndex(t *testing.T) string {
	t.Helper()
	requireBins(t, "codegraph", "dot")
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".codegraph", "codegraph.db")); err != nil {
		t.Skip("this repo is not codegraph-indexed (no .codegraph/codegraph.db)")
	}
	return root
}

// runCLI runs the CLI in-process and returns its exit code and output.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

// wantExit runs the CLI and asserts a non-zero exit with stderr matching pattern.
func wantRejected(t *testing.T, pattern string, args ...string) (stderr string) {
	t.Helper()
	code, _, stderr := runCLI(args...)
	if code != 1 {
		t.Errorf("codeshot %s: exit code %d, want 1 (stderr: %s)", strings.Join(args, " "), code, stderr)
	}
	wantMatch(t, pattern, stderr)
	return stderr
}
