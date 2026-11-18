package codeshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/codeshot/internal/codegraph"

	"github.com/inth3shadows/codeshot/internal/scipindex"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// sourceSpec is one parsed --source entry: kind codegraph, snapshot or scip,
// and the file a snapshot or SCIP index is read from. underPath marks bare
// `scip`, which reads <--path>/index.scip.
type sourceSpec struct {
	kind, file string
	underPath  bool
}

func (s sourceSpec) String() string {
	if s.kind == "codegraph" {
		return s.kind
	}
	return s.kind + ":" + s.file
}

// parseSourceSpec parses one entry: codegraph | snapshot:<file> | scip[:<file>].
func parseSourceSpec(v, repoPath string) (sourceSpec, bool) {
	switch {
	case v == "codegraph":
		return sourceSpec{kind: "codegraph"}, true
	case v == "scip":
		return sourceSpec{kind: "scip", file: filepath.Join(repoPath, "index.scip"), underPath: true}, true
	case strings.HasPrefix(v, "scip:") && v != "scip:":
		return sourceSpec{kind: "scip", file: strings.TrimPrefix(v, "scip:")}, true
	case strings.HasPrefix(v, "snapshot:") && v != "snapshot:":
		return sourceSpec{kind: "snapshot", file: strings.TrimPrefix(v, "snapshot:")}, true
	}
	return sourceSpec{}, false
}

// parseSourceList parses --source: one entry, or a comma list of two or more.
// A value that, read as one snapshot or SCIP entry, names an existing file is
// that entry, so a file whose name has a comma still reads as it did before
// lists existed. An empty or invalid entry is reported as itself, and the
// same entry twice is refused.
func parseSourceList(v, repoPath string) ([]sourceSpec, error) {
	bad := func(got string) error {
		return usageExit(fmt.Sprintf("codeshot: --source must be 'codegraph', 'snapshot:<file>' or 'scip[:<file>]' (or a comma list of them), got '%s'", got))
	}
	parts := strings.Split(v, ",")
	if len(parts) == 1 {
		s, ok := parseSourceSpec(v, repoPath)
		if !ok {
			return nil, usageExit(fmt.Sprintf("codeshot: --source must be 'codegraph', 'snapshot:<file>' or 'scip[:<file>]', got '%s'", v))
		}
		return []sourceSpec{s}, nil
	}
	// A whole value naming a file that exists is that one file, so a name
	// with a comma reads as it did before lists existed (F3).
	if s, ok := parseSourceSpec(v, repoPath); ok && s.kind != "codegraph" {
		if fi, err := os.Stat(s.file); err == nil && !fi.IsDir() {
			return []sourceSpec{s}, nil
		}
	}
	var specs []sourceSpec
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, usageExit(fmt.Sprintf("codeshot: --source '%s' has an empty entry", v))
		}
		s, ok := parseSourceSpec(part, repoPath)
		if !ok {
			if one, ok := parseSourceSpec(v, repoPath); ok && one.kind != "codegraph" {
				// It could also have been one file with a comma (V6).
				return nil, usageExit(fmt.Sprintf("codeshot: --source must be 'codegraph', 'snapshot:<file>' or 'scip[:<file>]' (or a comma list of them), got '%s' (and there is no file '%s')", part, one.file))
			}
			return nil, bad(part)
		}
		specs = append(specs, s)
	}
	seen := map[string]bool{}
	for _, s := range specs {
		k := s.String()
		if seen[k] {
			return nil, usageExit(fmt.Sprintf("codeshot: --source lists '%s' twice", k))
		}
		seen[k] = true
	}
	return specs, nil
}

// loadedSource is a --source list entry read and labeled. src is nil for
// codegraph, whose client the caller builds after its PATH checks.
type loadedSource struct {
	spec          sourceSpec
	label, phrase string
	src           ArchSource
}

// loadSources reads every snapshot and SCIP index of a --source list and
// labels each entry. Two entries with one label (two scip-go indexes, say)
// are refused: the label is how the diagram tells sources apart.
func loadSources(specs []sourceSpec, stderr io.Writer) ([]loadedSource, error) {
	var out []loadedSource
	byLabel := map[string]string{}
	for _, sp := range specs {
		l := loadedSource{spec: sp}
		switch sp.kind {
		case "codegraph":
			l.label, l.phrase = "codegraph", "codegraph"
		case "snapshot":
			snap, err := snapshot.Read(sp.file)
			if err != nil {
				return nil, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: could not read snapshot '%s': %v", sp.file, err)}
			}
			l.label, l.phrase, l.src = SnapshotMarkerSuffix(snap), snapshotPhrase(snap), SnapshotSource{Snap: snap}
			warn(stderr, labelWarning(l.label, UndrawnEdgeKindsWarning(snap)))
		case "scip":
			idx, err := scipindex.Read(sp.file)
			if err != nil {
				return nil, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: could not read SCIP index '%s': %v", sp.file, err)}
			}
			l.label, l.phrase, l.src = scipLabel(idx), scipPhrase(idx), ScipSource{Index: idx}
		}
		if prev, dup := byLabel[l.label]; dup {
			return nil, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --source entries '%s' and '%s' are both labeled '%s'; a multi-source diagram tells sources apart by label, so list only one of them", prev, sp, l.label)}
		}
		byLabel[l.label] = sp.String()
		out = append(out, l)
	}
	return out, nil
}

// joinAnd joins phrases as "a", "a and b", "a, b and c".
func joinAnd(phrases []string) string {
	if len(phrases) < 2 {
		return strings.Join(phrases, "")
	}
	return strings.Join(phrases[:len(phrases)-1], ", ") + " and " + phrases[len(phrases)-1]
}

// fileSourcePrefix is what a file source's paths need in front to match
// codegraph's in one diagram: codegraph's paths are relative to its index
// root, which FindIndex may find above --path, while a snapshot's or SCIP
// index's are taken as relative to --path (as everywhere else). "" when the
// index root is --path itself, or there is no index (codegraph then fails
// on its own).
func fileSourcePrefix(repoPath string) string {
	idx, err := codegraph.FindIndex(repoPath)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(filepath.Dir(filepath.Dir(idx)), abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel) + "/"
}

// maxRootProbes bounds the files pathsRelativeToPath looks up.
const maxRootProbes = 200

// pathsRelativeToPath tells whether a file source's paths start at --path
// (so they need prefix to match codegraph's) or already at codegraph's index
// root (one repo-wide index with --path below it, which a prefix would
// double, V1). It looks the source's files up on disk under both: more found
// under the index root means index-root-relative. A tie, including files
// found nowhere (an index from another checkout), keeps the documented rule:
// relative to --path.
func pathsRelativeToPath(src ArchSource, repoPath, prefix string) bool {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return true
	}
	root := abs
	for range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		root = filepath.Dir(root)
	}
	symbols, err := src.Symbols()
	if err != nil {
		return true // the run reports the error itself
	}
	isFile := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	seen := map[string]bool{}
	underPath, underRoot := 0, 0
	for _, s := range symbols {
		f := s.FilePath
		if f == "" || seen[f] || filepath.IsAbs(f) {
			continue
		}
		seen[f] = true
		if isFile(filepath.Join(abs, filepath.FromSlash(f))) {
			underPath++
		}
		if isFile(filepath.Join(root, filepath.FromSlash(f))) {
			underRoot++
		}
		if len(seen) >= maxRootProbes {
			break
		}
	}
	return underPath >= underRoot
}

// prefixedSource rebases a file source's edges onto the codegraph index root
// (fileSourcePrefix), so a file both sources see is one node (F1). Only for a
// source whose paths are relative to --path (pathsRelativeToPath).
type prefixedSource struct {
	Src    ArchSource
	Prefix string
}

func (p prefixedSource) Symbols() ([]Node, error)  { return p.Src.Symbols() }
func (p prefixedSource) EmptyGraphWarning() string { return p.Src.EmptyGraphWarning() }

func (p prefixedSource) BudgetWarning(budget int) string {
	if bw, ok := p.Src.(budgetWarner); ok {
		return bw.BudgetWarning(budget)
	}
	return SymbolBudgetWarning(budget)
}

func (p prefixedSource) FileEdges(symbols []Node) ([]SymbolEdge, error) {
	edges, err := p.Src.FileEdges(symbols)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolEdge, len(edges))
	for i, e := range edges {
		out[i] = SymbolEdge{FromFile: p.rebase(e.FromFile), ToFile: p.rebase(e.ToFile)}
	}
	return out, nil
}

func (p prefixedSource) rebase(f string) string {
	if f == "" {
		return ""
	}
	return path.Join(p.Prefix, f)
}

// labelError attributes a source's error to it in a multi-source run.
func labelError(label string, err error) error {
	var fatal *codegraph.FatalError
	var ex *Exit
	switch {
	case errors.As(err, &fatal):
		return &codegraph.FatalError{Msg: labelWarning(label, fatal.Msg)}
	case errors.As(err, &ex):
		c := *ex
		c.Stderr = labelWarning(label, ex.Stderr)
		return &c
	default:
		return fmt.Errorf("%s: %w", label, err)
	}
}
