package scipindex

import (
	"path"
	"sort"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
)

// Def is one definition symbol mode can draw: a function, method or type,
// or a function-valued variable.
type Def struct {
	Symbol string
	Path   string
	Name   string
	// QName is the descriptor path without packages and namespaces:
	// "Client.Edges" for the method Edges on the type Client.
	QName string
	// Kind is the indexer's kind lowercased ("struct", "interface",
	// "method"), else function, method or type by the descriptor.
	Kind string
	// Line is the 1-based line of the definition's name; End is the last
	// line of its enclosing range, or Line when the indexer gave none.
	Line, End int
	// Container reports whether references inside the definition's
	// enclosing range are drawn from it: a function, method or type, or
	// outside Go a function-valued variable.
	Container bool
}

// Ref is one definition or file referring to a symbol. Caller indexes
// CallGraph.Defs, or is -1 when the reference sits outside every ranged
// definition (module-level code): then the file Path is the caller.
type Ref struct {
	Caller int
	Path   string
	Callee string
}

// CallGraph is the definitions and references Calls derives. Unranged
// counts the functions, methods and types with no enclosing range, whose
// references are drawn from their file instead.
type CallGraph struct {
	Defs     []Def
	Refs     []Ref
	Unranged int
}

// pos is a 0-based (line, character) position in a document.
type pos struct{ line, char int32 }

func (a pos) before(b pos) bool {
	return a.line < b.line || a.line == b.line && a.char < b.char
}

// Calls derives symbol mode's graph. A reference's caller is the innermost
// definition whose enclosing range holds its position, starting no earlier
// than the definition's own name line (the range may begin at a doc comment
// or a decorator, which run in the scope around it), else the file.
// Positions are compared by line and character, so two definitions on one
// line, or code after a closing brace, are told apart. References to
// locals, packages and namespaces, imports, and symbols the project doesn't
// define are left out, as is a definition's reference to itself. A symbol
// defined more than once in a file (TypeScript overloads, or scip-python
// giving nested functions one symbol) is one Def there, and each of its
// ranges draws from it. Each (caller, callee) pair appears once, sorted.
func (x *Index) Calls() *CallGraph {
	g := &CallGraph{}
	if x.raw == nil {
		return g
	}
	defined := map[string]bool{}
	type container struct {
		def  int
		s, e pos
	}
	inFile := map[string][]container{}
	type key struct{ sym, path string }
	defAt := map[key]int{}
	var unranged []bool          // per Def: contains, but none of its definitions is ranged
	for _, doc := range x.Docs { // sorted by path
		p, d := doc.Path, x.raw[doc.Path]
		kinds := map[string]scip.SymbolInformation_Kind{}
		for _, si := range d.GetSymbols() {
			kinds[si.GetSymbol()] = si.GetKind()
		}
		golang := strings.EqualFold(d.GetLanguage(), "go") || path.Ext(p) == ".go"
		for _, o := range d.GetOccurrences() {
			sym := o.GetSymbol()
			if o.GetSymbolRoles()&int32(scip.SymbolRole_Definition) == 0 || sym == "" || scip.IsLocalSymbol(sym) {
				continue
			}
			r, ok := o.SourceRange()
			if !ok {
				continue
			}
			er, ranged := o.EnclosingSourceRange()
			def, contains, ok := newDef(sym, kinds[sym], ranged, golang)
			if !ok {
				continue
			}
			line := int(r.Start.Line) + 1
			i, seenDef := defAt[key{sym, p}]
			if !seenDef {
				def.Path, def.Line, def.End = p, line, line
				i = len(g.Defs)
				defAt[key{sym, p}] = i
				defined[sym] = true
				g.Defs = append(g.Defs, def)
				unranged = append(unranged, contains)
			}
			d := &g.Defs[i]
			d.Line = min(d.Line, line)
			if !contains || !ranged {
				d.End = max(d.End, line)
				continue
			}
			d.Container, unranged[i] = true, false
			d.End = max(d.End, int(er.End.Line)+1)
			s := pos{er.Start.Line, er.Start.Character}
			if name := (pos{r.Start.Line, r.Start.Character}); name.before(s) {
				s = name
			}
			if s.line < r.Start.Line {
				s = pos{r.Start.Line, 0}
			}
			inFile[p] = append(inFile[p], container{i, s, pos{er.End.Line, er.End.Character}})
		}
	}
	for _, u := range unranged {
		if u {
			g.Unranged++
		}
	}
	// callerAt picks, among the ranges holding at, the one starting last
	// (and of two starting together the one ending first): with nested
	// ranges that is the innermost.
	callerAt := func(p string, at pos) int {
		best := -1
		var bs, be pos
		for _, c := range inFile[p] {
			if at.before(c.s) || !at.before(c.e) {
				continue
			}
			if best < 0 || bs.before(c.s) || bs == c.s && c.e.before(be) {
				best, bs, be = c.def, c.s, c.e
			}
		}
		return best
	}
	seen := map[Ref]bool{}
	for _, doc := range x.Docs {
		p := doc.Path
		for _, o := range x.raw[p].GetOccurrences() {
			sym := o.GetSymbol()
			roles := o.GetSymbolRoles()
			if !defined[sym] || roles&int32(scip.SymbolRole_Definition|scip.SymbolRole_Import) != 0 {
				continue
			}
			r, ok := o.SourceRange()
			if !ok {
				continue
			}
			ref := Ref{Caller: callerAt(p, pos{r.Start.Line, r.Start.Character}), Path: p, Callee: sym}
			if ref.Caller >= 0 && g.Defs[ref.Caller].Symbol == sym || seen[ref] {
				continue
			}
			seen[ref] = true
			g.Refs = append(g.Refs, ref)
		}
	}
	sort.Slice(g.Refs, func(a, b int) bool {
		ra, rb := g.Refs[a], g.Refs[b]
		if ra.Path != rb.Path {
			return ra.Path < rb.Path
		}
		if ra.Caller != rb.Caller {
			return ra.Caller < rb.Caller
		}
		return ra.Callee < rb.Callee
	})
	return g
}

// newDef reads a definition's name, qualified name and kind from its
// symbol, and whether it can be a caller (a container), or reports false
// for one symbol mode doesn't draw: a package, parameter, type parameter,
// field or member variable. Functions, methods and types count and contain.
// A term counts when the indexer calls it a function or method, when it is
// a top-level constant or variable (codegraph draws references to those),
// or, outside Go, when the indexer gave it an enclosing range:
// scip-typescript ranges only function-valued variables, which contain,
// while scip-go ranges every field and variable, which don't.
func newDef(sym string, kind scip.SymbolInformation_Kind, ranged, golang bool) (Def, bool, bool) {
	s, err := scip.ParseSymbol(sym)
	if err != nil || len(s.GetDescriptors()) == 0 {
		return Def{}, false, false
	}
	ds := s.GetDescriptors()
	last := ds[len(ds)-1]
	topLevel := len(ds) == 1 || ds[len(ds)-2].GetSuffix() == scip.Descriptor_Namespace
	callable := kind == scip.SymbolInformation_Function || kind == scip.SymbolInformation_Method || kind == scip.SymbolInformation_MethodSpecification
	container := true
	switch last.GetSuffix() {
	case scip.Descriptor_Method, scip.Descriptor_Type:
	case scip.Descriptor_Term, scip.Descriptor_Meta:
		fnValued := ranged && !golang
		value := topLevel && (kind == scip.SymbolInformation_Constant || kind == scip.SymbolInformation_Variable)
		if !callable && !fnValued && !value {
			return Def{}, false, false
		}
		container = callable || fnValued
	default:
		return Def{}, false, false
	}
	var names []string
	parentIsType := false
	for i, d := range ds {
		switch d.GetSuffix() {
		case scip.Descriptor_Namespace:
			continue
		}
		names = append(names, d.GetName())
		if i == len(ds)-2 && d.GetSuffix() == scip.Descriptor_Type {
			parentIsType = true
		}
	}
	def := Def{Symbol: sym, Name: last.GetName(), QName: strings.Join(names, ".")}
	switch {
	case kind != scip.SymbolInformation_UnspecifiedKind:
		def.Kind = strings.ToLower(kind.String())
	case last.GetSuffix() == scip.Descriptor_Type:
		def.Kind = "type"
	case parentIsType:
		def.Kind = "method"
	default:
		def.Kind = "function"
	}
	return def, container, def.Name != ""
}
