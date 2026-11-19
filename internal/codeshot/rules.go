package codeshot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Rules is an architecture rules file (--rules): ordered layers, forbid
// rules and allow exceptions, checked on --architecture's per-file edges.
type Rules struct {
	File   string // as given to --rules, for messages
	Layers []Layer
	Forbid []ForbidRule
	Allow  []ForbidRule
}

// Layer is one named layer; Paths are directory sides ("cmd/").
type Layer struct {
	Name  string
	Paths []string
}

type rulesFile struct {
	Version yaml.Node   `yaml:"version"`
	Layers  []layerSpec `yaml:"layers"`
	Forbid  []ruleSpec  `yaml:"forbid"`
	Allow   []ruleSpec  `yaml:"allow"`
}

type layerSpec struct {
	Name  string   `yaml:"name"`
	Paths []string `yaml:"paths"`
}

type ruleSpec struct {
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	Reason string `yaml:"reason"`
}

// ReadRules reads and validates a rules file. Every problem is reported here,
// before any graph work, naming the file and the entry.
func ReadRules(file string) (*Rules, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("could not read rules file '%s': %w", file, err)
	}
	return ParseRules(file, data)
}

// ParseRules parses rules-file YAML; file names it in errors.
func ParseRules(file string, data []byte) (*Rules, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("rules file '%s': %s", file, fmt.Sprintf(format, a...))
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var rf rulesFile
	if err := dec.Decode(&rf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, bad("is empty; it needs at least 'version: 1'")
		}
		return nil, bad("%v", err)
	}
	// One document only: rules after a stray '---' would be dropped (F5).
	// An empty trailing document drops nothing (V2); a syntax error in a
	// later one is reported as itself.
	for {
		var more yaml.Node
		err := dec.Decode(&more)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, bad("%v", err)
		}
		if !emptyDocument(&more) {
			return nil, bad("has more than one YAML document ('---'); put every rule in one")
		}
	}
	v := rf.Version
	switch {
	case v.Kind == 0 || v.Tag == "!!null":
		return nil, bad("missing 'version: 1'")
	case v.Kind == yaml.SequenceNode:
		return nil, bad("version must be the integer 1, not a list")
	case v.Kind == yaml.MappingNode:
		return nil, bad("version must be the integer 1, not a map")
	case v.Tag == "!!str":
		return nil, bad("version must be the integer 1, not the string '%s'", v.Value) // V4
	case v.Kind != yaml.ScalarNode || v.Tag != "!!int":
		return nil, bad("version must be the integer 1, got '%s'", v.Value) // F6: 1.9 is not 1
	}
	var version int
	if err := v.Decode(&version); err != nil || version != 1 {
		return nil, bad("version is %s; this codeshot reads version 1", v.Value)
	}
	r := &Rules{File: file}
	names := map[string]bool{}
	for i, l := range rf.Layers {
		where := fmt.Sprintf("layers[%d]", i)
		name := strings.TrimSpace(l.Name)
		if name == "" {
			return nil, bad("%s has no name", where)
		}
		if names[name] {
			return nil, bad("two layers are named '%s'", name)
		}
		names[name] = true
		if len(l.Paths) == 0 {
			return nil, bad("layer '%s' has no paths", name)
		}
		layer := Layer{Name: name}
		for _, p := range l.Paths {
			side, err := cleanRuleSide(p)
			if err != nil {
				return nil, bad("layer '%s': %v", name, err)
			}
			if !strings.HasSuffix(side, "/") {
				return nil, bad("layer '%s': path '%s' must be a directory ending in '/' (use a forbid rule for one file)", name, p)
			}
			layer.Paths = append(layer.Paths, side)
		}
		r.Layers = append(r.Layers, layer)
	}
	// Two layers claiming one directory (F3): decidable here for plain
	// directory paths; a pattern path is checked file by file later.
	for i, a := range r.Layers {
		for _, b := range r.Layers[i+1:] {
			for _, pa := range a.Paths {
				for _, pb := range b.Paths {
					if hasRulePattern(pa) || hasRulePattern(pb) {
						continue
					}
					outer, inner := pa, pb
					if strings.HasPrefix(pa, pb) {
						outer, inner = pb, pa
					}
					if strings.HasPrefix(inner, outer) {
						return nil, bad("layers '%s' and '%s' overlap: '%s' contains '%s'; a file can be in one layer only", a.Name, b.Name, outer, inner)
					}
				}
			}
		}
	}
	rules := func(key string, specs []ruleSpec) ([]ForbidRule, error) {
		var out []ForbidRule
		for i, s := range specs {
			from, err := cleanRuleSide(s.From)
			if err != nil {
				return nil, bad("%s[%d] from: %v", key, i, err)
			}
			to, err := cleanRuleSide(s.To)
			if err != nil {
				return nil, bad("%s[%d] to: %v", key, i, err)
			}
			raw := from + "->" + to
			rule := ForbidRule{From: from, To: to, Raw: raw, Glob: true}
			// An allow's reason is a note for readers; only a forbid rule's
			// is printed (V6).
			if key == "forbid" {
				rule.Breaks = fmt.Sprintf("forbid '%s' in %s", raw, file)
				if reason := strings.TrimSpace(s.Reason); reason != "" {
					rule.Breaks += " (" + reason + ")"
				}
			}
			out = append(out, rule)
		}
		return out, nil
	}
	var err error
	if r.Forbid, err = rules("forbid", rf.Forbid); err != nil {
		return nil, err
	}
	if r.Allow, err = rules("allow", rf.Allow); err != nil {
		return nil, err
	}
	return r, nil
}

// cleanRuleSide trims a rule side as --forbid does and checks its glob.
func cleanRuleSide(s string) (string, error) {
	side := trimRuleSide(s)
	if side == "" {
		return "", errors.New("is empty")
	}
	if strings.HasPrefix(side, "/") {
		return "", fmt.Errorf("'%s' starts with '/'; sides are relative to the repo root", s) // F7
	}
	if strings.Contains(side, "**") {
		return "", fmt.Errorf("'%s' uses '**', which isn't supported; '*' matches within one path segment, and a side ending in '/' already covers every file below it", s)
	}
	for _, seg := range strings.Split(strings.TrimSuffix(side, "/"), "/") {
		if _, err := path.Match(seg, ""); err != nil {
			return "", fmt.Errorf("'%s' has a bad pattern: %v", s, err)
		}
	}
	return side, nil
}

// RuleViolations checks the --forbid flags and, when rules is set, the rules
// file's forbid rules and layers, dropping edges an allow rule covers. With no
// rules file it is exactly ForbiddenEdges. A file in two layers is an error.
func RuleViolations(fileEdges []FileEdge, flags []ForbidRule, rules *Rules) ([]Violation, error) {
	if rules == nil {
		return ForbiddenEdges(fileEdges, flags), nil
	}
	hits := forbiddenEdges(fileEdges, append(append([]ForbidRule{}, flags...), rules.Forbid...))
	layerOf := map[string]int{}
	lookup := func(file string) (int, error) {
		if i, ok := layerOf[file]; ok {
			return i, nil
		}
		found := -1
		for i, l := range rules.Layers {
			for _, p := range l.Paths {
				if globRuleSideMatches(p, file) {
					if found >= 0 && found != i {
						return 0, fmt.Errorf("rules file '%s': '%s' is in two layers, '%s' and '%s'; a file can be in one layer only", rules.File, file, rules.Layers[found].Name, l.Name)
					}
					found = i
				}
			}
		}
		layerOf[file] = found
		return found, nil
	}
	for _, e := range fileEdges {
		from, err := lookup(e.From)
		if err != nil {
			return nil, err
		}
		to, err := lookup(e.To)
		if err != nil {
			return nil, err
		}
		// Layers run top to bottom; a layer may use itself and layers below.
		if from < 0 || to < 0 || from <= to {
			continue
		}
		lf, lt := rules.Layers[from].Name, rules.Layers[to].Name
		hits = append(hits, Violation{
			Rule: lf + "->" + lt, From: e.From, To: e.To, Weight: e.Weight, SeenBy: seenList(e.Seen, nil),
			Breaks: fmt.Sprintf("the layers in %s ('%s' may not depend on '%s', a layer above it)", rules.File, lf, lt),
			Kind:   "layers",
		})
	}
	var kept []Violation
	for _, v := range hits {
		allowed := false
		for _, a := range rules.Allow {
			if ruleMatches(a, v.From, v.To) {
				allowed = true
				break
			}
		}
		if !allowed {
			kept = append(kept, v)
		}
	}
	sortViolations(kept)
	return kept, nil
}

// sortViolations orders violations heaviest first, then by path.
func sortViolations(hits []Violation) {
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Weight != hits[b].Weight {
			return hits[a].Weight > hits[b].Weight
		}
		if hits[a].From != hits[b].From {
			return hits[a].From < hits[b].From
		}
		return hits[a].To < hits[b].To
	})
}

// emptyDocument is a YAML document with nothing in it ("---" and comments).
func emptyDocument(n *yaml.Node) bool {
	if n.Kind == 0 {
		return true
	}
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode || c.Tag != "!!null" || c.Value != "" {
				return false
			}
		}
		return true
	}
	return false
}
