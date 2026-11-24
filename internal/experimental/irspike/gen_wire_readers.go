package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// wireReaderSites are GenerateWireReaders's functions with an IR body in
// every seed: take and cut decode on the prelude's Reader interface.
var wireReaderSites = []string{"take", "cut", "direct"}

// wireReaderDecls are the types GenerateWireReaders decodes and the
// functions that decode and print them: records of basics (narrow
// integers, whose range is checked), a nested record, a list of records,
// a map and a list of ints (PORT-WIRE-READERS R1's forms).
const wireReaderDecls = `type In { A int8, B uint8, S string, F bool, X float64 }

type Shape = Point | Arc

type Point { X int }

type Arc { Radius int, Label json.Optional[string] }

type Ev {
	N int, Name string, In In, Xs []In, M Map[string, int], Ts []int,
	Extra json.Optional[int], Shape Shape, Level Level, Tags Tags, Limit Small,
	Duo (json.Nullable[int], int),
	At time.Time, Data json.Value, Raw json.Raw,
}

func showIn(i In) string { "(\(i.A) \(i.B) \(i.S) \(i.F) \(i.X))" }

func showExtra(extra json.Optional[int]) string {
	match extra {
	case json.Absent:
		"absent"
	case json.Present[int] as x:
		"extra \(x.Value)"
	}
}

func showShape(s Shape) string {
	match s {
	case Point as p:
		"point \(p.X)"
	case Arc as a:
		match a.Label {
		case json.Absent:
			"arc \(a.Radius) unlabelled"
		case json.Present[string] as x:
			"arc \(a.Radius) \(x.Value)"
		}
	}
}

func showDuo(duo (json.Nullable[int], int)) string {
	let (first, second) = duo
	match first {
	case json.Null:
		"null \(second)"
	case json.NonNull[int] as x:
		"\(x.Value) \(second)"
	}
}

func show(e Ev) string {
	let mut s = "\(e.N) \(e.Name) \(showIn(e.In)) ["
	for x in e.Xs {
		s = s + " " + showIn(x)
	}
	let mut sum = 0
	for k, v in e.M {
		sum = sum + v + len(k)
	}
	let mut ts = 0
	for t in e.Ts {
		ts = ts * 3 + t
	}
	let mut tags = ""
	for t in e.Tags.xs {
		tags = tags + "[" + t + "]"
	}
	let data = json.Encode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, e.Data)
	s + " ] \(len(e.M)) \(sum) \(len(e.Ts)) \(ts) \(showExtra(e.Extra)) \(showShape(e.Shape)) \(e.Level.Wire()) \(tags) \(int(e.Limit)) \(showDuo(e.Duo)) \(e.At.UnixSeconds()) \(data) \(e.Raw.String())"
}

// take decodes on the Reader interface: Ev's decoder function and the
// ones it calls read through the prelude's Reader.
func take(r Reader) string {
	let e = decode[Ev](r) catch {
	case WireWrongKind as w:
		return "kind \(w.Path) \(w.Want)"
	case WireMissingField as m:
		return "missing \(m.Path) \(m.Field)"
	case WireUnknownTag as u:
		return "tag \(u.Path) \(u.Tag)"
	case UnknownLevel as u:
		return "level \(u.Name)"
	case Unsatisfied as u:
		return "refinement \(u.Type) \(u.Where)"
	case time.ParseError:
		return "time parse"
	case json.Malformed:
		return "number malformed"
	}
	show(e)
}

func read(text string) string {
	let r = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	take(r)
}

// direct decodes on json's Reader itself, a direct reader (R2a): Ev's
// decoder function and the ones it calls read by json.Reader's own
// functions, and print what take prints.
func direct(text string) string {
	let r = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	let e = decode[Ev](r) catch {
	case WireWrongKind as w:
		return "kind \(w.Path) \(w.Want)"
	case WireMissingField as m:
		return "missing \(m.Path) \(m.Field)"
	case WireUnknownTag as u:
		return "tag \(u.Path) \(u.Tag)"
	case UnknownLevel as u:
		return "level \(u.Name)"
	case Unsatisfied as u:
		return "refinement \(u.Type) \(u.Where)"
	case time.ParseError:
		return "time parse"
	case json.Malformed:
		return "number malformed"
	}
	show(e)
}

// cut decodes text, encodes the value to events and decodes the first k
// of them (k mod their count + 1) on a second reader, codec's Replay,
// through the same take: a text cut short mid-decode, which json.Open,
// checking the text whole, never lets reach the decoder. Replay reads
// EndObject past its end, so the decoder fails at "event k".
func cut(text string, k int) string {
	let r = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	let e = decode[Ev](r) catch {
	case _:
		return "bad"
	}
	let w = codec.NewEvents()
	encode(e, w)
	let evs = codec.Recorded(w)
	take(codec.NewReplay(evs[0:k % (len(evs) + 1)]))
}

`

// GenerateWireReaders is a program that decodes drawn JSON texts into Ev
// on the prelude's Reader (PORT-WIRE-READERS R1): texts that read, with
// their keys in a drawn order, an unknown key or a key given twice, and
// texts that fail: a field missing (at the top or in a nested record), a
// value of the wrong kind, an int8 or a uint8 out of its range, and a
// truncated text (Malformed at json.Open); each text that reads is also
// decoded cut short at three drawn events on codec's Replay (cut), the
// decoder's own mid-decode failures. Each failure prints its value and its place, so a
// failure at another read, or another failure, changes the output.
func GenerateWireReaders(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x6a09e667f3bcc909))
	n := func(k int) int { return r.IntN(k) }
	q := func(s string) string { return `\"` + s + `\"` }
	in := func(bad int) string {
		fields := []string{
			q("a") + ": " + fmt.Sprint([]int{n(200) - 100, 128, -129}[map[bool]int{true: 1 + n(2), false: 0}[bad == 1]]),
			q("b") + ": " + fmt.Sprint([]int{n(256), 256, -1}[map[bool]int{true: 1 + n(2), false: 0}[bad == 2]]),
			q("s") + ": " + q([]string{"", "x", "a b"}[n(3)]),
			q("f") + ": " + []string{"true", "false"}[n(2)],
			q("x") + ": " + []string{"1.5", "0", "-2.25"}[n(3)],
		}
		if bad == 3 {
			fields[2] = q("s") + ": 7" // a number where the string is
		}
		if bad == 4 {
			fields = fields[:3] // f and x missing
		}
		r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
		return "{" + strings.Join(fields, ", ") + "}"
	}
	// text is Ev's JSON with failure bad at the top (0: none) and, when
	// bad is 1, failure inBad in its nested record.
	text := func(bad, inBad int) string {
		var xs []string
		for i := n(3); i > 0; i-- {
			xs = append(xs, in(0))
		}
		tag := q("_tag") + ": "
		shape := "{" + q("x") + ": " + fmt.Sprint(n(9)) + ", " + tag + q("Point") + "}"
		switch bad {
		case 11:
			shape = "{" + tag + q("Arc") + ", " + q("radius") + ": " + fmt.Sprint(n(9)) + ", " + q("label") + ": " + q("arc") + "}"
		case 12:
			shape = "{" + tag + q("Unknown") + "}"
		case 13:
			shape = "{}"
		case 14:
			shape = "{" + tag + q("Point") + "}"
		case 15:
			shape = "[]"
		case 16:
			shape = "{" + tag + q("Arc") + ", " + q("radius") + ": " + q("not an int") + "}"
		}
		level, tags, limit := q([]string{"debug", "info", "warn"}[n(3)]), "["+q("short")+","+q("tag")+"]", fmt.Sprint(n(100))
		switch bad {
		case 17:
			level = q("other")
		case 18:
			level = "1"
		case 19:
			limit = "100"
		case 20:
			tags = "[" + q("ok") + ",1]"
		}
		duo := fmt.Sprintf("[null,%d]", n(100))
		switch bad {
		case 21:
			duo = fmt.Sprintf("[%d,%d]", n(100), n(100))
		case 22:
			duo = "[" + q("bad nullable payload") + ",4]"
		case 23:
			duo = "[null]"
		case 24:
			duo = "[null,4,5]"
		case 25:
			duo = "{}"
		}
		at := q(fmt.Sprintf("2026-11-23T00:%02d:00Z", n(60)))
		switch bad {
		case 26:
			at = q("not a date")
		case 27:
			at = "1"
		}
		data := "{" + q("repeat") + ":1e+02," + q("repeat") + ":[null,true," + q("s") + "]}"
		fields := []string{
			q("n") + ": " + fmt.Sprint(n(100)),
			q("name") + ": " + q([]string{"p", "", "q r"}[n(3)]),
			q("in") + ": " + in(map[bool]int{true: inBad, false: 0}[bad == 1]),
			q("xs") + ": [" + strings.Join(xs, ", ") + "]",
			q("m") + ": {" + q("k") + ": " + fmt.Sprint(n(9)) + ", " + q("kk") + ": " + fmt.Sprint(n(9)) + "}",
			q("ts") + ": [" + fmt.Sprint(n(5)) + ", " + fmt.Sprint(n(5)) + "]",
			q("shape") + ": " + shape,
			q("level") + ": " + level,
			q("tags") + ": " + tags,
			q("limit") + ": " + limit,
			q("duo") + ": " + duo,
			q("at") + ": " + at,
			q("data") + ": " + data,
			q("raw") + ": { " + q("space") + ": [ 1, 2 ] }",
		}
		switch bad {
		case 2: // a field missing at the top
			fields = append(fields[:1], fields[2:]...)
		case 3: // an unknown key
			fields = append(fields, q("zz")+": [1, {"+q("a")+": 2}]")
		case 4: // a key given twice: the last one counts
			fields = append(fields, q("n")+": "+fmt.Sprint(n(100)))
		case 5: // the wrong kind: a list where the int is
			fields[0] = q("n") + ": [1]"
		case 6: // a map value of the wrong kind
			fields[4] = q("m") + ": {" + q("k") + ": " + q("v") + "}"
		case 8: // an omittable key whose payload is present (zero is present too)
			fields = append(fields, q("extra")+": "+fmt.Sprint(n(2)))
		case 9: // null is not absence for Optional[int]
			fields = append(fields, q("extra")+": null")
		case 10: // the last omittable key wins
			fields = append(fields, q("extra")+": 3", q("extra")+": 5")
		}
		r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
		s := "{" + strings.Join(fields, ", ") + "}"
		if bad == 7 { // a truncated text
			// Cut between escapes: the text is an Ergo string literal,
			// whose \" must stay whole.
			s = strings.TrimSuffix(s[:len(s)/2], `\`)
		}
		return s
	}
	var b strings.Builder
	b.WriteString("// generated by irspike.GenerateWireReaders\n\nimport codec\nimport json\nimport time\n\n")
	b.WriteString(pairTypes)
	b.WriteString(wireReaderDecls)
	b.WriteString("func main() needs Console {\n")
	// Every program reaches every failure: each bad at the top, each of
	// the nested record's four (two out of range), and two texts that
	// read, in a drawn order.
	var cases [][2]int
	for bad := 0; bad <= 27; bad++ {
		if bad != 1 {
			cases = append(cases, [2]int{bad, 0})
		}
	}
	for inBad := 1; inBad <= 4; inBad++ {
		cases = append(cases, [2]int{1, inBad})
	}
	cases = append(cases, [2]int{0, 0})
	r.Shuffle(len(cases), func(i, j int) { cases[i], cases[j] = cases[j], cases[i] })
	for _, c := range cases {
		t := text(c[0], c[1])
		fmt.Fprintf(&b, "\tConsole.Println(read(\"%s\"))\n", t)
		fmt.Fprintf(&b, "\tConsole.Println(direct(\"%s\"))\n", t)
		for j := 0; j < 3; j++ {
			fmt.Fprintf(&b, "\tConsole.Println(cut(\"%s\", %d))\n", t, n(60))
		}
	}
	b.WriteString("}\n")
	return b.String()
}
