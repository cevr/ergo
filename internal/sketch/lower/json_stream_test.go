package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// jsonStreamProbe restates std json's Stream laws (stream_laws.ergo) in a
// module of its own: DecodeStream over a text cut every few bytes gives
// what Decode gives for the text whole, on spelled docs with a piece
// spliced in, on texts made of pieces (mostly not JSON) under small byte
// and nesting limits, on encoded fleets, and on a union whose "_tag" comes
// last. The std mutants sj1-* (internal/sketch/std/testdata/mutants.json)
// are the kills: a refill that drops the rest of the window, a nesting
// limit off by one, an offset counted from the chunk, MaxBytes not ahead
// of an earlier break, a tag looked for only first, and a failed decode
// not settled.
const jsonStreamProbe = `import io
import json
import strconv
import strings

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down
type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }
type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths, Pairs Map[string, (string, int64)], Nested [][]float64 }

func codec() json.Codec[fleet] { json.Codec[fleet]{MaxBytes: 0, MaxDepth: 0} }

type docNull {}
type docBool { V bool }
type docInt { V int64 }
type docDigits { Lead int, Frac int, Exp int, Long bool }
type docStr { V string }
type docArr { Items []doc }
type docObj { Keys []string, Vals []doc }
type doc = docNull | docBool | docInt | docDigits | docStr | docArr | docObj

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

func quoted(s string) string { json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, s) }

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

func nat(n int) int { if n < 0 { -(n + 1) } else { n } }

func chunks(text string, cuts []int) io.Memory {
	let mut at = 0
	let mut offsets = []int{}
	for c in cuts {
		at = at + nat(c)%8 + 1
		offsets = offsets.Append(at)
	}
	io.CutAt(text, offsets)
}

func said(m json.Malformed) string { "malformed " + strconv.Itoa(m.Offset) + " " + m.Reason }

func wholeValue(c json.Codec[json.Value], text string) string {
	let v = json.Decode(c, text) catch {
	case json.Malformed as m:
		return said(m)
	case WireWrongKind as e:
		return "kind " + e.Path + " " + e.Want
	case WireMissingField as e:
		return "missing " + e.Path + " " + e.Field
	case WireUnknownTag as e:
		return "tag " + e.Path + " " + e.Tag
	}
	"ok " + json.Encode(c, v)
}

func streamValue(c json.Codec[json.Value], text string, cuts []int) string {
	let v = json.DecodeStream(c, chunks(text, cuts)) catch {
	case json.Malformed as m:
		return said(m)
	case io.ReadFailed:
		return "read failed"
	case WireWrongKind as e:
		return "kind " + e.Path + " " + e.Want
	case WireMissingField as e:
		return "missing " + e.Path + " " + e.Field
	case WireUnknownTag as e:
		return "tag " + e.Path + " " + e.Tag
	}
	"ok " + json.Encode(c, v)
}

func wholeFleet(c json.Codec[fleet], text string) string {
	let v = json.Decode(c, text) catch {
	case json.Malformed as m:
		return said(m)
	case WireWrongKind as e:
		return "kind " + e.Path + " " + e.Want
	case WireMissingField as e:
		return "missing " + e.Path + " " + e.Field
	case WireUnknownTag as e:
		return "tag " + e.Path + " " + e.Tag
	}
	"ok " + json.Encode(c, v)
}

func streamFleet(c json.Codec[fleet], text string, cuts []int) string {
	let v = json.DecodeStream(c, chunks(text, cuts)) catch {
	case json.Malformed as m:
		return said(m)
	case io.ReadFailed:
		return "read failed"
	case WireWrongKind as e:
		return "kind " + e.Path + " " + e.Want
	case WireMissingField as e:
		return "missing " + e.Path + " " + e.Field
	case WireUnknownTag as e:
		return "tag " + e.Path + " " + e.Tag
	}
	"ok " + json.Encode(c, v)
}

func agrees(text string, cuts []int, maxBytes int, maxDepth int) bool {
	let cv = json.Codec[json.Value]{MaxBytes: maxBytes, MaxDepth: maxDepth}
	let cf = json.Codec[fleet]{MaxBytes: maxBytes, MaxDepth: maxDepth}
	let a = wholeValue(cv, text)
	let b = streamValue(cv, text, cuts)
	let x = wholeFleet(cf, text)
	let y = streamFleet(cf, text, cuts)
	a == b && x == y
}

let pieces = []string{"{\"states\":[", "{\"nested\":[[1,", "{\"name\":\"n\",\"states\":[{\"host\":\"h\",\"_tag\":\"up\"},", "{", "}", "[", "]", ",", ":", " ", "\n", "\"a\"", "\"_tag\"", "\"up\"", "\"host\"", "\"NaN\"", "\"\\u00e9\\n\"", "\"\\ud83d\\ude00\"", "\"\\udcff\\ud800\"", "\"\\x\"", "\"\\u12\"", "\"\\u12g4\"", "\"\x01\"", "\"", "\\", "1", "-", "0", "01", "0.", "1e", "1.5e+3", "-0.0E-2", "18446744073709551616", "tru", "true", "false", "null", "nul", "é", "\xff"}

func agreesSpliced(d doc, space int, splice int, at int, cuts []int, depth int) bool {
	let spaces = []string{"", " ", "\n\t", " \r\n "}
	let mut text = spell(d, spaces[nat(space)%len(spaces)])
	if splice >= 0 {
		let k = nat(at) % (len(text) + 1)
		text = text[:k] + pieces[splice%len(pieces)] + text[k:]
	}
	agrees(text, cuts, 0, nat(depth)%6)
}

test JsonStreamAgreesWithDecode(d doc, space int, splice int, at int, cuts []int, depth int) {
	assert agreesSpliced(d, space, splice, at, cuts, depth)
}

func agreesOnPieces(parts []int, cuts []int, bytes int, depth int) bool {
	let mut text = ""
	for p in parts {
		let piece = pieces[nat(p)%len(pieces)]
		if !agrees("{\"states\":["+piece+"],\"nested\":[["+piece+"]]}", cuts, 0, 0) {
			return false
		}
		text = text + piece
	}
	let maxBytes = if bytes%3 == 0 { 0 } else { nat(bytes) % (len(text) + 2) }
	agrees(text, cuts, maxBytes, nat(depth)%4)
}

test JsonStreamAgreesOnPieces(parts []int, cuts []int, bytes int, depth int) {
	assert agreesOnPieces(parts, cuts, bytes, depth)
}

func streamRoundTrips(v fleet, cuts []int) bool {
	let text = json.Encode(codec(), v)
	let want = wholeFleet(codec(), text)
	want == streamFleet(codec(), text, cuts) && want == "ok "+text
}

test JsonStreamRoundTrips(v fleet, cuts []int) {
	assert streamRoundTrips(v, cuts)
}

func tagAnywhere(v up, cuts []int) bool {
	let record = json.Encode(json.Codec[up]{MaxBytes: 0, MaxDepth: 0}, v)
	let text = record[:len(record)-1] + ",\"_tag\":\"up\"}"
	let c = json.Codec[state]{MaxBytes: 0, MaxDepth: 0}
	let whole = json.Decode(c, text) catch {
	case _:
		return false
	}
	let streamed = json.DecodeStream(c, chunks(text, cuts)) catch {
	case _:
		return false
	}
	let want state = v
	json.Encode(c, whole) == json.Encode(c, want) && json.Encode(c, streamed) == json.Encode(c, want)
}

test JsonStreamFindsTagAnywhere(v up, cuts []int) {
	assert tagAnywhere(v, cuts)
}
`

// TestJSONStreamAgrees runs the Stream laws' restatements with --seed 1.
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestJSONStreamAgrees(t *testing.T) {
	needsBuild(t)
	out, errout, code := runExeArgs(t, tableExe(t, "jsonstream"), "--seed", "1")
	if code != 0 {
		t.Fatalf("json stream laws: exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, jsonStreamLawStatements, func(key string) string {
		_, law, _ := strings.Cut(key, " ")
		return "Json" + law
	}, jsonStreamProbe, out))
}

var jsonStreamLawStatements = map[string]string{
	"json StreamAgreesOnPieces":   `StreamAgreesOnPieces(parts []int, cuts []int, bytes int, depth int) { agreesOnPieces(parts, cuts, bytes, depth) }`,
	"json StreamAgreesWithDecode": `StreamAgreesWithDecode(d doc, space int, splice int, at int, cuts []int, depth int) { agreesSpliced(d, space, splice, at, cuts, depth) }`,
	"json StreamFindsTagAnywhere": `StreamFindsTagAnywhere(v up, cuts []int) { tagAnywhere(v, cuts) }`,
	"json StreamRoundTrips":       `StreamRoundTrips(v fleet, cuts []int) { streamRoundTrips(v, cuts) }`,
}
