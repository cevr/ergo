package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger/parsed"
)

// stdXmlLawsProbe restates the laws whose runner is TestStdXmlLawsRun
// as Ergo tests. A member law runs on a provider of each kind the probe
// draws; a law over a package's private helpers restates them.
const stdXmlLawsProbe = `import strings
import utf8
import xml

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down

type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }

type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths, Pairs Map[string, (string, int64)], Nested [][]float64 }

func codecOf() xml.Codec[fleet] { xml.Codec[fleet]{Root: "fleet", MaxBytes: 0, MaxDepth: 0} }

// charRune and held restate xml's private names.ergo: s as XML holds it,
// each character XML cannot hold as U+FFFD.
func charRune(r rune) bool { r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF }

func held(s string) string {
	let mut o = ""
	let mut i = 0
	for i < len(s) {
		let c = s[i]
		if c < 0x80 {
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				o = o + "\uFFFD"
			} else {
				o = o + strings.Slice(s, i, i+1)
			}
			i = i + 1
		} else {
			let d = utf8.DecodeRuneInString(strings.Slice(s, i, len(s)))
			if d.Size <= 1 {
				o = o + "\uFFFD"
				i = i + 1
			} else if !charRune(d.Rune) {
				o = o + "\uFFFD"
				i = i + d.Size
			} else {
				o = o + strings.Slice(s, i, i+d.Size)
				i = i + d.Size
			}
		}
	}
	o
}

func heldFleet(v fleet) fleet {
	let mut states = []state{}
	for s in v.States {
		states = states.Append(match s {
		case up as u:
			heldUp(u)
		case down:
			down
		})
	}
	let spare = match v.Spare {
	case None:
		None
	case Some[up] as s:
		Some[up]{Value: heldUp(s.Value)}
	}
	let mut meta = Map[string, int]{}
	for k, n in v.Meta {
		meta[held(k)] = n
	}
	let mut pairs = Map[string, (string, int64)]{}
	for k, p in v.Pairs {
		let s, n = p
		pairs[held(k)] = (held(s), n)
	}
	fleet{Name: held(v.Name), States: states, Spare: spare, Meta: meta, Sizes: v.Sizes, Pairs: pairs, Nested: v.Nested}
}

func heldUp(u up) up {
	let mut tags = []string{}
	for t in u.Tags {
		tags = tags.Append(held(t))
	}
	up{Host: held(u.Host), Port: u.Port, Tags: tags, Load: u.Load}
}

func roundTrips(v fleet) bool {
	let w = heldFleet(v)
	let back = xml.Decode(codecOf(), xml.Encode(codecOf(), w)) catch {
	case _:
		return false
	}
	back == w
}

test XmlRoundTrips(v fleet) {
	assert roundTrips(v)
}

func outputIsWellFormed(v fleet) bool {
	let root = xml.Check(xml.Encode(codecOf(), v), 0) catch {
	case _:
		return false
	}
	root == "fleet"
}

test XmlOutputIsWellFormed(v fleet) {
	assert outputIsWellFormed(v)
}

func pointsAt(m xml.Malformed, text string, offset int) bool {
	let want = xml.At(text, offset, m.Reason)
	m.Offset == offset && m.Line == want.Line && m.Column == want.Column
}

func controlCharacterIsRejectedWhereItIs(v fleet, at int) bool {
	let text = xml.Encode(codecOf(), heldFleet(v))
	let i = if at < 0 { -(at % (len(text) + 1)) } else { at % (len(text) + 1) }
	let bad = strings.Slice(text, 0, i) + "\x01" + strings.Slice(text, i, len(text))
	match xml.Check(bad, 0) catch {
	case xml.Malformed as m:
		let split = m.Offset < i && i-m.Offset < 4 && bad[m.Offset] >= 0xC0
		return (m.Offset == i || split) && pointsAt(m, bad, m.Offset)
	} {
	case _:
		false
	}
}

test XmlControlCharacterIsRejectedWhereItIs(v fleet, at int) {
	assert controlCharacterIsRejectedWhereItIs(v, at)
}

func truncationIsRejectedInsideTheText(v fleet, cut int) bool {
	let text = xml.Encode(codecOf(), heldFleet(v))
	let k = if cut < 0 { -(cut % len(text)) } else { cut % len(text) }
	let part = strings.Slice(text, 0, k)
	match xml.Check(part, 0) catch {
	case xml.Malformed as m:
		return m.Offset <= k && pointsAt(m, part, m.Offset)
	} {
	case _:
		false
	}
}

test XmlTruncationIsRejectedInsideTheText(v fleet, cut int) {
	assert truncationIsRejectedInsideTheText(v, cut)
}

type entry { ID xml.Attribute, Lang xml.Attribute, Title string, Parts []xml.Attribute, Note Option[xml.Attribute], Late xml.Attribute }

func entryOf(id string, lang string, title string, parts []string, note Option[string]) entry {
	let mut ps = []xml.Attribute{}
	for x in parts {
		ps = ps.Append(xml.Attribute{Value: held(x)})
	}
	let n = match note {
	case None:
		None
	case Some[string] as s:
		Some[xml.Attribute]{Value: xml.Attribute{Value: held(s.Value)}}
	}
	entry{ID: xml.Attribute{Value: held(id)}, Lang: xml.Attribute{Value: held(lang)}, Title: held(title), Parts: ps, Note: n, Late: xml.Attribute{Value: held(lang)}}
}

func entryCodec() xml.Codec[entry] { xml.Codec[entry]{Root: "entry", MaxBytes: 0, MaxDepth: 0} }

func attributesRoundTrip(id string, lang string, title string, parts []string, note Option[string]) bool {
	let e = entryOf(id, lang, title, parts, note)
	let back = xml.Decode(entryCodec(), xml.Encode(entryCodec(), e)) catch {
	case _:
		return false
	}
	back == e
}

test XmlAttributesRoundTrip(id string, lang string, title string, parts []string, note Option[string]) {
	assert attributesRoundTrip(id, lang, title, parts, note)
}

test XmlAttributesAreInTheStartTag(id string, lang string) {
	let e = entryOf(id, lang, "", []string{}, None)
	let text = xml.Encode(entryCodec(), e)
	let head = strings.Slice(text, 0, strings.Index(text, ">"))
	assert strings.HasPrefix(head, "<entry id=\"") && strings.Contains(head, "\" lang=\"") && !strings.Contains(text, "<id>")
}

test XmlAttributeRoundTrips(x xml.Attribute) {
	assert xml.ParseAttribute(x.Wire()) == x
}
`

// stdXmlLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdXmlLawStatements = map[string]string{
	"xml RoundTrips":                          `RoundTrips(v fleet) { let w = heldFleet(v) let back = Decode(codecOf(), Encode(codecOf(), w)) catch { case _: return false } back == w }`,
	"xml OutputIsWellFormed":                  `OutputIsWellFormed(v fleet) { let root = Check(Encode(codecOf(), v), 0) catch { case _: return false } root == "fleet" }`,
	"xml ControlCharacterIsRejectedWhereItIs": `ControlCharacterIsRejectedWhereItIs(v fleet, at int) { let text = Encode(codecOf(), heldFleet(v)) let i = if at < 0 { -(at % (len(text) + 1)) } else { at % (len(text) + 1) } let bad = strings.Slice(text, 0, i) + "\x01" + strings.Slice(text, i, len(text)) match Check(bad, 0) catch { case Malformed as m: let split = m.Offset < i && i-m.Offset < 4 && bad[m.Offset] >= 0xC0 return (m.Offset == i || split) && pointsAt(m, bad, m.Offset) } { case _: false } }`,
	"xml TruncationIsRejectedInsideTheText":   `TruncationIsRejectedInsideTheText(v fleet, cut int) { let text = Encode(codecOf(), heldFleet(v)) let k = if cut < 0 { -(cut % len(text)) } else { cut % len(text) } let part = strings.Slice(text, 0, k) match Check(part, 0) catch { case Malformed as m: return m.Offset <= k && pointsAt(m, part, m.Offset) } { case _: false } }`,
	"xml AttributesRoundTrip":                 `AttributesRoundTrip(id string, lang string, title string, parts []string, note Option[string]) { let e = entryOf(id, lang, title, parts, note) let back = Decode(entryCodec(), Encode(entryCodec(), e)) catch { case _: return false } back == e }`,
	"xml AttributesAreInTheStartTag":          `AttributesAreInTheStartTag(id string, lang string) { let e = entryOf(id, lang, "", []string{}, None) let text = Encode(entryCodec(), e) let head = strings.Slice(text, 0, strings.Index(text, ">")) strings.HasPrefix(head, "<entry id=\"") && strings.Contains(head, "\" lang=\"") && !strings.Contains(text, "<id>") }`,
	"xml AttributeRoundTrips":                 `AttributeRoundTrips(x Attribute) { ParseAttribute(x.Wire()) == x }`,
}

// stdXmlLawTestName is the probe's test of a ledger key: the last
// element of the package path, capitalized, then the law without its dot
// ("ratelimit/memory AgreesWithTheModel" is MemoryAgreesWithTheModel).
func stdXmlLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdXmlLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It builds Go because the laws run in the built test binary (ergo test), the only place a law runs.
func TestStdXmlLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdxml"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdxml failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	parsed.Run(t, lawsPassed(t, stdXmlLawStatements, stdXmlLawTestName, stdXmlLawsProbe, stdout))
}
