package codeshot

import "fmt"

// Wording is how symbol and --diff mode warnings name the graph a run reads,
// so a snapshot user is never sent to codegraph for a fix.
type Wording struct {
	Graph   string // "codegraph's index", "the cgr snapshot"
	Tool    string // what may not index a language: "codegraph"
	Limit   string // what --limit caps: "codegraph's --limit"
	StaleIs string // "the index may be stale"
	// Refresh is how to bring the graph up to date, for a --path.
	Refresh func(repoPath string) string
}

// CodegraphWording words every warning as v0.4 did.
var CodegraphWording = Wording{
	Graph:   "codegraph's index",
	Tool:    "codegraph",
	Limit:   "codegraph's --limit",
	StaleIs: "the index may be stale",
	Refresh: func(repoPath string) string { return fmt.Sprintf("run 'codegraph sync %s'", repoPath) },
}
