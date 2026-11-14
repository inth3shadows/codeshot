// Package codegraph is codeshot's adapter for the CodeGraph CLI and its
// on-disk index. It is the only package that knows CodeGraph's output shapes;
// everything above it works on the types defined here.
package codegraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Node is one symbol as CodeGraph reports it in callers/callees/query JSON.
// QualifiedName is a pointer because its presence is meaningful: the direct
// index read needs it, and codegraph builds before 1.6.0 don't send it.
type Node struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind,omitempty"`
	FilePath      string  `json:"filePath"`
	QualifiedName *string `json:"qualifiedName,omitempty"`
}

// DefinitionRef identifies which definition a per-definition slice belongs to.
type DefinitionRef struct {
	FilePath *string `json:"filePath"`
}

// Definition is one entry of the per-definition breakdown (`definitions[]`)
// that codegraph with upstream #1801 adds to callers/callees --json.
type Definition struct {
	Definition *DefinitionRef `json:"definition"`
	Callers    []Node         `json:"callers"`
	Callees    []Node         `json:"callees"`
	Truncated  *bool          `json:"truncated"`
	Total      *float64       `json:"total"`
}

// Response is a callers/callees --json response. Truncated and Total are
// pointers because absence ("this codegraph doesn't report it") and false/0
// mean different things. Definitions is nil when the response has none.
type Response struct {
	Callers     []Node       `json:"callers"`
	Callees     []Node       `json:"callees"`
	Truncated   *bool        `json:"truncated"`
	Total       *float64     `json:"total"`
	Definitions []Definition `json:"definitions"`
}

// List returns the callers or callees list, by key.
func (r *Response) List(key string) []Node {
	if r == nil {
		return nil
	}
	if key == "callers" {
		return r.Callers
	}
	return r.Callees
}

// FatalError is a failure codeshot reports as one clean line and exit 1.
type FatalError struct{ Msg string }

func (e *FatalError) Error() string { return e.Msg }

func fatalf(format string, a ...any) error { return &FatalError{Msg: fmt.Sprintf(format, a...)} }

var (
	symbolNotFoundRe = regexp.MustCompile(`(?i)Symbol\s+["'“”](.+?)["'“”]\s+not found`)
	notInitializedRe = regexp.MustCompile(`(?i)CodeGraph\s+not\s+initialized`)
	unresolvedRefsRe = regexp.MustCompile(`(?i)([\d,]+)\s+references?\s+from an interrupted run`)
)

// MatchSymbolNotFound returns the symbol name from codegraph's plain-text
// "Symbol 'x' not found" message, and whether it matched.
func MatchSymbolNotFound(out string) (string, bool) {
	m := symbolNotFoundRe.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// MatchNotInitialized reports codegraph's "CodeGraph not initialized" message.
// Only ever matched against stderr of a failed run: a successful response can
// contain the phrase as indexed source content.
func MatchNotInitialized(out string) bool { return notInitializedRe.MatchString(out) }

// ArgRepoPath recovers the --path value from a codegraph argv, defaulting to ".".
func ArgRepoPath(args []string) string {
	for i, a := range args {
		if a == "--path" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "."
}

// ParseUnresolvedRefs reads the "N references from an interrupted run" count
// out of `codegraph status`. Returns 0 when absent.
func ParseUnresolvedRefs(statusOutput string) int {
	m := unresolvedRefsRe.FindStringSubmatch(statusOutput)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Client runs the codegraph CLI. Exec is injectable so tests can replay output.
type Client struct {
	Exec func(args []string) (stdout, stderr string, err error)
}

// NewClient returns a Client that runs the `codegraph` binary on PATH.
func NewClient() *Client {
	return &Client{Exec: func(args []string) (string, string, error) {
		cmd := exec.Command("codegraph", args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}}
}

// RunRaw runs codegraph and applies the not-initialized and not-found checks.
// With fatal=false those two cases return ok=false instead of a FatalError, so
// a loop over many symbols survives one bad name. Any other exec failure is
// returned as a plain error regardless of fatal.
func (c *Client) RunRaw(args []string, fatal bool) (out string, ok bool, err error) {
	stdout, stderr, execErr := c.Exec(args)
	if execErr != nil {
		// Match stderr only: partial stdout can contain the phrase as content.
		if MatchNotInitialized(stderr) {
			if !fatal {
				return "", false, nil
			}
			p := ArgRepoPath(args)
			return "", false, fatalf("codeshot: codegraph has no index for '%s' yet — build one first with 'codegraph init %s', then rerun. (codeshot reads codegraph's index; it doesn't create it.)", p, p)
		}
		return "", false, fmt.Errorf("Command failed: codegraph %s\n%s", strings.Join(args, " "), strings.TrimRight(stderr, "\n"))
	}
	// First non-empty line only: a successful response embeds indexed source,
	// which can contain the not-found sentence as content.
	first := ""
	for _, l := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(l) != "" {
			first = l
			break
		}
	}
	if name, found := MatchSymbolNotFound(first); found {
		if !fatal {
			return "", false, nil
		}
		return "", false, fatalf("codeshot: symbol '%s' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.", name)
	}
	return stdout, true, nil
}

// RunJSON is RunRaw plus JSON decoding into v. Output that isn't JSON is a
// FatalError, or ok=false with fatal=false. Valid JSON of an unexpected shape
// decodes as far as it can, leaving the rest zero.
func (c *Client) RunJSON(args []string, fatal bool, v any) (ok bool, err error) {
	out, ok, err := c.RunRaw(args, fatal)
	if err != nil || !ok {
		return ok, err
	}
	if !json.Valid([]byte(out)) {
		if !fatal {
			return false, nil
		}
		return false, fatalf("codeshot: 'codegraph %s' did not return JSON:\n%s", strings.Join(args, " "), strings.TrimSpace(out))
	}
	_ = json.Unmarshal([]byte(out), v)
	return true, nil
}

// Edges fetches one symbol's callers or callees ("callers"/"callees").
// A nil response with no error means fatal=false hit a not-found/non-JSON case.
func (c *Client) Edges(direction, repoPath string, limit int, name string, fatal bool) (*Response, error) {
	var r Response
	ok, err := c.RunJSON([]string{direction, "--path", repoPath, "--limit", strconv.Itoa(limit), "--json", "--", name}, fatal, &r)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}

type queryHit struct {
	Node *Node `json:"node"`
}

// Query runs `codegraph query --json` and unwraps its {node, score} envelope,
// dropping entries with no usable name. File-kind nodes are kept.
func (c *Client) Query(repoPath string, limit int, q string) ([]Node, error) {
	var hits []queryHit
	if _, err := c.RunJSON([]string{"query", "--path", repoPath, "--json", "--limit", strconv.Itoa(limit), "--", q}, true, &hits); err != nil {
		return nil, err
	}
	return UnwrapQueryNodes(hits), nil
}

// UnwrapQueryNodes drops hits with no node or an empty name.
func UnwrapQueryNodes(hits []queryHit) []Node {
	var nodes []Node
	for _, h := range hits {
		if h.Node != nil && h.Node.Name != "" {
			nodes = append(nodes, *h.Node)
		}
	}
	return nodes
}

// ParseQueryHits decodes a raw `query --json` body; exported for tests.
func ParseQueryHits(raw string) []Node {
	var hits []queryHit
	_ = json.Unmarshal([]byte(raw), &hits)
	return UnwrapQueryNodes(hits)
}

// Status runs `codegraph status <repoPath>`; any failure returns ok=false.
func (c *Client) Status(repoPath string) (string, bool) {
	out, _, err := c.Exec([]string{"status", repoPath})
	if err != nil {
		return "", false
	}
	return out, true
}

// IsFatal reports whether err is a FatalError.
func IsFatal(err error) bool {
	var f *FatalError
	return errors.As(err, &f)
}
