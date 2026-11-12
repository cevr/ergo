package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdTextLawsProbe restates each law of std text (ledger runner
// TestStdTextLawsRun) as a property test of its own name
// (stdLawsTestName), over the package's exported functions, with the
// laws' helpers near and spanNear restated.
const stdTextLawsProbe = `import strings
import text

func near(n int, k int) int {
	let m = if n < 0 { 3 } else { n + 3 }
	(k%m + m) % m - 1
}

func spanNear(s string, a int, b int) text.Span { text.Span{Start: near(len(s), a), End: near(len(s), b)} }

func wordText(s string) string {
	let alphabet = "  abcab\tc"
	let mut out = ""
	for i in 0..len(s) {
		let k = int(s[i]) % len(alphabet)
		out = out + strings.Slice(alphabet, k, k+1)
	}
	out
}

test TextCutOfWholeIsTheText(s string) {
	assert text.In(s, text.Whole(s)) && text.Cut(s, text.Whole(s)) == s && text.Width(text.Whole(s)) == len(s)
}

test TextGetIsCut(s string, a int, b int) {
	let sp = spanNear(s, a, b)
	match text.Get(s, sp) {
	case Some[string] as v:
		assert text.In(s, sp) && v.Value == text.Cut(s, sp) && len(v.Value) == text.Width(sp)
	case None:
		assert !text.In(s, sp)
	}
}

test TextSubThenCutIsCutThenSlice(s string, a int, b int, from int, to int) {
	let sp = spanNear(s, a, b)
	let f = near(text.Width(sp), from)
	let t = near(text.Width(sp), to)
	match text.Sub(s, sp, f, t) {
	case Some[text.Span] as u:
		assert text.In(s, u.Value) && text.Cut(s, u.Value) == strings.Slice(text.Cut(s, sp), f, t)
	case None:
		assert !text.In(s, sp) || f < 0 || f > t || t > text.Width(sp)
	}
}

test TextEqualAgreesWithCut(s string, a int, b int, lit string) {
	let sp = spanNear(s, a, b)
	assert text.Equal(s, sp, lit) == (text.Get(s, sp) == Some[string]{Value: lit})
	assert !text.In(s, sp) || text.Equal(s, sp, text.Cut(s, sp))
}

test TextFindIsTheFirstInside(s string, a int, b int, sub string) {
	let sp = spanNear(s, a, b)
	match text.Find(s, sp, sub) {
	case Some[text.Span] as f:
		let u = f.Value
		let first = if sub == "" { u.Start == sp.Start } else { !strings.Contains(strings.Slice(s, sp.Start, u.End-1), sub) }
		assert text.In(s, u) && sp.Start <= u.Start && u.End <= sp.End && text.Cut(s, u) == sub && first
	case None:
		assert !text.In(s, sp) || !strings.Contains(text.Cut(s, sp), sub)
	}
}

test TextFieldsAreGoFields(raw string) {
	let s = wordText(raw)
	let spans = text.Fields(s)
	let words = strings.Fields(s)
	assert len(spans) == len(words)
	let mut last = -1
	for i, sp in spans {
		assert text.In(s, sp) && text.Width(sp) > 0 && sp.Start > last && text.Cut(s, sp) == words[i]
		last = sp.End
	}
}

test TextCollectChecksEverySpan(s string, xs []int) {
	let mut at = []text.Span{}
	for i in 0..len(xs)/2 {
		at = at.Append(spanNear(s, xs[2*i], xs[2*i+1]))
	}
	let mut all = true
	for sp in at {
		all = all && text.In(s, sp)
	}
	match text.Collect(s, at) {
	case Some[text.Spans] as d:
		assert all && text.Source(d.Value) == s && text.Count(d.Value) == len(at)
		for i, sp in at {
			assert text.SpanAt(d.Value, i) == sp && text.At(d.Value, i) == text.Cut(s, sp)
		}
	case None:
		assert !all
	}
}

test TextModelAgrees(raw string, seed int, count int) {
	let s = wordText(raw)
	let n = (count%64 + 64) % 64
	let mut x = (seed%2147483648 + 2147483648) % 2147483648
	let spans = text.Fields(s)
	let words = strings.Fields(s)
	if len(spans) == 0 {
		return
	}
	let d = match text.Collect(s, spans) {
	case Some[text.Spans] as v:
		v.Value
	case None:
		assert false
		return
	}
	for _ in 0..n {
		x = (x*1103515245 + 12345) % 2147483648
		let k = (x / 65536) % len(spans)
		let sp = spans[k]
		let w = words[k]
		let from = (x / 8) % (len(w) + 1)
		let to = from + (x / 512) % (len(w) - from + 1)
		match (x / 2) % 4 {
		case 0:
			assert text.Cut(s, sp) == w && text.Get(s, sp) == (Some[string]{Value: w})
		case 1:
			match text.Sub(s, sp, from, to) {
			case Some[text.Span] as u:
				assert text.Cut(s, u.Value) == strings.Slice(w, from, to)
			case None:
				assert false
			}
		case 2:
			let sub = strings.Slice(w, from, to)
			match text.Find(s, sp, sub) {
			case Some[text.Span] as f:
				assert f.Value.Start-sp.Start == strings.Index(w, sub) && text.Cut(s, f.Value) == sub
			case None:
				assert false
			}
		case _:
			assert text.At(d, k) == w && text.SpanAt(d, k) == sp && text.Equal(s, sp, w)
		}
	}
}
`

// stdTextLawStatements is the statement of each text law, as the ledger
// writes it, by ledger key.
var stdTextLawStatements = map[string]string{
	"text CutOfWholeIsTheText":      `CutOfWholeIsTheText(s string) { In(s, Whole(s)) && Cut(s, Whole(s)) == s && Width(Whole(s)) == len(s) }`,
	"text GetIsCut":                 `GetIsCut(s string, a int, b int) { let sp = spanNear(s, a, b) match Get(s, sp) { case Some[string] as v: In(s, sp) && v.Value == Cut(s, sp) && len(v.Value) == Width(sp) case None: !In(s, sp) } }`,
	"text SubThenCutIsCutThenSlice": `SubThenCutIsCutThenSlice(s string, a int, b int, from int, to int) { let sp = spanNear(s, a, b) let f = near(Width(sp), from) let t = near(Width(sp), to) match Sub(s, sp, f, t) { case Some[Span] as u: In(s, u.Value) && Cut(s, u.Value) == strings.Slice(Cut(s, sp), f, t) case None: !In(s, sp) || f < 0 || f > t || t > Width(sp) } }`,
	"text EqualAgreesWithCut":       `EqualAgreesWithCut(s string, a int, b int, lit string) { let sp = spanNear(s, a, b) Equal(s, sp, lit) == (Get(s, sp) == Some[string]{Value: lit}) && (!In(s, sp) || Equal(s, sp, Cut(s, sp))) }`,
	"text FindIsTheFirstInside":     `FindIsTheFirstInside(s string, a int, b int, sub string) { let sp = spanNear(s, a, b) match Find(s, sp, sub) { case Some[Span] as f: let u = f.Value let first = if sub == "" { u.Start == sp.Start } else { !strings.Contains(strings.Slice(s, sp.Start, u.End-1), sub) } In(s, u) && sp.Start <= u.Start && u.End <= sp.End && Cut(s, u) == sub && first case None: !In(s, sp) || !strings.Contains(Cut(s, sp), sub) } }`,
	"text FieldsAreGoFields":        `FieldsAreGoFields(raw string) { let s = wordText(raw) let spans = Fields(s) let words = strings.Fields(s) if len(spans) != len(words) { return false } let mut ok = true let mut last = -1 for i, sp in spans { ok = ok && In(s, sp) && Width(sp) > 0 && sp.Start > last && Cut(s, sp) == words[i] last = sp.End } ok }`,
	"text CollectChecksEverySpan":   `CollectChecksEverySpan(s string, xs []int) { let mut at = []Span{} for i in 0..len(xs)/2 { at = at.Append(spanNear(s, xs[2*i], xs[2*i+1])) } let mut all = true for sp in at { all = all && In(s, sp) } match Collect(s, at) { case Some[Spans] as d: let mut ok = all && Source(d.Value) == s && Count(d.Value) == len(at) for i, sp in at { ok = ok && SpanAt(d.Value, i) == sp && At(d.Value, i) == Cut(s, sp) } ok case None: !all } }`,
	"text ModelAgrees":              `ModelAgrees(raw string, seed int, count int) { let s = wordText(raw) let n = (count%64 + 64) % 64 let mut x = (seed%2147483648 + 2147483648) % 2147483648 let spans = Fields(s) let words = strings.Fields(s) if len(spans) == 0 { return true } let d = match Collect(s, spans) { case Some[Spans] as v: v.Value case None: return false } for _ in 0..n { x = (x*1103515245 + 12345) % 2147483648 let k = (x / 65536) % len(spans) let sp = spans[k] let w = words[k] let from = (x / 8) % (len(w) + 1) let to = from + (x / 512) % (len(w) - from + 1) match (x / 2) % 4 { case 0: if Cut(s, sp) != w || Get(s, sp) != (Some[string]{Value: w}) { return false } case 1: match Sub(s, sp, from, to) { case Some[Span] as u: if Cut(s, u.Value) != strings.Slice(w, from, to) { return false } case None: return false } case 2: let sub = strings.Slice(w, from, to) let want = strings.Index(w, sub) match Find(s, sp, sub) { case Some[Span] as f: if f.Value.Start-sp.Start != want || Cut(s, f.Value) != sub { return false } case None: return false } case _: if At(d, k) != w || SpanAt(d, k) != sp || !Equal(s, sp, w) { return false } } } true }`,
}

// TestStdTextLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's text laws, each with
// the row's statement, and that each passed. The span mutants in
// testdata/mutants.json are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdTextLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdtext", stdTextLawsProbe, stdTextLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
