// Package snapshot reads graph-assay's tool-neutral code-graph snapshot
// (schema graph-assay/snapshot@1, defined by schema/snapshot.v1.json in
// github.com/inth3shadows/graph-assay). It is codeshot's shared graph format:
// any tool graph-assay has an adapter for can feed codeshot through it, and the
// two projects are coupled by this schema, never by code.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
)

// Schema is the only schema id this reader accepts.
const Schema = "graph-assay/snapshot@1"

type Snapshot struct {
	Schema       string       `json:"schema"`
	Subject      Subject      `json:"subject"`
	Capabilities Capabilities `json:"capabilities"`
	Files        []File       `json:"files"`
	Nodes        []Node       `json:"nodes"`
	Edges        []Edge       `json:"edges"`
	Unresolved   []Unresolved `json:"unresolved"`
}

type Subject struct {
	Name          string         `json:"name"`
	Version       *string        `json:"version,omitempty"`
	AdapterSha256 string         `json:"adapterSha256,omitempty"`
	Config        map[string]any `json:"config,omitempty"`
}

type Capabilities struct {
	SiteLines      bool     `json:"siteLines"`
	SiteColumns    bool     `json:"siteColumns"`
	ColumnEncoding *string  `json:"columnEncoding,omitempty"`
	Unresolved     bool     `json:"unresolved"`
	Provenance     bool     `json:"provenance"`
	EdgeKinds      []string `json:"edgeKinds,omitempty"`
}

type File struct {
	Path     string  `json:"path"`
	Language *string `json:"language,omitempty"`
}

// Node is one definition (or file, import, ...) in the subject's own kinds.
// Start and End are 1-based lines.
type Node struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	QName    *string `json:"qname,omitempty"`
	File     string  `json:"file"`
	Language *string `json:"language,omitempty"`
	Start    int     `json:"start"`
	End      int     `json:"end"`
}

// Edge points from node Src to node Dst. Provenance nil means extracted or
// resolved; "heuristic" means guessed or synthesized.
type Edge struct {
	Kind       string  `json:"kind"`
	Src        string  `json:"src"`
	Dst        string  `json:"dst"`
	Line       *int    `json:"line,omitempty"`
	Col        *int    `json:"col,omitempty"`
	Ref        *string `json:"ref,omitempty"`
	ResolvedBy *string `json:"resolvedBy,omitempty"`
	Provenance *string `json:"provenance,omitempty"`
}

type Unresolved struct {
	Src  *string `json:"src,omitempty"`
	File string  `json:"file"`
	Line int     `json:"line"`
	Col  *int    `json:"col,omitempty"`
	Name string  `json:"name"`
	Kind string  `json:"kind"`
}

// Read loads and validates a snapshot file.
func Read(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

var requiredTop = []string{"schema", "subject", "capabilities", "files", "nodes", "edges", "unresolved"}

// Parse decodes and validates a snapshot. It checks what codeshot relies on:
// the schema id, the required top-level keys and node fields, unique node ids,
// and that every edge's endpoints are nodes in the snapshot. A snapshot that
// fails is rejected whole; drawing from a partly valid one would mislead.
func Parse(data []byte) (*Snapshot, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	for _, k := range requiredTop {
		if _, ok := top[k]; !ok {
			return nil, fmt.Errorf("missing required key %q", k)
		}
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Schema != Schema {
		return nil, fmt.Errorf("schema is %q; codeshot reads %q", s.Schema, Schema)
	}
	var rawNodes []map[string]json.RawMessage
	if err := json.Unmarshal(top["nodes"], &rawNodes); err != nil {
		return nil, fmt.Errorf("nodes: %w", err)
	}
	for i, n := range rawNodes {
		for _, k := range []string{"id", "kind", "name", "file", "start", "end"} {
			if _, ok := n[k]; !ok {
				return nil, fmt.Errorf("nodes[%d]: missing required key %q", i, k)
			}
		}
	}
	ids := make(map[string]bool, len(s.Nodes))
	for i, n := range s.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("nodes[%d]: empty id", i)
		}
		if ids[n.ID] {
			return nil, fmt.Errorf("nodes[%d]: duplicate id %q", i, n.ID)
		}
		ids[n.ID] = true
	}
	for i, e := range s.Edges {
		if !ids[e.Src] {
			return nil, fmt.Errorf("edges[%d]: src %q is not a node", i, e.Src)
		}
		if !ids[e.Dst] {
			return nil, fmt.Errorf("edges[%d]: dst %q is not a node", i, e.Dst)
		}
	}
	return &s, nil
}
