package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// jsonValueProbe restates std json's ValueRoundTrips over generated text
// (deep nesting, long numbers, exponents, strings of any text) and holds
// json.Value as a record field, through Writer and through codec's events.
// Mutants g4-members and g4-number (the ledger), and the std mutants
// g4-peek (json Peek maps 2 to WireString) and g4-replay-tag (Replay's Key
// consumes a tag), judged by hand because the mutant tool maps Go coverage
// only, have no killer in the earlier json laws: those draw no Value.
// JsonValueRepeatedKey is the stated rule's one row: a repeated key is
// kept, in order. JsonValueLeastFraction is the smallest repro of the J2
// bug: magnitude negated the number, so a fraction drawn as the least int
// spelled ".0-9223…", an invalid number (mutant json-value-least-frac).
const jsonValueProbe = `import codec
import json
import strconv
import strings

type docNull {}
type docBool { V bool }
type docInt { V int64 }
type docDigits { Lead int, Frac int, Exp int, Long bool }
type docStr { V string }
type docArr { Items []doc }
type docObj { Keys []string, Vals []doc }
type doc = docNull | docBool | docInt | docDigits | docStr | docArr | docObj

type envelope { ID int, Meta json.Value }

func c[T wire]() json.Codec[T] { json.Codec[T]{MaxBytes: 0, MaxDepth: 0} }

func magnitude(n int) string { strings.TrimPrefix(strconv.Itoa(n), "-") }

func digits(n docDigits) string {
	let mut s = strconv.Itoa(n.Lead)
	if n.Long && n.Lead != 0 {
		s = s + "12345678901234567890123456789"
	}
	if n.Frac != 0 {
		s = s + ".0" + magnitude(n.Frac)
	}
	if n.Exp > 0 {
		s = s + "e+" + strconv.Itoa(n.Exp)
	}
	if n.Exp < 0 {
		s = s + "E" + strconv.Itoa(n.Exp)
	}
	s
}

func quoted(s string) string { json.Encode(c[string](), s) }

func spell(d doc, sp string) string {
	match d {
	case docNull:
		"null"
	case docBool as b:
		if b.V { "true" } else { "false" }
	case docInt as n:
		strconv.FormatInt(n.V, 10)
	case docDigits as n:
		digits(n)
	case docStr as s:
		quoted(s.V)
	case docArr as a:
		let mut out = "[" + sp
		for i in 0..len(a.Items) {
			if i > 0 { out = out + sp + "," + sp }
			out = out + spell(a.Items[i], sp)
		}
		out + sp + "]"
	case docObj as o:
		let mut out = "{" + sp
		let n = if len(o.Keys) < len(o.Vals) { len(o.Keys) } else { len(o.Vals) }
		for i in 0..n {
			if i > 0 { out = out + sp + "," + sp }
			out = out + quoted(o.Keys[i]) + sp + ":" + sp + spell(o.Vals[i], sp)
		}
		out + sp + "}"
	}
}

func valueRoundTrips(d doc) bool {
	let v = json.Decode(c[json.Value](), spell(d, " \n\t\r")) catch { case _: return false }
	json.Encode(c[json.Value](), v) == spell(d, "")
}

test JsonValueRoundTrips(d doc) {
	assert valueRoundTrips(d)
}

// viaEvents is v through codec's Events and Replay (Number, Peek).
func viaEvents(d doc) bool {
	let v = json.Decode(c[json.Value](), spell(d, "")) catch { case _: return false }
	let back = codec.RoundTrip(v) catch { case _: return false }
	json.Encode(c[json.Value](), back) == spell(d, "")
}

// tagAsMember reads a union's events as a Value: Replay gives the tag as
// the member "_tag", as JSON writes it.
func tagAsMember(x Option[int]) bool {
	let e = codec.NewEvents()
	encode(x, e)
	let v = decode[json.Value](codec.NewReplay(codec.Recorded(e))) catch { case _: return false }
	json.Encode(c[json.Value](), v) == json.Encode(c[Option[int]](), x)
}

// kinds counts the values of each kind by an exhaustive match.
func kinds(v json.Value) int {
	match v {
	case json.Null: 1
	case json.Boolean: 1
	case json.Num: 1
	case json.Str: 1
	case json.Array as a:
		let mut n = 1
		for i in 0..len(a.Items) { n = n + kinds(a.Items[i]) }
		n
	case json.Object as o:
		let mut n = 1
		for i in 0..len(o.Members) { n = n + kinds(o.Members[i].Value) }
		n
	}
}

test JsonValueField(d doc, id int) {
	let text = "{\"id\":" + strconv.Itoa(id) + ",\"meta\":" + spell(d, "") + "}"
	let e = json.Decode(c[envelope](), text) catch { case _: assert false; return }
	assert e.ID == id
	assert json.Encode(c[envelope](), e) == text
	let t = json.NewText()
	let w Writer = t
	encode(e, w)
	assert t.Written() == text
	assert kinds(e.Meta) > 0
	assert viaEvents(d)
	assert tagAsMember(if id % 2 == 0 { None } else { Some{Value: id} })
}

test JsonValueRepeatedKey {
	let v = json.Decode(c[json.Value](), "{\"a\":1,\"a\":2}") catch { case _: assert false; return }
	match v {
	case json.Object as o:
		assert len(o.Members) == 2
		assert o.Members[0].Key == "a"
		assert o.Members[1].Key == "a"
	case _:
		assert false
	}
	assert json.Encode(c[json.Value](), v) == "{\"a\":1,\"a\":2}"
}

test JsonValueLeastFraction {
	assert valueRoundTrips(docDigits{Lead: 1, Frac: -9223372036854775807 - 1, Exp: 0, Long: false})
}
`

// TestJSONValueAgrees runs the json.Value property and its repeated-key row.
// It builds Go because the property observes the bytes and values of
// generated codec functions; go/types cannot execute them.
func TestJSONValueAgrees(t *testing.T) {
	needsBuild(t)
	out, errout, code := runExeArgs(t, tableExe(t, "jsonvalue"), "--seed", "1")
	if code != 0 {
		t.Fatalf("json value properties: exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, jsonValueLawStatements, func(key string) string {
		_, law, _ := strings.Cut(key, " ")
		return "Json" + law
	}, jsonValueProbe, out, "JsonValueField", "JsonValueRepeatedKey", "JsonValueLeastFraction"))
}

var jsonValueLawStatements = map[string]string{
	"json ValueRoundTrips": `ValueRoundTrips(d doc) { valueRoundTrips(d) }`,
}
