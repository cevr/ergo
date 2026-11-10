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

func viaWriter[T wire](v T) string {
	let t = json.NewText()
	let w Writer = t
	encode(v, w)
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
	let r Writer = w
	r.BeginList(3)
	encode(v, r)
	r.Int(2)
	encode(v, r)
	r.EndList()
	let h = json.NewHTMLText()
	encode(v, h)
	let g = json.NewHTMLText()
	let gw Writer = g
	encode(v, gw)
	assert t.Written() == w.Written() && h.Written() == g.Written()
	assert json.Encode(codec(), v) == viaWriter(v)
}

test JsonTruncatedFails(v fleet, cut int) {
	let text = json.Encode(codec(), v)
	let n = if cut < 0 { -(cut + 1) } else { cut }
	let short = text[:n % len(text)]
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
	let depth = n % 64 + 1
	let _ = json.Open(nest(depth), 0, depth) catch {
	case _:
		assert false
		return
	}
	let _ = json.Open(nest(depth + 1), 0, depth) catch {
	case json.Malformed:
		return
	}
	assert false
}

type port int where 1 <= it && it <= 65535
type listen { Port port, Hosts []host, Spare Option[host] }

// host has a by-name wire form: its text.
type host { name string }
type badHost { Text string }

func parseHost(s string) host fails badHost {
	if s == "" {
		return badHost{Text: s}
	}
	host{name: s}
}

func Wire(h host) string { h.name }

test JsonFastRefinedPair(names []string, p int) {
	let mut hosts = []host{}
	for s in names {
		if s != "" {
			hosts = hosts.Append(host{name: s})
		}
	}
	let at = parsePort(if p < 1 { 1 } else if p > 65535 { 65535 } else { p }) catch {
	case _:
		assert false
		return
	}
	let spare Option[host] = if len(hosts) > 0 { Some{Value: hosts[0]} } else { None }
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
	"json DepthLimits":          `DepthLimits(k int) { let n = if k < 0 { -(k + 1) } else { k } let depth = n % 64 + 1 let _ = Open(nest(depth), 0, depth) catch { case _: return false } let _ = Open(nest(depth + 1), 0, depth) catch { case Malformed: return true } false }`,
	"json FastAgreesWithWriter": `FastAgreesWithWriter(v fleet) { let t = NewText() t.BeginList(3) encode(v, t) t.Int(2) encode(v, t) t.EndList() let w = NewText() let r Writer = w r.BeginList(3) encode(v, r) r.Int(2) encode(v, r) r.EndList() let h = NewHTMLText() encode(v, h) let g = NewHTMLText() let gw Writer = g encode(v, gw) t.Written() == w.Written() && h.Written() == g.Written() && Encode(codec(), v) == viaWriter(v) }`,
	"json RoundTrips":           `RoundTrips(v fleet) { let back = Decode(codec(), Encode(codec(), v)) catch { case _: return false } back == v }`,
	"json TruncatedFails":       `TruncatedFails(v fleet, cut int) { let text = Encode(codec(), v) let n = if cut < 0 { -(cut + 1) } else { cut } let short = text[:n % len(text)] let _ = Decode(codec(), short) catch { case Malformed: return true case _: return false } false }`,
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
	extra := []string{"JsonFastRefinedPair"}
	laws := lawsPassed(t, stdJSONLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdJSONLawsProbe, stdout, extra...)
	for _, name := range extra {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(\d+ cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
	lawledger.Run(t, laws)
}
