package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdCodecLawsProbe restates the laws of std codec's laws.ergo as Ergo
// property tests over the derived encode and decode (types.md, Codecs;
// L11), with the package's private types written out again, and adds two
// tests the std laws cannot state, since std declares no refined type and
// no by-name pair: CodecRefinedParses (a refined field decodes through its
// ParseN, and a value outside its where fails Unsatisfied) and
// CodecByNamePair (a type with ParseX and Wire encodes as Wire's value and
// decodes through ParseX, whose failure is in the row, both through
// generic codecs, so the caller's witness carries the pair). The pair's
// derived law hostRoundTrips (types.md, Codecs 7) runs here too; it checks
// the pair, not the codec, so CodecByNamePair stays.
const stdCodecLawsProbe = `import codec

type up { Host string, Port int, Tags []string, Load float64 }
type down {}
type state = up | down
type widths { A int8, B int16, C int32, D int64, E uint8, F uint16, G uint32, H uint64, I int, J uint, K float32, L bool }
type fleet { Name string, States []state, Spare Option[up], Meta Map[string, int], Sizes widths }

func depth(e codec.Event) int {
	match e {
	case codec.BeginListEvent, codec.BeginObjectEvent: 1
	case codec.EndListEvent, codec.EndObjectEvent: -1
	case _: 0
	}
}

test CodecEventsReplay(v fleet) {
	let back = codec.RoundTrip(v) catch {
	case _:
		assert false
		v
	}
	assert back == v
}

test CodecBasicReplay(s string, b bool, i int8, u uint8, f float64) {
	let e = codec.NewEvents()
	encode(s, e)
	encode(b, e)
	encode(i, e)
	encode(u, e)
	encode(f, e)
	let r = codec.NewReplay(codec.Recorded(e))
	let s2 = decode[string](r) catch {
	case _:
		assert false
		return
	}
	let b2 = decode[bool](r) catch {
	case _:
		assert false
		return
	}
	let i2 = decode[int8](r) catch {
	case _:
		assert false
		return
	}
	let u2 = decode[uint8](r) catch {
	case _:
		assert false
		return
	}
	let f2 = decode[float64](r) catch {
	case _:
		assert false
		return
	}
	assert s2 == s && b2 == b && i2 == i && u2 == u && f2 == f
}

func many[T wire](vs []T) ([]T, Option[T]) fails wired[Option[T]] {
	let e = codec.NewEvents()
	encode(vs, e)
	let first Option[T] = if len(vs) > 0 { Some{Value: vs[0]} } else { None }
	encode(first, e)
	let r = codec.NewReplay(codec.Recorded(e))
	(try decode[[]T](r), try decode[Option[T]](r))
}

test CodecGenericReplay(vs []state) {
	let (back, first) = many(vs) catch {
	case _:
		assert false
		return
	}
	let want Option[state] = if len(vs) > 0 { Some{Value: vs[0]} } else { None }
	assert back == vs && first == want
}

test CodecEncodeNests(v fleet) {
	let e = codec.NewEvents()
	encode(v, e)
	let mut open = 0
	let mut ok = true
	for ev in codec.Recorded(e) {
		open = open + depth(ev)
		if open < 0 {
			ok = false
		}
	}
	assert ok && open == 0
}

test CodecMissingFieldFails() {
	let empty = []codec.Event{codec.BeginObjectEvent{N: 0}, codec.EndObjectEvent}
	let missing = decode[up](codec.NewReplay(empty)) catch {
	case WireMissingField:
		return
	case _:
		assert false
		up{Host: "", Port: 0, Tags: []string{}, Load: 0.0}
	}
	assert missing.Host == "never"
}

test CodecUnknownTagFails(name string) {
	if name == "up" {
		return
	}
	if name == "down" {
		return
	}
	let evs = []codec.Event{codec.BeginObjectEvent{N: 0}, codec.TagEvent{Name: name}, codec.EndObjectEvent}
	let s = decode[state](codec.NewReplay(evs)) catch {
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

test CodecRefinedParses(n int64) {
	let evs = []codec.Event{codec.IntEvent{V: n}}
	let p = decode[port](codec.NewReplay(evs)) catch {
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

// replayed decodes a T from evs in a generic function, so a pair's codec
// runs through the witness its caller's package builds.
func replayed[T wire](evs []codec.Event) T fails wired[T] {
	decode[T](codec.NewReplay(evs))
}

test CodecByNamePair(names []string) {
	let mut hosts = []host{}
	for s in names {
		if s != "" {
			hosts = hosts.Append(host{name: s})
		}
	}
	let v = listen{Port: 8080, Hosts: hosts}
	let back = codec.RoundTrip(v) catch {
	case _:
		assert false
		v
	}
	assert back == v
	let bad = []codec.Event{codec.BeginObjectEvent{N: 2}, codec.KeyEvent{K: "port"}, codec.IntEvent{V: 80}, codec.KeyEvent{K: "hosts"}, codec.BeginListEvent{N: 1}, codec.StringEvent{V: ""}, codec.EndListEvent, codec.EndObjectEvent}
	let refused = replayed[listen](bad) catch {
	case badHost:
		return
	case _:
		assert false
		v
	}
	assert refused.Port == 1
}
`

// stdCodecLawStatements is each restated law's ledger statement.
var stdCodecLawStatements = map[string]string{
	"codec BasicReplay":       "BasicReplay(s string, b bool, i int8, u uint8, f float64) { let e = NewEvents() encode(s, e) encode(b, e) encode(i, e) encode(u, e) encode(f, e) let r = NewReplay(Recorded(e)) let s2 = decode[string](r) catch { case _: return false } let b2 = decode[bool](r) catch { case _: return false } let i2 = decode[int8](r) catch { case _: return false } let u2 = decode[uint8](r) catch { case _: return false } let f2 = decode[float64](r) catch { case _: return false } s2 == s && b2 == b && i2 == i && u2 == u && f2 == f }",
	"codec EncodeNests":       "EncodeNests(v fleet) { let e = NewEvents() encode(v, e) let mut open = 0 let mut ok = true for ev in Recorded(e) { open = open + depth(ev) if open < 0 { ok = false } } ok && open == 0 }",
	"codec EventsReplay":      "EventsReplay(v fleet) { let back = RoundTrip(v) catch { case _: return false } back == v }",
	"codec GenericReplay":     "GenericReplay(vs []state) { let (back, first) = many(vs) catch { case _: return false } let want Option[state] = if len(vs) > 0 { Some{Value: vs[0]} } else { None } back == vs && first == want }",
	"codec MissingFieldFails": "MissingFieldFails() { let empty = []Event{BeginObjectEvent{N: 0}, EndObjectEvent} let _ = decode[up](NewReplay(empty)) catch { case WireMissingField: return true case _: return false } false }",
	"codec UnknownTagFails":   `UnknownTagFails(name string) { if name == "up" { return true } if name == "down" { return true } let evs = []Event{BeginObjectEvent{N: 0}, TagEvent{Name: name}, EndObjectEvent} let _ = decode[state](NewReplay(evs)) catch { case WireUnknownTag: return true case _: return false } false }`,
}

// TestStdCodecLawsRun runs the wire probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed, and the two extra tests pass too.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdCodecLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdcodec"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdcodec failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	extra := []string{"CodecRefinedParses", "CodecByNamePair"}
	laws := lawsPassed(t, stdCodecLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdCodecLawsProbe, stdout, extra...)
	for _, name := range extra {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(\d+ cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
	lawledger.Run(t, laws)
}
