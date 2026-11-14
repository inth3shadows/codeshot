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
		"missing key":      {`"unresolved":[]`, `"x":[]`, `missing required key "unresolved"`},
		"node missing end": {`"start":1,"end":2}`, `"start":1}`, `nodes[1]: missing required key "end"`},
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
