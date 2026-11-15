// Package scipindex reads a SCIP index (https://github.com/scip-code/scip),
// the format scip-go, scip-typescript, scip-python, scip-java and other
// compiler-backed indexers write, into the per-file definitions and
// references codeshot's --architecture needs.
package scipindex

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Index is what codeshot keeps of a SCIP index: the indexer's name and,
// per document, the project symbols it defines and references.
type Index struct {
	Tool string
	Docs []Doc
}

// Doc is one indexed source file. Path is relative to the project root.
type Doc struct {
	Path string
	Defs []Def
	// Refs are the distinct global symbols the file refers to other than
	// at a definition or an import, in first-occurrence order. They may
	// include the file's own definitions; FileEdges skips those.
	Refs []string
}

// Def is a symbol a document defines.
type Def struct {
	Symbol string
	Name   string
	Kind   string
}

// FileEdge is one file depending on another through Symbol.
type FileEdge struct {
	From, To, Symbol string
}

// Read parses the SCIP index at file.
func Read(file string) (*Index, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes a SCIP index and keeps the definitions and references of
// every document inside the project root. It rejects an index with no
// documents, and an empty, unclean or duplicate document path, since those
// name diagram nodes.
func Parse(data []byte) (*Index, error) {
	var raw scip.Index
	if err := proto.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("not a SCIP index: %w", err)
	}
	if len(raw.GetDocuments()) == 0 {
		return nil, fmt.Errorf("it has no documents")
	}
	out := &Index{Tool: raw.GetMetadata().GetToolInfo().GetName()}
	seenPath := map[string]bool{}
	for i, d := range raw.GetDocuments() {
		p := d.GetRelativePath()
		if p == "" || path.Clean(p) != p {
			return nil, fmt.Errorf("documents[%d]: relative_path %q is not a clean path", i, p)
		}
		if path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
			// Not a project file: scip-go indexes cgo output in the Go
			// build cache as "../../.cache/go-build/...".
			continue
		}
		if seenPath[p] {
			return nil, fmt.Errorf("documents[%d]: duplicate relative_path %q", i, p)
		}
		seenPath[p] = true
		out.Docs = append(out.Docs, readDoc(p, d))
	}
	return out, nil
}

func readDoc(p string, d *scip.Document) Doc {
	kinds := map[string]string{}
	for _, si := range d.GetSymbols() {
		if k := si.GetKind(); k != scip.SymbolInformation_UnspecifiedKind {
			kinds[si.GetSymbol()] = strings.ToLower(k.String())
		}
	}
	doc := Doc{Path: p}
	defined, referenced := map[string]bool{}, map[string]bool{}
	for _, o := range d.GetOccurrences() {
		sym := o.GetSymbol()
		if sym == "" || scip.IsLocalSymbol(sym) {
			continue
		}
		roles := o.GetSymbolRoles()
		if roles&int32(scip.SymbolRole_Definition) != 0 {
			if !defined[sym] {
				defined[sym] = true
				name, kind := describe(sym)
				if k, ok := kinds[sym]; ok {
					kind = k
				}
				doc.Defs = append(doc.Defs, Def{Symbol: sym, Name: name, Kind: kind})
			}
			continue
		}
		if roles&int32(scip.SymbolRole_Import) != 0 || referenced[sym] {
			continue
		}
		referenced[sym] = true
		doc.Refs = append(doc.Refs, sym)
	}
	return doc
}

// describe is a symbol's display name (its last descriptor's name) and a
// kind from that descriptor's suffix. An unparsable symbol keeps its text.
func describe(sym string) (name, kind string) {
	s, err := scip.ParseSymbol(sym)
	if err != nil || len(s.GetDescriptors()) == 0 {
		return sym, "symbol"
	}
	last := s.GetDescriptors()[len(s.GetDescriptors())-1]
	return last.GetName(), strings.ToLower(last.GetSuffix().String())
}

// isNamespace reports whether sym names a package or namespace. A reference
// to one is import-shaped: Go's package clause and import specs refer to it.
func isNamespace(sym string) bool {
	s, err := scip.ParseSymbol(sym)
	if err != nil || len(s.GetDescriptors()) == 0 {
		return false
	}
	return s.GetDescriptors()[len(s.GetDescriptors())-1].GetSuffix() == scip.Descriptor_Namespace
}

// FileEdges returns, for each document in from (all documents when from is
// nil), an edge to every other document defining a symbol it refers to.
// References to packages/namespaces are left out: codegraph draws an import
// only alongside a call, and a SCIP package reference is that import.
// Edges come in document order, then reference order, then target path.
func (x *Index) FileEdges(from map[string]bool) []FileEdge {
	definedIn := map[string][]string{}
	for _, d := range x.Docs {
		for _, def := range d.Defs {
			definedIn[def.Symbol] = append(definedIn[def.Symbol], d.Path)
		}
	}
	for _, files := range definedIn {
		sort.Strings(files)
	}
	ns := map[string]bool{}
	var out []FileEdge
	for _, d := range x.Docs {
		if from != nil && !from[d.Path] {
			continue
		}
		for _, sym := range d.Refs {
			files := definedIn[sym]
			if len(files) == 0 {
				continue // defined outside the project
			}
			isNS, ok := ns[sym]
			if !ok {
				isNS = isNamespace(sym)
				ns[sym] = isNS
			}
			if isNS {
				continue
			}
			for _, f := range files {
				if f != d.Path {
					out = append(out, FileEdge{From: d.Path, To: f, Symbol: sym})
				}
			}
		}
	}
	return out
}
