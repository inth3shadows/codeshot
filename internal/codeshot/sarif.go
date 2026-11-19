package codeshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/inth3shadows/codeshot/internal/codegraph"
	"github.com/inth3shadows/codeshot/internal/snapshot"
)

// SiteLiner is a source that can say where, in a file, it saw a dependency
// on another file: the smallest 1-based line of such a call or reference,
// for every file pair it knows. Asked only when --sarif needs line numbers
// for violations, so the diagram path never pays for it.
type SiteLiner interface {
	SiteLines() (map[codegraph.FilePair]int, error)
}

func (s CodegraphSource) SiteLines() (map[codegraph.FilePair]int, error) {
	idx, err := codegraph.FindIndex(s.RepoPath)
	if err != nil {
		return nil, err
	}
	return codegraph.ReadSiteLines(idx)
}

// SiteLines: the smallest line of a drawn-kind edge (not imports, not to a
// file node) between nodes in two files; a snapshot's line is optional.
func (s SnapshotSource) SiteLines() (map[codegraph.FilePair]int, error) {
	byID := make(map[string]snapshot.Node, len(s.Snap.Nodes))
	for _, n := range s.Snap.Nodes {
		byID[n.ID] = n
	}
	out := map[codegraph.FilePair]int{}
	for _, e := range s.Snap.Edges {
		if e.Line == nil || *e.Line < 1 || e.Kind == "imports" || !slices.Contains(codegraph.CalleeEdgeKinds, e.Kind) {
			continue
		}
		src, dst := byID[e.Src], byID[e.Dst]
		if dst.Kind == "file" || src.File == "" || dst.File == "" || src.File == dst.File {
			continue
		}
		k := codegraph.FilePair{From: src.File, To: dst.File}
		if cur, ok := out[k]; !ok || *e.Line < cur {
			out[k] = *e.Line
		}
	}
	return out, nil
}

func (s ScipSource) SiteLines() (map[codegraph.FilePair]int, error) {
	out := map[codegraph.FilePair]int{}
	for k, line := range s.Index.RefLines() {
		out[codegraph.FilePair{From: k[0], To: k[1]}] = line
	}
	return out, nil
}

// SiteLines rebases the inner source's pairs the way FileEdges does.
func (p prefixedSource) SiteLines() (map[codegraph.FilePair]int, error) {
	sl, ok := p.Src.(SiteLiner)
	if !ok {
		return nil, nil
	}
	lines, err := sl.SiteLines()
	if err != nil {
		return nil, err
	}
	out := make(map[codegraph.FilePair]int, len(lines))
	for k, line := range lines {
		r := codegraph.FilePair{From: p.rebase(k.From), To: p.rebase(k.To)}
		if cur, ok := out[r]; !ok || line < cur {
			out[r] = line
		}
	}
	return out, nil
}

// fillSiteLines sets each violation's Line to the smallest line any source
// gives its file pair; 0 when none knows. A source that can't answer costs
// its lines only, with a warning: the violations stand either way.
func fillSiteLines(vs []Violation, srcs []LabeledSource, stderr io.Writer) {
	if len(vs) == 0 {
		return
	}
	best := map[codegraph.FilePair]int{}
	for _, s := range srcs {
		sl, ok := s.Src.(SiteLiner)
		if !ok {
			continue
		}
		lines, err := sl.SiteLines()
		if err != nil {
			warn(stderr, labelWarning(s.Label, fmt.Sprintf("codeshot: --sarif: could not read line numbers (%v); its findings point at line 1 of the file.", err)))
			continue
		}
		for k, line := range lines {
			if cur, ok := best[k]; !ok || line < cur {
				best[k] = line
			}
		}
	}
	for i := range vs {
		vs[i].Line = best[codegraph.FilePair{From: vs[i].From, To: vs[i].To}]
	}
}

// SARIF rule ids, one per kind of rule.
const (
	sarifForbidRule = "codeshot/forbid"
	sarifLayersRule = "codeshot/layers"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	ShortDescription sarifText    `json:"shortDescription"`
	FullDescription  sarifText    `json:"fullDescription"`
	HelpURI          string       `json:"helpUri"`
	DefaultConfig    sarifDefault `json:"defaultConfiguration"`
}

type sarifDefault struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	RelatedLocations    []sarifLocation   `json:"relatedLocations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
}

type sarifLocation struct {
	ID               *int          `json:"id,omitempty"`
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
	Message          *sarifText    `json:"message,omitempty"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// BuildSARIF encodes violations as a SARIF 2.1.0 log: one result per
// violation at the depending file (the first call-site line when known,
// else line 1), the depended-on file as a related location, and a
// fingerprint from the rule and the two files, so an alert survives the
// line moving.
func BuildSARIF(vs []Violation, version string) ([]byte, error) {
	rules := []sarifRule{
		{
			ID: sarifForbidRule, Name: "ForbiddenDependency",
			ShortDescription: sarifText{"A file depends on a file a forbid rule keeps it from."},
			FullDescription:  sarifText{"A --forbid flag or a forbid rule in the --rules file says files matching FROM may not depend on files matching TO, and codeshot's graph has such an edge."},
			HelpURI:          "https://github.com/inth3shadows/codeshot/blob/master/USAGE.md#enforcing-architecture-rules",
			DefaultConfig:    sarifDefault{"error"},
		},
		{
			ID: sarifLayersRule, Name: "LayerViolation",
			ShortDescription: sarifText{"A file in a lower layer depends on a higher layer."},
			FullDescription:  sarifText{"The --rules file orders layers top to bottom; a layer may use itself and the layers below it, and this edge goes from a lower layer to a higher one."},
			HelpURI:          "https://github.com/inth3shadows/codeshot/blob/master/USAGE.md#a-rules-file",
			DefaultConfig:    sarifDefault{"error"},
		},
	}
	results := make([]sarifResult, 0, len(vs))
	for _, v := range vs {
		id, idx := sarifForbidRule, 0
		if v.Kind == "layers" {
			id, idx = sarifLayersRule, 1
		}
		line := v.Line
		if line < 1 {
			line = 1
		}
		sum := sha256.Sum256([]byte(id + "\x00" + v.Rule + "\x00" + v.From + "\x00" + v.To))
		related := 1
		results = append(results, sarifResult{
			RuleID: id, RuleIndex: idx, Level: "error",
			Message: sarifText{violationText(v)},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysical{
				ArtifactLocation: sarifArtifact{URI: v.From},
				Region:           &sarifRegion{StartLine: line},
			}}},
			RelatedLocations: []sarifLocation{{
				ID:               &related,
				PhysicalLocation: sarifPhysical{ArtifactLocation: sarifArtifact{URI: v.To}},
				Message:          &sarifText{"the file it depends on"},
			}},
			PartialFingerprints: map[string]string{"codeshotEdge/v1": hex.EncodeToString(sum[:16])},
		})
	}
	log := sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool:    sarifTool{Driver: sarifDriver{Name: "codeshot", Version: version, InformationURI: "https://github.com/inth3shadows/codeshot", Rules: rules}},
			Results: results,
		}},
	}
	out, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// WriteSARIF writes BuildSARIF's log to file.
func WriteSARIF(file string, vs []Violation, version string) error {
	data, err := BuildSARIF(vs, version)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return fmt.Errorf("could not write --sarif file '%s': %w", file, err)
	}
	return nil
}
