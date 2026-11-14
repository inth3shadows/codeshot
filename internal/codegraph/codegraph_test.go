package codegraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchSymbolNotFound(t *testing.T) {
	// plain-text not-found message, with ANSI colour codes as codegraph prints it
	name, ok := MatchSymbolNotFound("\x1b[34mℹ\x1b[0m Symbol \"Foo\" not found\n")
	if !ok || name != "Foo" {
		t.Errorf("MatchSymbolNotFound(ansi) = %q, %v; want \"Foo\", true", name, ok)
	}
	// a real JSON response should never match
	if name, ok := MatchSymbolNotFound(`{"symbol":"Foo","callers":[]}`); ok {
		t.Errorf("a real JSON response matched as not-found: %q", name)
	}
}

func TestMatchNotInitialized(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"ansi unindexed message", "\x1b[31m✗\x1b[0m CodeGraph not initialized in /repo\n Run \"codegraph init\" first", true},
		{"case-insensitive", "codegraph not initialized", true},
		{"real JSON response", `[{"node":{}}]`, false},
		{"the not-found message is a different case", `Symbol "Foo" not found`, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchNotInitialized(tt.in); got != tt.want {
				t.Errorf("MatchNotInitialized(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestArgRepoPath(t *testing.T) {
	if got := ArgRepoPath([]string{"query", "--path", "/repo/x", "--json", "--", "Foo"}); got != "/repo/x" {
		t.Errorf("ArgRepoPath with --path = %q, want /repo/x", got)
	}
	if got := ArgRepoPath([]string{"callers", "--json"}); got != "." {
		t.Errorf("ArgRepoPath without --path = %q, want \".\" (cwd default)", got)
	}
}

// Regression: codegraph's enumerate query returns indexed node content, and a
// source file can contain "CodeGraph not initialized" in a comment. Scanning
// stdout for that phrase falsely reported a well-indexed repo as uninitialized.
// The phrase is a real signal only on stderr with a non-zero exit.
func TestRunJSONDoesNotTreatContentMentioningNotInitializedAsUnindexed(t *testing.T) {
	stdout := `[{"node":{"name":"matchNotInitialized","content":"detects \"CodeGraph not initialized\""}}]`
	c := &Client{Exec: func(args []string) (string, string, error) { return stdout, "", nil }}
	var got []map[string]map[string]string
	ok, err := c.RunJSON([]string{"query", "--path", ".", "--json", "--", ""}, false, &got)
	if err != nil || !ok {
		t.Fatalf("RunJSON ok=%v err=%v, want ok and no error", ok, err)
	}
	if len(got) != 1 || got[0]["node"]["name"] != "matchNotInitialized" || got[0]["node"]["content"] != `detects "CodeGraph not initialized"` {
		t.Errorf("RunJSON decoded %#v, want the one node with its content intact", got)
	}
}

// Under non-JSON output the response embeds arbitrary source, and a repo's own
// test file can contain the literal not-found sentence. Matching it anywhere in
// the body would null out a perfectly good answer and silently fall back.
func TestRunRawDoesNotMistakeIndexedSourceContentForNotFound(t *testing.T) {
	out := strings.Join([]string{
		"**probe** (function)",
		"",
		"**Location:** test/run.js:700",
		"",
		"```javascript",
		`700	assert.match(err, /Symbol "Foo" not found in the codebase/);`,
		"```",
		"**Calls →** assert (test/run.js:4)",
	}, "\n")
	c := &Client{Exec: func(args []string) (string, string, error) { return out, "", nil }}
	for _, fatal := range []bool{false, true} {
		got, ok, err := c.RunRaw([]string{"node"}, fatal)
		if err != nil || !ok || got != out {
			t.Errorf("RunRaw(fatal=%v) = ok=%v err=%v, out equal=%v; want the body returned untouched", fatal, ok, err, got == out)
		}
	}
}

func TestParseUnresolvedRefs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"comma- and ANSI-tolerant", "\x1b[33m⚠\x1b[0m 4,303 references from an interrupted run are awaiting resolution — some callers/impact edges are missing. Run \"codegraph sync\" to resolve them.", 4303},
		{"plain count", "186 references from an interrupted run", 186},
		{"singular", "1 reference from an interrupted run", 1},
		{"healthy status", "Index Statistics:\n  Files: 40\n  Nodes: 900\n  Edges: 2000", 0},
		{"empty", "", 0},
		{"zero is healthy, not a warning", "0 references from an interrupted run", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseUnresolvedRefs(tt.in); got != tt.want {
				t.Errorf("ParseUnresolvedRefs = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseQueryHitsUnwrapsNodeAndKeepsFileKind(t *testing.T) {
	raw := `[
	  {"node":{"name":"Foo","kind":"function","filePath":"a.js"}},
	  {"node":{"name":"a.js","kind":"file","filePath":"a.js"}},
	  {"node":{"name":"BAR","kind":"constant","filePath":"b.js"}},
	  {"node":null},
	  {"node":{"name":"","kind":"function","filePath":"c.js"}},
	  {"node":{"kind":"function","filePath":"d.js"}}
	]`
	symbols := ParseQueryHits(raw)
	if len(symbols) != 3 {
		t.Fatalf("got %d symbols, want 3: a missing/empty name must be dropped, not passed through to a codegraph call", len(symbols))
	}
	for i, want := range []string{"Foo", "a.js", "BAR"} {
		if symbols[i].Name != want {
			t.Errorf("symbols[%d].Name = %q, want %q", i, symbols[i].Name, want)
		}
	}
}

// Ported from "readIndexRows defers to the CLI when told to or when there is no
// index": the Go FindIndex reports no index as an error (the CLI fallback is gone).
func TestFindIndex(t *testing.T) {
	dir := t.TempDir()
	if p, err := FindIndex(dir); err == nil {
		t.Fatalf("FindIndex on a dir with no .codegraph/codegraph.db = %q, want an error", p)
	}
	// A --path below the project root resolves to the root's index.
	if err := os.MkdirAll(filepath.Join(dir, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, ".codegraph", "codegraph.db")
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindIndex(sub)
	if err != nil || got != db {
		t.Errorf("FindIndex(%q) = %q, %v; want %q", sub, got, err, db)
	}
}
