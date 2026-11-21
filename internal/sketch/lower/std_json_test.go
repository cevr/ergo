package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdJSONLawsProbe restates the laws of std json's laws.ergo as Ergo
// property tests (S-json), with the package's private types written out
// again, and adds one test the std laws cannot state, since std declares
// no refined type and no by-name pair: JsonFastRefinedPair (a refined
// field and a by-name pair are written by the fast path as by the Writer
// path, and read back).
const stdJSONLawsProbe = `import json
import time
import utf8

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down
type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }
type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths, Pairs Map[string, (string, int64)], Nested [][]float64 }

func codec() json.Codec[fleet] { json.Codec[fleet]{MaxBytes: 0, MaxDepth: 0} }

test JsonRoundTrips(v fleet) {
	let back = json.Decode(codec(), json.Encode(codec(), v)) catch {
	case _:
		assert false
		v
	}
	assert back == v
}

func viaWriter[T wire](v T) string needs wired[T] {
	let t = json.NewText()
	encode(v, t)
	t.Written()
}

test JsonFastAgreesWithWriter(v fleet) {
	let t = json.NewText()
	t.BeginList(3)
	encode(v, t)
	t.Int(2)
	encode(v, t)
	t.EndList()
	let w = json.NewText()
	w.BeginList(3)
	encode(v, w)
	w.Int(2)
	encode(v, w)
	w.EndList()
	let h = json.NewHTMLText()
	encode(v, h)
	let g = json.NewHTMLText()
	encode(v, g)
	assert t.Written() == w.Written() && h.Written() == g.Written()
	assert json.Encode(codec(), v) == viaWriter(v)
}

test JsonStringBytesReadsString(s string, pre []byte) {
	let t = json.NewText()
	t.String(s)
	let doc = t.Written()
	let a = json.Open(doc, 0, 0) catch {
	case _:
		assert false
		return
	}
	let b = json.Open(doc, 0, 0) catch {
	case _:
		assert false
		return
	}
	let got = a.StringBytes(pre) catch {
	case _:
		assert false
		return
	}
	let want = b.String() catch {
	case _:
		assert false
		return
	}
	assert utf8.String(got) == utf8.String(pre)+want
}

test JsonStringBytesIsString(drawn []byte, html bool) {
	let b = utf8.AppendRune(utf8.AppendRune(drawn, '<'), '&')
	let t = if html { json.NewHTMLText() } else { json.NewText() }
	t.Int(1)
	t.StringBytes(b)
	t.Int(2)
	let u = if html { json.NewHTMLText() } else { json.NewText() }
	u.Int(1)
	u.String(utf8.String(b))
	u.Int(2)
	assert t.Written() == u.Written()
}

// stamped holds time.Time, a by-name pair of the append form (GX10).
type stamped { At time.Time, Ended Option[time.Time], Times []time.Time }

test JsonTimePair(sec int64, nsec int64, more []int64) {
	let t = time.NewTime(sec, nsec)
	let mut times = []time.Time{}
	for n in more {
		times = times.Append(time.NewTime(n, n))
	}
	let v = stamped{At: t, Ended: Some{Value: t}, Times: times}
	let c = json.Codec[stamped]{MaxBytes: 0, MaxDepth: 0}
	let text = json.Encode(c, v)
	assert text == viaWriter(v)
	let back = json.Decode(c, text) catch {
	case _:
		assert false
		v
	}
	assert back == v
}

test JsonLineSeparatorsEscaped {
	assert json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, "a\u2028b\u2029") == "\"a\\u2028b\\u2029\""
}

test JsonHTMLEscaped {
	let h = json.NewHTMLText()
	h.String("<&>")
	let t = json.NewText()
	t.String("<&>")
	assert h.Written() == "\"\\u003c\\u0026\\u003e\"" && t.Written() == "\"<&>\""
}

test JsonInvalidBytesEscaped {
	assert json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, "a\xffb") == "\"a\\udcffb\""
}

test JsonTruncatedFails(v fleet, cut int) {
	let text = json.Encode(codec(), v)
	let n = if cut < 0 { -(cut + 1) } else { cut }
	let short = text[:n%len(text)]
	let _ = json.Decode(codec(), short) catch {
	case json.Malformed:
		return
	case _:
		assert false
		return
	}
	assert false
}

func nest(n int) string {
	let mut s = ""
	for _ in 0..n {
		s = "[" + s + "]"
	}
	s
}

test JsonDepthLimits(k int) {
	let n = if k < 0 { -(k + 1) } else { k }
	let depth = n%64 + 1
	let _ = json.Open(nest(depth), 0, depth) catch {
	case _:
		assert false
		return
	}
	let _ = json.Open(nest(depth+1), 0, depth) catch {
	case json.Malformed:
		return
	}
	assert false
}

type port int where 1 <= it && it <= 65535
type listen { Port port, Hosts []Host, Spare Option[Host] }

// Host has a by-name wire form: its text.
type Host { name string }
type badHost { Text string }

func ParseHost(s string) Host fails badHost {
	if s == "" {
		return badHost{Text: s}
	}
	Host{name: s}
}

func Wire(h Host) string { h.name }

// NewHost is Host's constructor, its own generator, so the pair's derived
// law HostRoundTrips runs over the hosts it makes (types.md, Codecs 7):
// never empty, as ParseHost requires.
func NewHost(s string) Host { Host{name: "h" + s} }

test JsonFastRefinedPair(names []string, p int) {
	let mut hosts = []Host{}
	for s in names {
		if s != "" {
			hosts = hosts.Append(Host{name: s})
		}
	}
	let at = parsePort(if p < 1 { 1 } else if p > 65535 { 65535 } else { p }) catch {
	case _:
		assert false
		return
	}
	let spare Option[Host] = if len(hosts) > 0 { Some{Value: hosts[0]} } else { None }
	let v = listen{Port: at, Hosts: hosts, Spare: spare}
	let c = json.Codec[listen]{MaxBytes: 0, MaxDepth: 0}
	let text = json.Encode(c, v)
	assert text == viaWriter(v)
	let back = json.Decode(c, text) catch {
	case _:
		assert false
		v
	}
	assert back == v
}
`

// stdJSONLawStatements is each restated law's ledger statement.
var stdJSONLawStatements = map[string]string{
	"json StringBytesReadsString": `StringBytesReadsString(s string, pre []byte) { let t Text = text{comma: false, html: false, out: []byte{}} t.String(s) let doc = t.Written() let a = Open(doc, 0, 0) catch { case _: return false } let b = Open(doc, 0, 0) catch { case _: return false } let got = a.StringBytes(pre) catch { case _: return false } let want = b.String() catch { case _: return false } utf8.String(got) == utf8.String(pre)+want }`,
	"json StringBytesIsString":    `StringBytesIsString(drawn []byte, html bool) { let b = utf8.AppendRune(utf8.AppendRune(drawn, '<'), '&') let t Text = text{comma: false, html: html, out: []byte{}} t.Int(1) t.StringBytes(b) t.Int(2) let u Text = text{comma: false, html: html, out: []byte{}} u.Int(1) u.String(utf8.String(b)) u.Int(2) t.Written() == u.Written() }`,
	"json DepthLimits":            `DepthLimits(k int) { let n = if k < 0 { -(k + 1) } else { k } let depth = n%64 + 1 let _ = Open(nest(depth), 0, depth) catch { case _: return false } let _ = Open(nest(depth+1), 0, depth) catch { case Malformed: return true } false }`,
	"json FastAgreesWithWriter":   `FastAgreesWithWriter(v fleet) { let t Text = text{comma: false, html: false, out: []byte{}} t.BeginList(3) encode(v, t) t.Int(2) encode(v, t) t.EndList() let w = NewText() w.BeginList(3) encode(v, w) w.Int(2) encode(v, w) w.EndList() let h Text = text{comma: false, html: true, out: []byte{}} encode(v, h) let g = NewHTMLText() encode(v, g) t.Written() == w.Written() && h.Written() == g.Written() && Encode(codec(), v) == viaWriter(v) }`,
	"json HTMLEscaped":            `HTMLEscaped() { let h = NewHTMLText() h.String("<&>") let t = NewText() t.String("<&>") h.Written() == "\"\\u003c\\u0026\\u003e\"" && t.Written() == "\"<&>\"" }`,
	"json InvalidBytesEscaped":    `InvalidBytesEscaped() { Encode(Codec[string]{MaxBytes: 0, MaxDepth: 0}, "a\xffb") == "\"a\\udcffb\"" }`,
	"json LineSeparatorsEscaped":  `LineSeparatorsEscaped() { Encode(Codec[string]{MaxBytes: 0, MaxDepth: 0}, "a\u2028b\u2029") == "\"a\\u2028b\\u2029\"" }`,
	"json RoundTrips":             `RoundTrips(v fleet) { let back = Decode(codec(), Encode(codec(), v)) catch { case _: return false } back == v }`,
	"json TruncatedFails":         `TruncatedFails(v fleet, cut int) { let text = Encode(codec(), v) let n = if cut < 0 { -(cut + 1) } else { cut } let short = text[:n%len(text)] let _ = Decode(codec(), short) catch { case Malformed: return true case _: return false } false }`,
}

// TestStdJSONLawsRun runs the json probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed, and the extra test passes too.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdJSONLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdjson"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdjson failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	extra := []string{"JsonFastRefinedPair", "JsonTimePair"}
	laws := lawsPassed(t, stdJSONLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdJSONLawsProbe, stdout, extra...)
	for _, name := range extra {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(\d+ cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
	// The Host pair's derived law runs, not skips: NewHost is Host's own
	// generator (types.md, Codecs 7; GX12).
	if !regexp.MustCompile(`(?m)^ok law HostRoundTrips\b`).MatchString(stdout) {
		t.Errorf("the pair's derived law HostRoundTrips did not run:\n%s", stdout)
	}
	lawledger.Run(t, laws)
}
