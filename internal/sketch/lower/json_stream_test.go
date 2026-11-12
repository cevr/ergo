package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// jsonStreamProbe restates std json's Stream and Elements laws
// (stream_laws.ergo, elements_laws.ergo) in a module of its own:
// DecodeStream over a text cut every few bytes gives what Decode gives for
// the text whole, on spelled docs with a piece spliced in, on texts made
// of pieces (mostly not JSON) under small byte and nesting limits, on
// encoded fleets, and on a union whose "_tag" comes last; Elements reads
// an array element by element as Decode reads it into a slice, a sequence
// stream's values back as written and stops at a break, and holds one
// window however long the text. The std mutants sj1-* and sj2-*
// (internal/sketch/std/testdata/mutants.json) are its kills.
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


// The laws of Elements: the elements of an array, read one at a time
// through a Stream cut anywhere, are what json.Decode reads into a slice, and
// Next's last answer is json.Decode's verdict; a sequence's values read back as
// written; and the window a Stream holds does not grow with the text.

func wholeValues(c json.Codec[[]json.Value], text string) string {
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

func eachValue(c json.Codec[[]json.Value], text string, cuts []int) string {
	let e = json.NewElements[json.Value](json.NewStream(chunks(text, cuts), c.MaxBytes, c.MaxDepth))
	let mut got = []json.Value{}
	for {
		let next = e.Next() catch {
		case json.Malformed as m:
			return said(m)
		case io.ReadFailed:
			return "read failed"
		case WireWrongKind as w:
			return "kind " + w.Path + " " + w.Want
		case WireMissingField as w:
			return "missing " + w.Path + " " + w.Field
		case WireUnknownTag as w:
			return "tag " + w.Path + " " + w.Tag
		}
		match next {
		case Some[json.Value] as v:
			got = got.Append(v.Value)
		case None:
			break
		}
	}
	"ok " + json.Encode(c, got)
}

func wholeUps(c json.Codec[[]up], text string) string {
	let v = json.Decode(c, text) catch {
	case json.Malformed as m:
		return said(m)
	case WireWrongKind as e:
		return "kind " + e.Path + " " + e.Want
	case WireMissingField as e:
		return "missing " + e.Path + " " + e.Field
	}
	"ok " + json.Encode(c, v)
}

// eachUp reads text's elements as ups, from an array, or from a sequence
// when seq.
func eachUp(c json.Codec[[]up], text string, cuts []int, seq bool) string {
	let src = chunks(text, cuts)
	let r = if seq { json.NewSequenceStream(src, c.MaxBytes, c.MaxDepth) } else { json.NewStream(src, c.MaxBytes, c.MaxDepth) }
	let e = json.NewElements[up](r)
	let mut got = []up{}
	for {
		let next = e.Next() catch {
		case json.Malformed as m:
			return said(m)
		case io.ReadFailed:
			return "read failed"
		case WireWrongKind as w:
			return "kind " + w.Path + " " + w.Want
		case WireMissingField as w:
			return "missing " + w.Path + " " + w.Field
		}
		match next {
		case Some[up] as v:
			got = got.Append(v.Value)
		case None:
			break
		}
	}
	"ok " + json.Encode(c, got)
}

// elementsAgree: the text read whole into a slice and element by element
// gives one outcome, as Values and as ups, under the same limits.
func elementsAgree(text string, cuts []int, maxBytes int, maxDepth int) bool {
	let cv = json.Codec[[]json.Value]{MaxBytes: maxBytes, MaxDepth: maxDepth}
	let cu = json.Codec[[]up]{MaxBytes: maxBytes, MaxDepth: maxDepth}
	wholeValues(cv, text) == eachValue(cv, text, cuts) && wholeUps(cu, text) == eachUp(cu, text, cuts, false)
}

// ElementsEqualWholeDecode: a text spelled from a doc, as it is or as the
// one element of a list, with one piece spliced in or not, reads element
// by element through a Stream cut anywhere as json.Decode reads it into a
// slice: the same elements, or the same failure at the end.
test JsonElementsEqualWholeDecode(d doc, space int, splice int, at int, cuts []int, depth int) {
	assert elementsSpliced(d, space, splice, at, cuts, depth)
}

func elementsSpliced(d doc, space int, splice int, at int, cuts []int, depth int) bool {
	let spaces = []string{"", " ", "\n\t", " \r\n "}
	let sp = spaces[nat(space)%len(spaces)]
	let mut text = spell(d, sp)
	if nat(space)%2 == 1 {
		text = "[" + sp + text + sp + "," + text + "]"
	}
	if splice >= 0 {
		let k = nat(at) % (len(text) + 1)
		text = text[:k] + pieces[splice%len(pieces)] + text[k:]
	}
	elementsAgree(text, cuts, 0, nat(depth)%6)
}

// ElementsAgreeOnPieces: a list of pieces, mostly not JSON, and each piece
// as two elements of a list, read element by element as json.Decode reads them
// into a slice, under any byte and nesting limit.
test JsonElementsAgreeOnPieces(parts []int, cuts []int, bytes int, depth int) {
	assert elementsOnPieces(parts, cuts, bytes, depth)
}

func elementsOnPieces(parts []int, cuts []int, bytes int, depth int) bool {
	let mut text = "["
	for p in parts {
		let piece = pieces[nat(p)%len(pieces)]
		if !elementsAgree("["+piece+","+piece+"]", cuts, 0, 0) {
			return false
		}
		text = text + piece
	}
	let maxBytes = if bytes%3 == 0 { 0 } else { nat(bytes) % (len(text) + 2) }
	elementsAgree(text, cuts, maxBytes, nat(depth)%4)
}

// ElementsRoundTrip: the elements of an encoded list, read one at a time,
// are the list.
test JsonElementsRoundTrip(vs []up, cuts []int) {
	assert elementsRoundTrip(vs, cuts)
}

func elementsRoundTrip(vs []up, cuts []int) bool {
	let c = json.Codec[[]up]{MaxBytes: 0, MaxDepth: 0}
	let text = json.Encode(c, vs)
	eachUp(c, text, cuts, false) == "ok "+text && wholeUps(c, text) == "ok "+text
}

// separated is vs encoded one after another, each followed by a
// separator drawn from seps, as a sequence stream reads them.
func separated(vs []up, seps []int) string {
	let c = json.Codec[up]{MaxBytes: 0, MaxDepth: 0}
	let mut text = ""
	for i in 0..len(vs) {
		text = text + json.Encode(c, vs[i]) + between[sepAt(seps, i)]
	}
	text
}

let between = []string{"\n", " ", "\r\n", "\n\n", "\t"}

// sepAt is the index in between of the separator after value i.
func sepAt(seps []int, i int) int {
	let k = if i < len(seps) { nat(seps[i]) } else { i }
	k % len(between)
}

// ValuesRoundTrip: values written one after another, newline-delimited
// or by any white space, read back from a sequence stream as written.
test JsonValuesRoundTrip(vs []up, seps []int, cuts []int) {
	assert valuesRoundTrip(vs, seps, cuts)
}

func valuesRoundTrip(vs []up, seps []int, cuts []int) bool {
	let c = json.Codec[[]up]{MaxBytes: 0, MaxDepth: 0}
	eachUp(c, separated(vs, seps), cuts, true) == "ok "+json.Encode(c, vs)
}

// ValuesStopAtBreak: a sequence cut short gives the values written whole
// before the cut and then fails json.Malformed at the cut, or, cut between
// values, gives those values and ends.
test JsonValuesStopAtBreak(vs []up, seps []int, cut int, cuts []int) {
	assert valuesStopAtBreak(vs, seps, cut, cuts)
}

func valuesStopAtBreak(vs []up, seps []int, cut int, cuts []int) bool {
	let one = json.Codec[up]{MaxBytes: 0, MaxDepth: 0}
	let c = json.Codec[[]up]{MaxBytes: 0, MaxDepth: 0}
	let text = separated(vs, seps)
	let k = nat(cut) % (len(text) + 1)
	let mut whole = []up{}
	let mut start = 0
	let mut inside = -1
	for i in 0..len(vs) {
		let n = len(json.Encode(one, vs[i]))
		if start+n <= k {
			whole = whole.Append(vs[i])
		} else if start < k {
			inside = start
		}
		start = start + n + len(between[sepAt(seps, i)])
	}
	let e = json.NewElements[up](json.NewSequenceStream(chunks(text[:k], cuts), 0, 0))
	let mut got = []up{}
	for {
		let next = e.Next() catch {
		case json.Malformed as m:
			return inside >= 0 && m.Offset >= inside && m.Offset <= k && json.Encode(c, got) == json.Encode(c, whole)
		case _:
			return false
		}
		match next {
		case Some[up] as v:
			got = got.Append(v.Value)
		case None:
			break
		}
	}
	inside < 0 && json.Encode(c, got) == json.Encode(c, whole)
}

// ElementsHoldOneWindow: reading a sequence of n values from chunks of
// one size holds at most that size and the few bytes a token past the
// edge needs, however many values the text holds (the growth guard: the
// window counts the bytes the Stream keeps).
test JsonElementsHoldOneWindow(v up, n int, size int) {
	assert holdsOneWindow(v, n, size)
}

func holdsOneWindow(v up, n int, size int) bool {
	let chunk = nat(size)%64 + 1
	let count = nat(n)%40 + 1
	let text = separated([]up{v}, []int{})
	let mut all = ""
	for _ in 0..count {
		all = all + text
	}
	let r = json.NewSequenceStream(io.Chunked(all, chunk), 0, 0)
	let e = json.NewElements[up](r)
	let mut read = 0
	for {
		let next = e.Next() catch {
		case _:
			return false
		}
		match next {
		case Some[up]:
			read = read + 1
		case None:
			break
		}
	}
	read == count && json.Window(r) <= chunk+6
}
`

// TestJSONStreamAgrees runs the Stream and Elements laws' restatements
// with --seed 1. It builds Go because the laws run in the built test
// binary (ergo test), the only place a law runs.
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
	"json ElementsAgreeOnPieces":    `ElementsAgreeOnPieces(parts []int, cuts []int, bytes int, depth int) { elementsOnPieces(parts, cuts, bytes, depth) }`,
	"json ElementsEqualWholeDecode": `ElementsEqualWholeDecode(d doc, space int, splice int, at int, cuts []int, depth int) { elementsSpliced(d, space, splice, at, cuts, depth) }`,
	"json ElementsHoldOneWindow":    `ElementsHoldOneWindow(v up, n int, size int) { holdsOneWindow(v, n, size) }`,
	"json ElementsRoundTrip":        `ElementsRoundTrip(vs []up, cuts []int) { elementsRoundTrip(vs, cuts) }`,
	"json StreamAgreesOnPieces":     `StreamAgreesOnPieces(parts []int, cuts []int, bytes int, depth int) { agreesOnPieces(parts, cuts, bytes, depth) }`,
	"json StreamAgreesWithDecode":   `StreamAgreesWithDecode(d doc, space int, splice int, at int, cuts []int, depth int) { agreesSpliced(d, space, splice, at, cuts, depth) }`,
	"json StreamFindsTagAnywhere":   `StreamFindsTagAnywhere(v up, cuts []int) { tagAnywhere(v, cuts) }`,
	"json StreamRoundTrips":         `StreamRoundTrips(v fleet, cuts []int) { streamRoundTrips(v, cuts) }`,
	"json ValuesRoundTrip":          `ValuesRoundTrip(vs []up, seps []int, cuts []int) { valuesRoundTrip(vs, seps, cuts) }`,
	"json ValuesStopAtBreak":        `ValuesStopAtBreak(vs []up, seps []int, cut int, cuts []int) { valuesStopAtBreak(vs, seps, cut, cuts) }`,
}
