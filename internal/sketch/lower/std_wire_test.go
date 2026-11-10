package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdWireLawsProbe restates the laws of std wire's laws.ergo as Ergo
// property tests over the derived encode and decode (types.md, Codecs;
// L11), with the package's private types written out again, and adds two
// tests the std laws cannot state, since std declares no refined type and
// no by-name pair: WireRefinedParses (a refined field decodes through its
// ParseN, and a value outside its where fails Unsatisfied) and
// WireByNamePair (a type with ParseX and Wire encodes as Wire's value and
// decodes through ParseX, whose failure is in the row).
const stdWireLawsProbe = `import wire

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down
type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }
type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths }

func depth(e wire.Event) int {
	match e {
	case wire.BeginListEvent, wire.BeginObjectEvent: 1
	case wire.EndListEvent, wire.EndObjectEvent: -1
	case _: 0
	}
}

test WireEventsReplay(v fleet) {
	let back = wire.RoundTrip(v) catch {
	case _:
		assert false
		v
	}
	assert back == v
}

func many[T codable](vs []T) ([]T, Option[T]) fails wired[Option[T]] {
	let e = wire.NewEvents()
	encode(vs, e)
	let first Option[T] = if len(vs) > 0 { Some{Value: vs[0]} } else { None }
	encode(first, e)
	let r = wire.NewReplay(wire.Recorded(e))
	(try decode[[]T](r), try decode[Option[T]](r))
}

test WireGenericReplay(vs []state) {
	let (back, first) = many(vs) catch {
	case _:
		assert false
		return
	}
	let want Option[state] = if len(vs) > 0 { Some{Value: vs[0]} } else { None }
	assert back == vs && first == want
}

test WireEncodeNests(v fleet) {
	let e = wire.NewEvents()
	encode(v, e)
	let mut open = 0
	let mut ok = true
	for ev in wire.Recorded(e) {
		open = open + depth(ev)
		if open < 0 {
			ok = false
		}
	}
	assert ok && open == 0
}

test WireMissingFieldFails() {
	let empty = []wire.Event{wire.BeginObjectEvent{N: 0}, wire.EndObjectEvent}
	let missing = decode[up](wire.NewReplay(empty)) catch {
	case WireMissingField:
		return
	case _:
		assert false
		up{Host: "", Port: 0, Tags: []string{}, Load: 0.0}
	}
	assert missing.Host == "never"
}

test WireUnknownTagFails(name string) {
	if name == "up" {
		return
	}
	if name == "down" {
		return
	}
	let evs = []wire.Event{wire.BeginObjectEvent{N: 0}, wire.TagEvent{Name: name}, wire.EndObjectEvent}
	let s = decode[state](wire.NewReplay(evs)) catch {
	case WireUnknownTag:
		return
	case _:
		assert false
		down
	}
	assert s == up{Host: "never", Port: 0, Tags: []string{}, Load: 0.0}
}

type port int where 1 <= it && it <= 65535
type listen { Port port, Hosts []host }

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

test WireRefinedParses(n int64) {
	let evs = []wire.Event{wire.IntEvent{V: n}}
	let p = decode[port](wire.NewReplay(evs)) catch {
	case Unsatisfied:
		assert n < 1 || n > 65535
		return
	case WireWrongKind:
		assert false
		return
	}
	assert int(p) >= 1 && int(p) <= 65535
	assert int64(int(p)) == n
}

test WireByNamePair(names []string) {
	let mut hosts = []host{}
	for s in names {
		if s != "" {
			hosts = hosts.Append(host{name: s})
		}
	}
	let v = listen{Port: 8080, Hosts: hosts}
	let e = wire.NewEvents()
	encode(v, e)
	let back = decode[listen](wire.NewReplay(wire.Recorded(e))) catch {
	case _:
		assert false
		v
	}
	assert back == v
	let bad = []wire.Event{wire.BeginObjectEvent{N: 2}, wire.KeyEvent{K: "port"}, wire.IntEvent{V: 80}, wire.KeyEvent{K: "hosts"}, wire.BeginListEvent{N: 1}, wire.StringEvent{V: ""}, wire.EndListEvent, wire.EndObjectEvent}
	let refused = decode[listen](wire.NewReplay(bad)) catch {
	case badHost:
		return
	case _:
		assert false
		v
	}
	assert refused.Port == 1
}
`

// stdWireLawStatements is each restated law's ledger statement.
var stdWireLawStatements = map[string]string{
	"wire EncodeNests":       "EncodeNests(v fleet) { let e = NewEvents() encode(v, e) let mut open = 0 let mut ok = true for ev in Recorded(e) { open = open + depth(ev) if open < 0 { ok = false } } ok && open == 0 }",
	"wire EventsReplay":      "EventsReplay(v fleet) { let back = RoundTrip(v) catch { case _: return false } back == v }",
	"wire GenericReplay":     "GenericReplay(vs []state) { let (back, first) = many(vs) catch { case _: return false } let want Option[state] = if len(vs) > 0 { Some{Value: vs[0]} } else { None } back == vs && first == want }",
	"wire MissingFieldFails": "MissingFieldFails() { let empty = []Event{BeginObjectEvent{N: 0}, EndObjectEvent} let _ = decode[up](NewReplay(empty)) catch { case WireMissingField: return true case _: return false } false }",
	"wire UnknownTagFails":   `UnknownTagFails(name string) { if name == "up" { return true } if name == "down" { return true } let evs = []Event{BeginObjectEvent{N: 0}, TagEvent{Name: name}, EndObjectEvent} let _ = decode[state](NewReplay(evs)) catch { case WireUnknownTag: return true case _: return false } false }`,
}

// TestStdWireLawsRun runs the wire probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed, and the two extra tests pass too.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdWireLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdwire"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdwire failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	extra := []string{"WireRefinedParses", "WireByNamePair"}
	laws := lawsPassed(t, stdWireLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdWireLawsProbe, stdout, extra...)
	for _, name := range extra {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(\d+ cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
	lawledger.Run(t, laws)
}
