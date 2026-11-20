package lower

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdJSONLooseLawsProbe restates the laws of std json's loose reader
// (loose.ergo, wf10 JL) as Ergo tests over its exported API: DecodeLoose,
// Read and the Fix variants, with the laws' helpers restated; then a test
// per row of testdata/loose-baml-cases.json (looseCaseTests).
var stdJSONLooseLawsProbe = stdJSONLooseLaws + looseCaseTests()

const stdJSONLooseLaws = `import json
import strings

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down
type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }
type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths, Pairs Map[string, (string, int64)], Nested [][]float64 }
func codec() json.Codec[fleet] { json.Codec[fleet]{MaxBytes: 0, MaxDepth: 0} }
// looseCodec reads the laws' sample record (laws.ergo's up).
func looseCodec() json.Codec[up] { json.Codec[up]{MaxBytes: 0, MaxDepth: 0} }

// looseTricky is v with a host that holds what a repair must not touch
// inside a string: a comment, a fence, braces, quotes, a comma.
func looseTricky(v up) up {
	up{Host: v.Host + " // ` + "`" + `` + "`" + `` + "`" + `json {'x',} \"q\" /*", Port: v.Port, Tags: v.Tags, Load: v.Load}
}

// looseProse is the prose a model writes around a value: no bracket, no
// backtick.
let looseProse = []string{"", "Sure! Here it is:", "The answer is below.", "Hope this helps."}

// looseNoise is a text with noise and the sum of the fix kinds
// (looseKind) json.DecodeLoose must report for it.
type looseNoise { text string, kinds int }

func looseAt(n int, size int) int {
	let m = if n < 0 { -(n + 1) } else { n }
	m % size
}

// looseNoisy is v's text with the noise the flags name: prose before and
// after, a fence, a trailing comma, bare or single-quoted keys, a comment,
// the last brace cut, another object before or after.
func looseNoisy(v up, pre int, post int, fence bool, comma bool, bare bool, single bool, comment bool, cut bool, before bool, after bool) looseNoise {
	let mut t = json.Encode(looseCodec(), v)
	let mut kinds = []int{}
	if comment {
		// A line comment between a comma and the key after it.
		t = strings.ReplaceAll(t, ",\"port\":", ", // note\n\"port\":")
	}
	for k in []string{"host", "port", "tags", "load"} {
		if bare {
			t = strings.ReplaceAll(t, "\""+k+"\":", k+":")
		} else if single {
			t = strings.ReplaceAll(t, "\""+k+"\":", "'"+k+"':")
		}
	}
	if bare {
		kinds = kinds.Append(looseKind(json.UnquotedKey{Start: 0, End: 0, Key: ""}))
	} else if single {
		t = looseSingleValue(t, "host")
		kinds = kinds.Append(looseKind(json.SingleQuoted{Start: 0, End: 0}))
	}
	if comment {
		t = "{/* note */" + t[1:]
		kinds = kinds.Append(looseKind(json.Comment{Start: 0, End: 0}))
	}
	if comma {
		t = t[:len(t)-1] + ",}"
		kinds = kinds.Append(looseKind(json.TrailingComma{Start: 0, End: 0}))
	}
	if cut {
		t = t[:len(t)-1]
		kinds = kinds.Append(looseKind(json.Closed{Start: 0, End: 0, Depth: 0, String: false}))
	}
	if fence {
		let close = if cut { "" } else { "\n` + "`" + `` + "`" + `` + "`" + `" }
		t = "` + "`" + `` + "`" + `` + "`" + `json\n" + t + close
		kinds = kinds.Append(looseKind(json.Fenced{Start: 0, End: 0}))
	}
	let p = looseProse[looseAt(pre, len(looseProse))]
	let q = if cut { "" } else { looseProse[looseAt(post, len(looseProse))] }
	t = p + "\n" + t + "\n" + q
	let others = before || (after && !cut)
	if before {
		t = "{\"example\": true}\n" + t
	}
	if after && !cut {
		t = t + "\n{\"other\": 1}"
	}
	if others {
		kinds = kinds.Append(looseKind(json.Chose{Start: 0, End: 0, Index: 0, Of: 0}))
	}
	if others || p != "" || q != "" {
		kinds = kinds.Append(looseKind(json.Surrounded{Start: 0, End: 0}))
	}
	looseNoise{text: t, kinds: looseSum(kinds)}
}

// looseSingleValue is t with the string after the key 'k': in single
// quotes: an escaped " unescaped, a ' escaped, other escapes kept.
func looseSingleValue(t string, k string) string {
	let at = strings.Index(t, "'"+k+"':\"")
	if at < 0 {
		return t
	}
	let start = at + len(k) + 3
	let mut s = t[:start] + "'"
	let mut j = start + 1
	for j < len(t) && t[j] != '"' {
		if t[j] == '\\' && j+1 < len(t) && t[j+1] == '"' {
			s = s + "\""
			j = j + 2
		} else if t[j] == '\\' && j+1 < len(t) {
			s = s + t[j:j+2]
			j = j + 2
		} else if t[j] == '\'' {
			s = s + "\\'"
			j = j + 1
		} else {
			s = s + t[j:j+1]
			j = j + 1
		}
	}
	s + "'" + t[j+1:]
}

// looseKind is a fix's kind as one bit of a sum.
func looseKind(f json.Fix) int {
	match f {
	case json.Surrounded:
		1
	case json.Fenced:
		2
	case json.TrailingComma:
		4
	case json.UnquotedKey:
		8
	case json.SingleQuoted:
		16
	case json.Comment:
		32
	case json.Closed:
		64
	case json.Chose:
		128
	}
}

// looseSum is the sum of the distinct kinds in kinds.
func looseSum(kinds []int) int {
	let mut sum = 0
	for k in kinds {
		if sum/k%2 == 0 {
			sum = sum + k
		}
	}
	sum
}

func looseKinds(fixes []json.Fix) int {
	let mut kinds = []int{}
	for f in fixes {
		kinds = kinds.Append(looseKind(f))
	}
	looseSum(kinds)
}

// looseEdits are the bytes a mutation inserts: the syntax the repairs
// read, and others.
let looseEdits = []string{",", "}", "]", "'", "/", "*", "` + "`" + `", "\"", "a", ":", " ", "\n", "{", "\\", "//", "` + "`" + `` + "`" + `` + "`" + `", "/*"}

// looseMutated is v's text with two edits: each inserts an edit, deletes
// a byte or cuts the text, at a drawn place.
func looseMutated(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) string {
	looseEdit(looseEdit(json.Encode(looseCodec(), looseTricky(v)), at, op, ch), at2, op2, ch2)
}

func looseEdit(t string, at int, op int, ch int) string {
	let pos = looseAt(at, len(t)+1)
	let kind = looseAt(op, 3)
	if kind == 0 {
		return t[:pos] + looseEdits[looseAt(ch, len(looseEdits))] + t[pos:]
	}
	if kind == 1 && pos < len(t) {
		return t[:pos] + t[pos+1:]
	}
	t[:pos]
}

// looseNames is whether each fix names its span of text: in the text,
// and holding what the fix says it repaired.
func looseNames(text string, fixes []json.Fix) bool {
	for f in fixes {
		let ok = match f {
		case json.Surrounded as s:
			looseIn(text, s.Start, s.End) && looseOpens(text[s.Start])
		case json.Chose as c:
			looseIn(text, c.Start, c.End) && looseOpens(text[c.Start]) && c.Index < c.Of
		case json.Fenced as g:
			looseIn(text, g.Start, g.End) && text[g.Start:g.Start+3] == "` + "`" + `` + "`" + `` + "`" + `"
		case json.TrailingComma as c:
			looseIn(text, c.Start, c.End) && text[c.Start:c.End] == ","
		case json.UnquotedKey as k:
			looseIn(text, k.Start, k.End) && text[k.Start:k.End] == k.Key
		case json.SingleQuoted as q:
			looseIn(text, q.Start, q.End) && text[q.Start] == '\''
		case json.Comment as c:
			looseIn(text, c.Start, c.End) && text[c.Start] == '/'
		case json.Closed as c:
			looseIn(text, c.Start, c.End) && c.End == len(text) && c.Depth > 0 && if c.String { text[c.Start] == '"' || text[c.Start] == '\'' } else { looseOpens(text[c.Start]) }
		}
		if !ok {
			return false
		}
	}
	true
}

func looseIn(text string, start int, end int) bool { 0 <= start && start < end && end <= len(text) }

func looseOpens(c byte) bool { c == '{' || c == '[' }

// looseNamed is whether the fixes json.DecodeLoose reports for text, read as
// an up and as a Value (which takes any cut, a cut string too), name their
// spans (a text it does not read has none).
func looseNamed(text string) bool {
	let r = json.DecodeLoose(looseCodec(), text) catch {
	case _:
		return looseNamedValue(text)
	}
	looseNames(text, r.Fixes) && looseNamedValue(text)
}

func looseNamedValue(text string) bool {
	let r = json.DecodeLoose(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		return true
	}
	looseNames(text, r.Fixes)
}

// looseCutRead is whether text, a list of one string cut inside the
// string, reads as a list of a prefix of full with a json.Closed fix that says
// a string was cut.
func looseCutRead(text string, full string) bool {
	let r = json.DecodeLoose(json.Codec[[]string]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		return false
	}
	let mut cut = false
	for f in r.Fixes {
		match f {
		case json.Closed as c:
			cut = c.String && c.Depth == 1
		case _:
		}
	}
	len(r.Value) == 1 && strings.HasPrefix(full, r.Value[0]) && cut
}

// looseLetters are the string bytes CutStringsClose draws: a letter and
// the two that json.Encode escapes.
let looseLetters = []string{"a", "\"", "\\"}

// looseOpen is n list brackets, never closed.
func looseOpen(n int) string { strings.Repeat("[", n) }

// looseMalformed is whether json.DecodeLoose fails json.Malformed on text.
func looseMalformed(c json.Codec[json.Value], text string) bool {
	let _ = json.DecodeLoose(c, text) catch {
	case json.Malformed:
		return true
	case _:
		return false
	}
	false
}

func lawStrictTextsAgree(v fleet, w up) bool {
	let r = json.DecodeLoose(codec(), json.Encode(codec(), v)) catch {
	case _:
		return false
	}
	let _ = json.DecodeLoose(codec(), json.Encode(looseCodec(), w)) catch {
	case json.Malformed:
		return false
	case _:
		return r.Value == v && len(r.Fixes) == 0
	}
	false
}

test JsonStrictTextsAgree(v fleet, w up) { assert lawStrictTextsAgree(v, w) }

func lawNoiseIsReported(v up, pre int, post int, fence bool, comma bool, bare bool, single bool, comment bool, cut bool, before bool, after bool) bool {
	let w = looseTricky(v)
	let n = looseNoisy(w, pre, post, fence, comma, bare, single, comment, cut, before, after)
	let r = json.DecodeLoose(looseCodec(), n.text) catch {
	case _:
		return false
	}
	let want = if before { 1 } else { 0 }
	let of = 1 + (if before { 1 } else { 0 }) + (if after && !cut { 1 } else { 0 })
	let mut index = true
	for f in r.Fixes {
		match f {
		case json.Chose as c:
			index = c.Index == want && c.Of == of
		case _:
		}
	}
	r.Value == w && looseKinds(r.Fixes) == n.kinds && index
}

test JsonNoiseIsReported(v up, pre int, post int, fence bool, comma bool, bare bool, single bool, comment bool, cut bool, before bool, after bool) { assert lawNoiseIsReported(v, pre, post, fence, comma, bare, single, comment, cut, before, after) }

func lawNoFixMeansStrict(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) bool {
	let text = looseMutated(v, at, op, ch, at2, op2, ch2)
	let r = json.DecodeLoose(looseCodec(), text) catch {
	case _:
		return true
	}
	if len(r.Fixes) > 0 {
		return true
	}
	let s = json.Decode(looseCodec(), text) catch {
	case _:
		return false
	}
	s == r.Value
}

test JsonNoFixMeansStrict(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) { assert lawNoFixMeansStrict(v, at, op, ch, at2, op2, ch2) }

func lawRepairIsIdempotent(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) bool {
	let r = json.DecodeLoose(looseCodec(), looseMutated(v, at, op, ch, at2, op2, ch2)) catch {
	case _:
		return true
	}
	let again = json.DecodeLoose(looseCodec(), json.Encode(looseCodec(), r.Value)) catch {
	case _:
		return false
	}
	again.Value == r.Value && len(again.Fixes) == 0
}

test JsonRepairIsIdempotent(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) { assert lawRepairIsIdempotent(v, at, op, ch, at2, op2, ch2) }

func lawFixesNameTheirText(v up, pre int, fence bool, comma bool, bare bool, comment bool, cut bool, at int, op int, ch int, at2 int, op2 int, ch2 int) bool {
	let a = looseNoisy(looseTricky(v), pre, pre, fence, comma, bare, !bare, comment, cut, false, true).text
	let b = looseMutated(v, at, op, ch, at2, op2, ch2)
	looseNamed(a) && looseNamed(b)
}

test JsonFixesNameTheirText(v up, pre int, fence bool, comma bool, bare bool, comment bool, cut bool, at int, op int, ch int, at2 int, op2 int, ch2 int) { assert lawFixesNameTheirText(v, pre, fence, comma, bare, comment, cut, at, op, ch, at2, op2, ch2) }

func lawCutStringsClose(raw []byte) bool {
	let mut s = "\"\\"
	for b in raw {
		s = s + looseLetters[int(b)%3]
	}
	let text = json.Encode(json.Codec[[]string]{MaxBytes: 0, MaxDepth: 0}, []string{s})
	let mut ok = true
	for n in 2..len(text)-2 {
		ok = ok && looseCutRead(text[:n], s)
	}
	ok
}

test JsonCutStringsClose(raw []byte) { assert lawCutStringsClose(raw) }

func lawBoundsHoldWhenRepaired(k int) bool {
	let depth = looseAt(k, 32) + 1
	let r = json.DecodeLoose(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: depth}, looseOpen(depth)) catch {
	case _:
		return false
	}
	let text = "ok: [1, 2,]"
	let _ = json.DecodeLoose(json.Codec[json.Value]{MaxBytes: len(text), MaxDepth: 0}, text) catch {
	case _:
		return false
	}
	len(r.Fixes) > 0 && looseMalformed(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: depth}, looseOpen(depth+1)) && looseMalformed(json.Codec[json.Value]{MaxBytes: len(text) - 1, MaxDepth: 0}, text)
}

test JsonBoundsHoldWhenRepaired(k int) { assert lawBoundsHoldWhenRepaired(k) }

// looseCase is whether a row of the BAML conformance cases holds: input
// fails when fails, else reads as the json.Value want writes, with fixes
// of the kinds whose looseKind sum is kinds.
func looseCase(input string, want string, kinds int, refused bool) bool {
	let c = json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}
	let r = json.DecodeLoose(c, input) catch {
	case _:
		return refused
	}
	let w = json.Decode(c, want) catch {
	case _:
		return false
	}
	!refused && r.Value == w && looseKinds(r.Fixes) == kinds
}
`

// looseBAMLCase is one row of testdata/loose-baml-cases.json: an input of
// BAML's jsonish tests and what Ergo's loose reader does with it, read as
// a json.Value. State is passing (the value BAML reads), different (another
// value, on purpose) or unsupported (Ergo fails, on purpose); Want and
// Fixes are the value and fix kinds Ergo reports.
type looseBAMLCase struct {
	ID    string   `json:"id"`
	State string   `json:"state"`
	Input string   `json:"input"`
	Want  string   `json:"want"`
	Fixes []string `json:"fixes"`
	Note  string   `json:"note"`
}

// looseKindBits is each fix kind's bit in the probe's looseKind sum.
var looseKindBits = map[string]int{"Surrounded": 1, "Fenced": 2, "TrailingComma": 4, "UnquotedKey": 8, "SingleQuoted": 16, "Comment": 32, "Closed": 64, "Chose": 128}

// looseCaseNames is the probe test of each case, in the file's order.
var looseCaseNames []string

// looseCaseTests is a probe test per case: an Ergo string of its input
// and want (strconv.Quote's escapes are Ergo's), the sum of its fix kinds,
// and whether it fails. A file that does not read panics, so the build of
// the probe fails with it.
func looseCaseTests() string {
	data, err := os.ReadFile("testdata/loose-baml-cases.json")
	if err != nil {
		panic(err)
	}
	var file struct {
		Cases []looseBAMLCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		panic(err)
	}
	var b strings.Builder
	for _, c := range file.Cases {
		kinds := 0
		for _, k := range c.Fixes {
			bit, ok := looseKindBits[k]
			if !ok {
				panic(fmt.Sprintf("case %s: no fix kind %q", c.ID, k))
			}
			kinds += bit
		}
		if c.State != "passing" && c.State != "different" && c.State != "unsupported" {
			panic(fmt.Sprintf("case %s: state %q", c.ID, c.State))
		}
		name := "JsonLoose"
		for _, w := range regexp.MustCompile(`[A-Za-z0-9]+`).FindAllString(c.ID, -1) {
			name += strings.ToUpper(w[:1]) + w[1:]
		}
		looseCaseNames = append(looseCaseNames, name)
		fmt.Fprintf(&b, "\ntest %s() { assert looseCase(%s, %s, %d, %t) }\n", name, strconv.Quote(c.Input), strconv.Quote(c.Want), kinds, c.State == "unsupported")
	}
	return b.String()
}

// stdJSONLooseLawStatements is the statement of each loose law, by ledger
// key; lawledger.Run holds it equal to the ledger.
var stdJSONLooseLawStatements = map[string]string{
	"json StrictTextsAgree":       `StrictTextsAgree(v fleet, w up) { let r = DecodeLoose(codec(), Encode(codec(), v)) catch { case _: return false } let _ = DecodeLoose(codec(), Encode(looseCodec(), w)) catch { case Malformed: return false case _: return r.Value == v && len(r.Fixes) == 0 } false }`,
	"json NoiseIsReported":        `NoiseIsReported(v up, pre int, post int, fence bool, comma bool, bare bool, single bool, comment bool, cut bool, before bool, after bool) { let w = looseTricky(v) let n = looseNoisy(w, pre, post, fence, comma, bare, single, comment, cut, before, after) let r = DecodeLoose(looseCodec(), n.text) catch { case _: return false } let want = if before { 1 } else { 0 } let of = 1 + (if before { 1 } else { 0 }) + (if after && !cut { 1 } else { 0 }) let mut index = true for f in r.Fixes { match f { case Chose as c: index = c.Index == want && c.Of == of case _: } } r.Value == w && looseKinds(r.Fixes) == n.kinds && index }`,
	"json NoFixMeansStrict":       `NoFixMeansStrict(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) { let text = looseMutated(v, at, op, ch, at2, op2, ch2) let r = DecodeLoose(looseCodec(), text) catch { case _: return true } if len(r.Fixes) > 0 { return true } let s = Decode(looseCodec(), text) catch { case _: return false } s == r.Value }`,
	"json RepairIsIdempotent":     `RepairIsIdempotent(v up, at int, op int, ch int, at2 int, op2 int, ch2 int) { let r = DecodeLoose(looseCodec(), looseMutated(v, at, op, ch, at2, op2, ch2)) catch { case _: return true } let again = DecodeLoose(looseCodec(), Encode(looseCodec(), r.Value)) catch { case _: return false } again.Value == r.Value && len(again.Fixes) == 0 }`,
	"json FixesNameTheirText":     `FixesNameTheirText(v up, pre int, fence bool, comma bool, bare bool, comment bool, cut bool, at int, op int, ch int, at2 int, op2 int, ch2 int) { let a = looseNoisy(looseTricky(v), pre, pre, fence, comma, bare, !bare, comment, cut, false, true).text let b = looseMutated(v, at, op, ch, at2, op2, ch2) looseNamed(a) && looseNamed(b) }`,
	"json CutStringsClose":        `CutStringsClose(raw []byte) { let mut s = "\"\\" for b in raw { s = s + looseLetters[int(b)%3] } let text = Encode(Codec[[]string]{MaxBytes: 0, MaxDepth: 0}, []string{s}) let mut ok = true for n in 2..len(text)-2 { ok = ok && looseCutRead(text[:n], s) } ok }`,
	"json BoundsHoldWhenRepaired": `BoundsHoldWhenRepaired(k int) { let depth = looseAt(k, 32) + 1 let r = DecodeLoose(Codec[Value]{MaxBytes: 0, MaxDepth: depth}, looseOpen(depth)) catch { case _: return false } let text = "ok: [1, 2,]" let _ = DecodeLoose(Codec[Value]{MaxBytes: len(text), MaxDepth: 0}, text) catch { case _: return false } len(r.Fixes) > 0 && looseMalformed(Codec[Value]{MaxBytes: 0, MaxDepth: depth}, looseOpen(depth+1)) && looseMalformed(Codec[Value]{MaxBytes: len(text) - 1, MaxDepth: 0}, text) }`,
}

// TestStdJSONLooseLawsRun runs the loose reader's laws as the probe's
// tests with --seed 1; lawledger.Run holds that the probe restates exactly
// the ledger's laws whose runner is this test, each with the row's
// statement, and that each passed; and each BAML case's test passed. The
// json-loose-* mutants of the std mutant ledger name it. It builds Go
// because each law is an Ergo test the built probe runs, as
// TestStdJSONLawsRun's are.
func TestStdJSONLooseLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdjsonloose"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdjsonloose failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	laws := lawsPassed(t, stdJSONLooseLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdJSONLooseLawsProbe, stdout, looseCaseNames...)
	for _, name := range looseCaseNames {
		if !regexp.MustCompile(`(?m)^ok ` + name + `$`).MatchString(stdout) {
			t.Errorf("no ok line for the BAML case %s:\n%s", name, stdout)
		}
	}
	lawledger.Run(t, laws)
}
