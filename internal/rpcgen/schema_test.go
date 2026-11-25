package rpcgen_test

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

// schemaFixture is a contract-free package of wire types: a record with a
// json.Optional field, a union of a field-less and a field-holding
// variant, a slice and an integer.
const schemaFixture = `import json

type Size = Small{} | Large{Inches int}

type Box {
	Label string,
	Note json.Optional[string],
	Count int,
	Size Size,
	Tags []string,
}
`

// schemaProbe holds, over the generated schema, what the generated law
// cannot see: JSON no Box encodes to does not fit.
const schemaProbe = `import json
import jsonschema
import loop/shapes/shapesschema

func value(text string) json.Value {
	json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		json.Null
	}
}

func quoted(s string) string { json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, s) }

func box(label string, count string, size string) json.Value {
	value("{\"label\":" + quoted(label) + ",\"count\":" + count + ",\"size\":" + size + ",\"tags\":[]}")
}

// OnlyBoxesFit: a box with no note fits; one whose size's tag no variant
// has, whose count has a fraction, or that lacks its label does not.
law OnlyBoxesFit(label string, n int) {
	let s = shapesschema.Box()
	jsonschema.Conforms(s, box(label, "\(n)", "{\"_tag\":\"Large\",\"inches\":\(n)}")) && !jsonschema.Conforms(s, box(label, "\(n)", "{\"_tag\":\"Medium\"}")) && !jsonschema.Conforms(s, box(label, "\(n).5", "{\"_tag\":\"Small\"}")) && !jsonschema.Conforms(s, value("{\"count\":1,\"size\":{\"_tag\":\"Small\"},\"tags\":[]}"))
}
`

// TestGenSchemaFits is ergo gen schema's property (wf10 JS0), checked in
// process: for schemaFixture's Box, the generated package checks and its
// law BoxValuesFitTheSchema holds over the Boxes check time draws (the
// codec's JSON of each fits, so a json.Optional key written as required is
// refuted by a Box with no note), and the probe's OnlyBoxesFit holds (a
// union variant written without its "_tag" const lets a tag no variant
// has fit). Mutants: schema-optional-required and schema-union-untagged
// (internal/rpcgen's ledger).
func TestGenSchemaFits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ergo.mod"), "module loop\n")
	writeFile(t, filepath.Join(dir, "shapes", "shapes.ergo"), schemaFixture)
	a, err := sketchfront.CheckDir(filepath.Join(dir, "shapes"))
	if err != nil || a.Root().Package == nil {
		t.Fatalf("the fixture does not check: %v %v", err, a.Diagnostics)
	}
	out, err := rpcgen.GenerateSchema(a.Root().Package, "loop/shapes", "Box")
	if err != nil {
		t.Fatal(err)
	}
	var generated strings.Builder
	for name, src := range out.Files {
		writeFile(t, filepath.Join(dir, "shapes", out.Dir, name), src)
		generated.WriteString(src)
	}
	writeFile(t, filepath.Join(dir, "probe", "probe.ergo"), schemaProbe)
	for _, c := range []struct{ pkg, law string }{
		{filepath.Join("shapes", out.Dir), "law BoxValuesFitTheSchema"},
		{"probe", "law OnlyBoxesFit"},
	} {
		a, err := sketchfront.CheckDir(filepath.Join(dir, c.pkg))
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v %v\n%s", c.pkg, err, a.Diagnostics, generated.String())
		}
		rep, err := a.Evaluate(eval.DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rep.Results {
			if r.Name == c.law {
				found = true
				if r.Status != eval.Passed {
					t.Errorf("%s: %s is %s: %s\n%s", c.pkg, c.law, r.Status, r.Reason, generated.String())
				}
			}
		}
		if !found {
			t.Errorf("%s: no %s among %+v", c.pkg, c.law, rep.Results)
		}
	}
}

// TestGenSchemaRefusesNoWireForm: ergo gen schema names the type it cannot
// write a schema for (a recursive type, an operation type) rather than
// writing a schema that admits other values.
func TestGenSchemaRefusesNoWireForm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ergo.mod"), "module loop\n")
	writeFile(t, filepath.Join(dir, "shapes", "shapes.ergo"), "type Node { Next []Node }\n\ntype Svc { Get() int }\n")
	a, err := sketchfront.CheckDir(filepath.Join(dir, "shapes"))
	if err != nil || a.Root().Package == nil {
		t.Fatalf("the fixture does not check: %v %v", err, a.Diagnostics)
	}
	for name, want := range map[string]string{"Node": "recursive", "Svc": "operation type", "Missing": "declares no type"} {
		if _, err := rpcgen.GenerateSchema(a.Root().Package, "loop/shapes", name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want an error naming %q, got %v", name, want, err)
		}
	}
}

func TestSchemaExprCoversAllShapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{}`, `jsonschema.Any()`},
		{`{"type":"null"}`, `jsonschema.Null()`},
		{`{"type":"boolean"}`, `jsonschema.Bool()`},
		{`{"type":"string"}`, `jsonschema.String()`},
		{`{"type":"integer"}`, `jsonschema.Integer()`},
		{`{"type":"number"}`, `jsonschema.Number()`},
		{`{"const":"Card"}`, `jsonschema.ConstString("Card")`},
		{`{"type":"array","items":{"type":"string"}}`, `jsonschema.Array(jsonschema.String())`},
		{`{"type":"array","items":{"type":"integer"},"minItems":3,"maxItems":3}`, `jsonschema.FixedArray(jsonschema.Integer(), 3)`},
		{`{"type":"array","prefixItems":[{"type":"string"},{"type":"boolean"}]}`, `jsonschema.Tuple([]jsonschema.Schema{jsonschema.String(), jsonschema.Bool()})`},
		{`{"type":"object"}`, `jsonschema.ObjectAny()`},
		{`{"type":"object","additionalProperties":{"type":"number"}}`, `jsonschema.Record(jsonschema.Number())`},
		{`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"]}`, `jsonschema.Object([]jsonschema.Prop{jsonschema.Prop{Key: "a", Schema: jsonschema.String()}, jsonschema.Prop{Key: "b", Schema: jsonschema.Integer()}}, []string{"a"})`},
		{`{"anyOf":[{"type":"string"},{"type":"null"}]}`, `jsonschema.AnyOf(jsonschema.String(), []jsonschema.Schema{jsonschema.Null()})`},
	}
	for _, tc := range cases {
		got, err := rpcgen.SchemaExpr(tc.in)
		if err != nil {
			t.Errorf("SchemaExpr(%s): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SchemaExpr(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{`not-json`, `[]`, `{"anyOf":[]}`, `{"type":"unknown"}`, `{"extra":1}`} {
		if got, err := rpcgen.SchemaExpr(bad); err == nil {
			t.Errorf("SchemaExpr(%s) = %s, want error", bad, got)
		}
	}
}
