package lower

import (
	"strings"
	"testing"
)

// wireFormsProbe uses user-owned declarations, with no std type/field
// names, and the same public wire protocol as std. cf1-form-by-name is
// not killed by earlier std-only laws; omission/missing-key mutants
// likewise need a user-owned generic field witness.
const wireFormsProbe = `import codec
import json
import strconv
import strings

type Gone {}
type Here[T] { Payload T }
type Maybe[T] = Gone | Here[T]
func WireForm[T](x Maybe[T]) WireOmittable { WireOmittable }
type NilValue {}
type HasValue[T] { Item T }
type NullOr[T] = NilValue | HasValue[T]
func WireForm[T](x NullOr[T]) WireNullable { WireNullable }
type Flag { Data bool }
type Digits { spelling string }
type Word { Data string }
type List { Data []Tree }
type Entry { Name string, Data Tree }
type Dict { Data []Entry }
type Tree = NilValue | Flag | Digits | Word | List | Dict
type Stop {}
type Packet { N int }
type Tiny = Stop | Packet
func WireForm(x Tiny) WireOmittable { WireOmittable }
func WireForm(x Tree) WireBare { WireBare }
func WireForm(x Digits) WireNumberText { WireNumberText }
func WireForm(x Dict) WireMembers { WireMembers }
func Wire(x Digits) string { x.spelling }
func ParseDigits(s string) Digits fails json.Malformed {
	let n = try json.ParseNum(s)
	Digits{spelling: n.Wire()}
}
type DirectDigits { text string }
func WireForm(x DirectDigits) WireNumberText { WireNumberText }
func Wire(x DirectDigits) string { x.text }
func ParseDirectDigits(s string) DirectDigits { DirectDigits{text: s} }
type PositiveDigits { text string }
type NegativeNumber {}
func WireForm(x PositiveDigits) WireNumberText { WireNumberText }
func Wire(x PositiveDigits) string { x.text }
func ParsePositiveDigits(s string) PositiveDigits fails json.Malformed | NegativeNumber {
	let n = try json.ParseNum(s)
	if strings.HasPrefix(s, "-") { return NegativeNumber }
	PositiveDigits{text: n.Wire()}
}
type local[T] { A T, B NullOr[string], C Tree, D Digits, E Tiny }
type standard { A json.Optional[int], B json.Nullable[string], C json.Value, D json.Num, E json.Optional[int] }
type single { E Tiny, V int }
type stdSingle { E json.Optional[int], V int }
type draw { A Option[int], B Option[string], Text string, N int64, Flag bool }
func c[T wire]() json.Codec[T] { json.Codec[T]{MaxBytes: 0, MaxDepth: 0} }

func encodeVia[T wire](v local[T]) string { json.Encode(c[local[T]](), v) }
func decodeVia[T wire](s string, like local[T]) local[T] fails json.Malformed | wired[local[T]] {
	try json.Decode(c[local[T]](), s)
}

test UserFormsAgree(i draw) {
	let a Maybe[int] = match i.A {
	case None: Gone
	case Some[int] as p: Here{Payload: p.Value}
	}
	let sa json.Optional[int] = match i.A {
	case None: json.Absent
	case Some[int] as p: json.Present{Value: p.Value}
	}
	let b NullOr[string] = match i.B {
	case None: NilValue
	case Some[string] as p: HasValue{Item: p.Value}
	}
	let sb json.Nullable[string] = match i.B {
	case None: json.Null
	case Some[string] as p: json.NonNull{Value: p.Value}
	}
	let digits = strconv.FormatInt(i.N, 10) + "e+0"
	let d = try ParseDigits(digits)
	let sd = try json.ParseNum(digits)
	let tree Tree = Dict{Data: []Entry{
		Entry{Name: i.Text, Data: List{Data: []Tree{
			NilValue, Flag{Data: i.Flag}, d, Word{Data: i.Text},
			List{Data: []Tree{}}, Dict{Data: []Entry{}},
		}}},
		Entry{Name: i.Text, Data: d},
	}}
	let raw = "{\"" + "items" + "\":[" +
		"null," + (if i.Flag { "true" } else { "false" }) + "," +
		digits + "," + json.Encode(c[string](), i.Text) + ",[],{}]}"
	let parsed = try json.Decode(c[Tree](), raw)
	let stdParsed = try json.Decode(c[json.Value](), raw)
	assert json.Encode(c[Tree](), parsed) == json.Encode(c[json.Value](), stdParsed)
	let bytes = json.Encode(c[Tree](), tree)
	// Independent spelling protects against the two codecs agreeing on
	// the same wrong key, member order, number or event kind.
	let key = json.Encode(c[string](), i.Text)
	let contents = "[null," + (if i.Flag { "true" } else { "false" }) +
		"," + digits + "," + key + ",[],{}]"
	assert bytes == "{" + key + ":" + contents + "," + key + ":" + digits + "}"
	let st = try json.Decode(c[json.Value](), bytes)
	let e Tiny = if i.Flag { Packet{N: int(i.N)} } else { Stop }
	let se json.Optional[int] = if i.Flag { json.Present{Value: int(i.N)} } else { json.Absent }
	let v = local[Maybe[int]]{A: a, B: b, C: tree, D: d, E: e}
	let s = standard{A: sa, B: sb, C: st, D: sd, E: se}
	let want = json.Encode(c[standard](), s)
	assert encodeVia(v) == want
	let lone = single{E: e, V: int(i.N)}
	let stdLone = stdSingle{E: se, V: int(i.N)}
	assert json.Encode(c[single](), lone) == json.Encode(c[stdSingle](), stdLone)
	let events = codec.NewEvents()
	let stdEvents = codec.NewEvents()
	encode(v, events)
	encode(s, stdEvents)
	assert events.Recorded() == stdEvents.Recorded()
	let back = try decodeVia(want, v)
	assert encodeVia(back) == want
	let t = json.NewText()
	let w Writer = t
	encode(v, w)
	assert t.Written() == want
	// Root/array omission is not a field: it keeps the tagged union.
	let tagged = match a {
	case Gone: "{\"_tag\":\"Gone\"}"
	case Here[int] as p: "{\"_tag\":\"Here\",\"payload\":" + strconv.Itoa(p.Payload) + "}"
	}
	assert json.Encode(c[Maybe[int]](), a) == tagged
	let direct = try json.Decode(c[DirectDigits](), digits)
	assert direct.Wire() == digits
	let _ = json.Decode(c[PositiveDigits](), digits) catch {
	case NegativeNumber: assert i.N < 0; return
	case _: assert false; return
	}
	assert i.N >= 0
}
`

// TestWireFormsAgree builds Go because its public property compares the
// bytes and decoded values of user/std generated codecs, which go/types
// cannot execute. The one-binary kit bounds every child in the kernel.
// Numeric forms also preserve a custom parser's rejection and support an
// infallible parser; cf1-number-parser-bypassed / cf1-parse-fallibility.
func TestWireFormsAgree(t *testing.T) {
	needsBuild(t)
	saved := wireLowering
	defer func() { wireLowering = saved }()
	var programs []*lowered
	for _, mode := range []string{"", "writer"} {
		wireLowering = mode
		programs = append(programs, lowerTestBuild(t, "wireforms"+mode, wireFormsProbe))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.FailNow()
	}
	var results []string
	for _, name := range []string{"wireforms", "wireformswriter"} {
		out, errout, code := runExeArgs(t, exes[name], "--seed", "119")
		results = append(results, out+"\n"+errout)
		if code != 0 || !strings.Contains(out, "UserFormsAgree") {
			t.Fatalf("replay: go test ./internal/sketch/lower -run '^TestWireFormsAgree$'\nsource:\n%s\n%s exit %d\noutputs:\n%s",
				wireFormsProbe, name, code, strings.Join(results, "\n---\n"))
		}
	}
	if results[0] != results[1] {
		t.Fatalf("wire lowering outputs differ\nsource:\n%s\n%s", wireFormsProbe, strings.Join(results, "\n---\n"))
	}
}
