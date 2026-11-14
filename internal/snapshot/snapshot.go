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

// The wire types decode every required field through a pointer, so a field
// that is absent and one that is null both arrive as nil and are rejected:
// decoded straight into the public types they would become "" or 0 and, for
// an edge kind or a node file, silently drop an edge from the diagram.
type wireSnapshot struct {
	Subject      *wireSubject      `json:"subject"`
	Capabilities *wireCapabilities `json:"capabilities"`
	Files        *[]wireFile       `json:"files"`
	Nodes        *[]wireNode       `json:"nodes"`
	Edges        *[]wireEdge       `json:"edges"`
	Unresolved   *[]wireUnresolved `json:"unresolved"`
}

type wireSubject struct {
	Name          *string        `json:"name"`
	Version       *string        `json:"version"`
	AdapterSha256 string         `json:"adapterSha256"`
	Config        map[string]any `json:"config"`
}

type wireCapabilities struct {
	SiteLines      *bool    `json:"siteLines"`
	SiteColumns    *bool    `json:"siteColumns"`
	ColumnEncoding *string  `json:"columnEncoding"`
	Unresolved     *bool    `json:"unresolved"`
	Provenance     *bool    `json:"provenance"`
	EdgeKinds      []string `json:"edgeKinds"`
}

type wireFile struct {
	Path     *string `json:"path"`
	Language *string `json:"language"`
}

type wireNode struct {
	ID, Kind, Name, File *string
	QName                *string `json:"qname"`
	Language             *string `json:"language"`
	Start, End           *int
}

type wireEdge struct {
	Kind, Src, Dst *string
	Line, Col      *int
	Ref            *string `json:"ref"`
	ResolvedBy     *string `json:"resolvedBy"`
	Provenance     *string `json:"provenance"`
}

type wireUnresolved struct {
	Src              *string `json:"src"`
	File, Name, Kind *string
	Line             *int
	Col              *int `json:"col"`
}

// field is one required value: its JSON name and whether it was present.
type field struct {
	name string
	ok   bool
}

func need(where string, fields ...field) error {
	for _, f := range fields {
		if !f.ok {
			return fmt.Errorf("%s%q is missing or null", where, f.name)
		}
	}
	return nil
}

func at(list string, i int) string { return fmt.Sprintf("%s[%d]: ", list, i) }

// Parse decodes and validates a snapshot. The schema id is read first, so a
// newer schema is named even when its fields no longer decode as v1. Then it
// checks everything codeshot relies on: every required key and field present
// and non-null, non-negative lines, unique non-empty node ids, and edges whose
// endpoints are nodes in the snapshot. A snapshot that fails is rejected
// whole; drawing from a partly valid one would mislead.
func Parse(data []byte) (*Snapshot, error) {
	var head struct {
		Schema *string `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if head.Schema == nil {
		return nil, fmt.Errorf("%q is missing or null", "schema")
	}
	if *head.Schema != Schema {
		return nil, fmt.Errorf("schema is %q; codeshot reads %q", *head.Schema, Schema)
	}
	var w wireSnapshot
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	if err := need("",
		field{"subject", w.Subject != nil}, field{"capabilities", w.Capabilities != nil},
		field{"files", w.Files != nil}, field{"nodes", w.Nodes != nil},
		field{"edges", w.Edges != nil}, field{"unresolved", w.Unresolved != nil}); err != nil {
		return nil, err
	}
	s := &Snapshot{Schema: Schema}

	if err := need("subject: ", field{"name", w.Subject.Name != nil}); err != nil {
		return nil, err
	}
	s.Subject = Subject{Name: *w.Subject.Name, Version: w.Subject.Version, AdapterSha256: w.Subject.AdapterSha256, Config: w.Subject.Config}

	c := w.Capabilities
	if err := need("capabilities: ", field{"siteLines", c.SiteLines != nil}, field{"siteColumns", c.SiteColumns != nil},
		field{"unresolved", c.Unresolved != nil}, field{"provenance", c.Provenance != nil}); err != nil {
		return nil, err
	}
	s.Capabilities = Capabilities{SiteLines: *c.SiteLines, SiteColumns: *c.SiteColumns, ColumnEncoding: c.ColumnEncoding,
		Unresolved: *c.Unresolved, Provenance: *c.Provenance, EdgeKinds: c.EdgeKinds}

	s.Files = make([]File, 0, len(*w.Files))
	for i, f := range *w.Files {
		if err := need(at("files", i), field{"path", f.Path != nil}); err != nil {
			return nil, err
		}
		s.Files = append(s.Files, File{Path: *f.Path, Language: f.Language})
	}

	ids := make(map[string]bool, len(*w.Nodes))
	s.Nodes = make([]Node, 0, len(*w.Nodes))
	for i, n := range *w.Nodes {
		where := at("nodes", i)
		if err := need(where, field{"id", n.ID != nil}, field{"kind", n.Kind != nil}, field{"name", n.Name != nil},
			field{"file", n.File != nil}, field{"start", n.Start != nil}, field{"end", n.End != nil}); err != nil {
			return nil, err
		}
		switch {
		case *n.ID == "":
			return nil, fmt.Errorf("%sempty id", where)
		case ids[*n.ID]:
			return nil, fmt.Errorf("%sduplicate id %q", where, *n.ID)
		case *n.Start < 0:
			return nil, fmt.Errorf("%s%q is negative", where, "start")
		case *n.End < 0:
			return nil, fmt.Errorf("%s%q is negative", where, "end")
		}
		ids[*n.ID] = true
		s.Nodes = append(s.Nodes, Node{ID: *n.ID, Kind: *n.Kind, Name: *n.Name, QName: n.QName, File: *n.File,
			Language: n.Language, Start: *n.Start, End: *n.End})
	}

	s.Edges = make([]Edge, 0, len(*w.Edges))
	for i, e := range *w.Edges {
		where := at("edges", i)
		if err := need(where, field{"kind", e.Kind != nil}, field{"src", e.Src != nil}, field{"dst", e.Dst != nil}); err != nil {
			return nil, err
		}
		if !ids[*e.Src] {
			return nil, fmt.Errorf("%ssrc %q is not a node", where, *e.Src)
		}
		if !ids[*e.Dst] {
			return nil, fmt.Errorf("%sdst %q is not a node", where, *e.Dst)
		}
		s.Edges = append(s.Edges, Edge{Kind: *e.Kind, Src: *e.Src, Dst: *e.Dst, Line: e.Line, Col: e.Col,
			Ref: e.Ref, ResolvedBy: e.ResolvedBy, Provenance: e.Provenance})
	}

	s.Unresolved = make([]Unresolved, 0, len(*w.Unresolved))
	for i, u := range *w.Unresolved {
		if err := need(at("unresolved", i), field{"file", u.File != nil}, field{"line", u.Line != nil},
			field{"name", u.Name != nil}, field{"kind", u.Kind != nil}); err != nil {
			return nil, err
		}
		s.Unresolved = append(s.Unresolved, Unresolved{Src: u.Src, File: *u.File, Line: *u.Line, Col: u.Col, Name: *u.Name, Kind: *u.Kind})
	}
	return s, nil
}
