package snapshot

import (
	"strings"
	"testing"
)

const valid = `{"schema":"graph-assay/snapshot@1","subject":{"name":"t"},
 "capabilities":{"siteLines":true,"siteColumns":false,"unresolved":false,"provenance":false},
 "files":[{"path":"a.go"}],
 "nodes":[{"id":"1","kind":"function","name":"A","qname":"p.A","file":"a.go","start":1,"end":3},
          {"id":"2","kind":"function","name":"B","qname":null,"file":"b.go","start":1,"end":2}],
 "edges":[{"kind":"calls","src":"1","dst":"2","line":2}],
 "unresolved":[]}`

func TestParseReadsAValidSnapshot(t *testing.T) {
	s, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 2 || len(s.Edges) != 1 || *s.Nodes[0].QName != "p.A" || s.Nodes[1].QName != nil || *s.Edges[0].Line != 2 {
		t.Errorf("decoded %+v", s)
	}
}

func TestParseRejectsWhatCodeshotCannotTrust(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"other schema":     {`snapshot@1`, `snapshot@2`, `codeshot reads "graph-assay/snapshot@1"`},
		"missing key":      {`"unresolved":[]`, `"x":[]`, `"unresolved" is missing or null`},
		"node missing end": {`"start":1,"end":2}`, `"start":1}`, `nodes[1]: "end" is missing or null`},
		"duplicate id":     {`"id":"2"`, `"id":"1"`, `duplicate id "1"`},
		"empty id":         {`"id":"2"`, `"id":""`, `nodes[1]: empty id`},
		"dangling dst":     {`"dst":"2"`, `"dst":"9"`, `edges[0]: dst "9" is not a node`},
		"dangling src":     {`"src":"1"`, `"src":"9"`, `edges[0]: src "9" is not a node`},
		"not an object":    {valid, `[]`, `not a JSON object`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(valid, c.old, c.new, 1)
			if in == valid {
				t.Fatalf("replacement %q did not apply", c.old)
			}
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// F2: a required field that is null or absent rejects the whole snapshot;
// decoded to a zero value it would silently drop an edge.
func TestParseRejectsNullOrAbsentRequiredFields(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"edge kind null":      {`"kind":"calls"`, `"kind":null`, `edges[0]: "kind" is missing or null`},
		"edge kind absent":    {`"kind":"calls",`, ``, `edges[0]: "kind" is missing or null`},
		"node file null":      {`"file":"b.go"`, `"file":null`, `nodes[1]: "file" is missing or null`},
		"node name null":      {`"name":"B"`, `"name":null`, `nodes[1]: "name" is missing or null`},
		"node kind null":      {`"kind":"function","name":"B"`, `"kind":null,"name":"B"`, `nodes[1]: "kind" is missing or null`},
		"node start null":     {`"start":1,"end":2`, `"start":null,"end":2`, `nodes[1]: "start" is missing or null`},
		"node start negative": {`"start":1,"end":2`, `"start":-1,"end":2`, `nodes[1]: "start" is negative`},
		"subject name absent": {`"subject":{"name":"t"}`, `"subject":{}`, `subject: "name" is missing or null`},
		"capabilities empty":  {`"siteLines":true,"siteColumns":false,"unresolved":false,"provenance":false`, ``, `capabilities: "siteLines" is missing or null`},
		"file path absent":    {`{"path":"a.go"}`, `{"nopath":1}`, `files[0]: "path" is missing or null`},
		"edges null":          {`"edges":[{"kind":"calls","src":"1","dst":"2","line":2}]`, `"edges":null`, `"edges" is missing or null`},
		"nodes null":          {`"nodes":[`, `"nodes":null,"x":[`, `"nodes" is missing or null`},
		"unresolved bad":      {`"unresolved":[]`, `"unresolved":[{"file":"a.go"}]`, `unresolved[0]: "line" is missing or null`},
		"edge src null":       {`"src":"1"`, `"src":null`, `edges[0]: "src" is missing or null`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(valid, c.old, c.new, 1)
			if in == valid {
				t.Fatalf("replacement %q did not apply", c.old)
			}
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// F8: the schema id is checked before anything else is decoded, so a newer
// schema whose fields changed type still gets the schema message.
func TestParseNamesTheSchemaBeforeDecodingFieldsItDoesNotKnow(t *testing.T) {
	in := strings.Replace(strings.Replace(valid, "snapshot@1", "snapshot@2", 1), `"start":1,"end":3`, `"start":{"line":1},"end":3`, 1)
	_, err := Parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), `schema is "graph-assay/snapshot@2"`) {
		t.Errorf("err = %v, want the schema message", err)
	}
}
