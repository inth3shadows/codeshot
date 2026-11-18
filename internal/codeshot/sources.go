package codeshot

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

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
// A value that isn't a valid list but is a valid single entry is that entry,
// so a snapshot or SCIP file whose name has a comma still reads as it did
// before lists existed. The same entry twice is refused.
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
	var specs []sourceSpec
	badPart, invalid := "", false
	for _, part := range parts {
		s, ok := parseSourceSpec(strings.TrimSpace(part), repoPath)
		if !ok {
			badPart, invalid = part, true
			break
		}
		specs = append(specs, s)
	}
	if invalid {
		if s, ok := parseSourceSpec(v, repoPath); ok {
			return []sourceSpec{s}, nil
		}
		return nil, bad(badPart)
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
