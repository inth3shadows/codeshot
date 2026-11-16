package codeshot

import (
	"fmt"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// CallSource is where symbol mode and --diff get their graph: a symbol's
// canonical name, its callers or callees, and (for --diff) every symbol.
type CallSource interface {
	// Resolve maps the symbol the user typed to the name Edges takes.
	Resolve(query string) (string, error)
	// Edges is name's callers or callees in codegraph's callers/callees
	// --json shape. With fatal false, a lookup failure is (nil, nil).
	Edges(direction, name string, limit int, fatal bool) (*codegraph.Response, error)
	// Symbols is every symbol, as --architecture enumerates them.
	Symbols() ([]Node, error)
	// Wording names this source in warnings.
	Wording() Wording
}

// CodegraphCalls asks the codegraph CLI.
type CodegraphCalls struct {
	CG       *codegraph.Client
	RepoPath string
}

// Resolve maps a possibly partial query to codegraph's canonical name:
// callers/callees fuzzy-match but echo the raw query back.
func (s CodegraphCalls) Resolve(query string) (string, error) {
	nodes, err := s.CG.Query(s.RepoPath, 1, query)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: symbol '%s' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.", query)}
	}
	return nodes[0].Name, nil
}

func (s CodegraphCalls) Edges(direction, name string, limit int, fatal bool) (*codegraph.Response, error) {
	return s.CG.Edges(direction, s.RepoPath, limit, name, fatal)
}

func (s CodegraphCalls) Symbols() ([]Node, error) { return enumerateAllSymbols(s.CG, s.RepoPath) }

func (s CodegraphCalls) Wording() Wording { return CodegraphWording }
