package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// jsonFieldsProbe holds optional, nullable and three-state fields against
// an independent event writer. Mutants g3-omit, g3-null, g3-wrap and
// g3-comma have no killer in the earlier json laws: those draw no wrappers.
const jsonFieldsProbe = `import json

type fields { A json.Optional[int], B int, C json.Nullable[string], D json.Optional[json.Nullable[int]], E json.Optional[string], F Option[int] }
type boxed[T, U] { A T, B int, C json.Nullable[string], D U, E json.Optional[string], F Option[int] }
type nullableField { Value json.Nullable[int] }
type optionalField { Value json.Optional[int] }
type nestedField { Value json.Optional[json.Nullable[int]] }
type input { A Option[int], B int, C Option[string], D Option[Option[int]], E Option[string], F Option[int] }

func optionalOf[T](v Option[T]) json.Optional[T] {
	match v {
	case None: json.Absent
	case Some[T] as p: json.Present{Value: p.Value}
	}
}

func nullableOf[T](v Option[T]) json.Nullable[T] {
	match v {
	case None: json.Null
	case Some[T] as p: json.NonNull{Value: p.Value}
	}
}

func nestedOf(v Option[Option[int]]) json.Optional[json.Nullable[int]] {
	match v {
	case None: json.Absent
	case Some[Option[int]] as p: json.Present{Value: nullableOf(p.Value)}
	}
}

func c[T wire]() json.Codec[T] { json.Codec[T]{MaxBytes: 0, MaxDepth: 0} }

func reference(v fields) string {
	let t = json.NewText()
	t.BeginObject(6)
	match v.A {
	case json.Absent:
	case json.Present[int] as p: t.Key("a"); t.Int(int64(p.Value))
	}
	t.Key("b")
	t.Int(int64(v.B))
	t.Key("c")
	match v.C {
	case json.Null: t.Null()
	case json.NonNull[string] as p: t.String(p.Value)
	}
	match v.D {
	case json.Absent:
	case json.Present[json.Nullable[int]] as p:
		t.Key("d")
		match p.Value {
		case json.Null: t.Null()
		case json.NonNull[int] as x: t.Int(int64(x.Value))
		}
	}
	match v.E {
	case json.Absent:
	case json.Present[string] as p: t.Key("e"); t.String(p.Value)
	}
	t.Key("f")
	t.BeginObject(1)
	match v.F {
	case None: t.Tag("None")
	case Some[int] as p: t.Tag("Some"); t.Key("value"); t.Int(int64(p.Value))
	}
	t.EndObject()
	t.EndObject()
	t.Written()
}

test JsonFieldsAgree(i input) {
	let v = fields{A: optionalOf(i.A), B: i.B, C: nullableOf(i.C), D: nestedOf(i.D), E: optionalOf(i.E), F: i.F}
	let text = json.Encode(c[fields](), v)
	let want = reference(v)
	assert text == want
	let back = json.Decode(c[fields](), text) catch { case _: assert false; v }
	assert back == v
	let t = json.NewText()
	let w Writer = t
	encode(v, w)
	assert t.Written() == want
	// A field typed by a type parameter takes the form of its argument.
	// Inside a generic function the record's codec takes T's witness.
	let g = boxed[json.Optional[int], json.Optional[json.Nullable[int]]]{A: v.A, B: v.B, C: v.C, D: v.D, E: v.E, F: v.F}
	assert encodeVia(g) == want
	assert writeVia(g) == want
	assert decodeVia(want, g) == Some{Value: g}
}

func encodeVia[T wire, U wire](g boxed[T, U]) string { json.Encode(c[boxed[T, U]](), g) }

func writeVia[T wire, U wire](g boxed[T, U]) string {
	let t = json.NewText()
	let w Writer = t
	encode(g, w)
	t.Written()
}

func decodeVia[T wire, U wire](text string, like boxed[T, U]) Option[boxed[T, U]] {
	let v = json.Decode(c[boxed[T, U]](), text) catch { case _: return None }
	Some{Value: v}
}

test JsonOptionalRoundTrips(v Option[int]) {
	assert optionalRoundTrips(v)
}

func optionalRoundTrips(v Option[int]) bool {
	let field = optionalField{Value: optionalOf(v)}
	let back = json.Decode(c[optionalField](), json.Encode(c[optionalField](), field)) catch { case _: return false }
	back == field
}

test JsonNullableRoundTrips(v Option[int]) {
	assert nullableRoundTrips(v)
}

func nullableRoundTrips(v Option[int]) bool {
	let field = nullableField{Value: nullableOf(v)}
	let back = json.Decode(c[nullableField](), json.Encode(c[nullableField](), field)) catch { case _: return false }
	back == field
}

test JsonNestedRoundTrips(v Option[Option[int]]) {
	assert nestedRoundTrips(v)
}

func nestedRoundTrips(v Option[Option[int]]) bool {
	let field = nestedField{Value: nestedOf(v)}
	let back = json.Decode(c[nestedField](), json.Encode(c[nestedField](), field)) catch { case _: return false }
	back == field
}

// The one concrete row protects the refusal's field and path.
test JsonNullableKeyIsRequired {
	let _ = json.Decode(c[nullableField](), "{}") catch {
	case WireMissingField as e: assert e.Path == ""; assert e.Field == "value"; return
	case _: assert false; return
	}
	assert false
}
`

// TestJSONFieldsAgree runs the public codec property, including the three
// wrapper round trips and the missing-nullable-key refusal.
// It builds Go because the property observes the encoded bytes and decoded
// values of generated codec functions; go/types cannot execute them.
func TestJSONFieldsAgree(t *testing.T) {
	needsBuild(t)
	out, errout, code := runExeArgs(t, tableExe(t, "jsonfields"), "--seed", "1")
	if code != 0 {
		t.Fatalf("json field properties: exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, jsonFieldLawStatements, func(key string) string {
		_, law, _ := strings.Cut(key, " ")
		return "Json" + law
	}, jsonFieldsProbe, out, "JsonFieldsAgree", "JsonNullableKeyIsRequired"))
}

var jsonFieldLawStatements = map[string]string{
	"json OptionalRoundTrips": `OptionalRoundTrips(v Option[int]) { optionalRoundTrips(v) }`,
	"json NullableRoundTrips": `NullableRoundTrips(v Option[int]) { nullableRoundTrips(v) }`,
	"json NestedRoundTrips":   `NestedRoundTrips(v Option[Option[int]]) { nestedRoundTrips(v) }`,
}
