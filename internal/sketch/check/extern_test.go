package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// p3Text is the P3 message (check-v1 spec add-on), quoted exactly.
const p3Text = "an extern function is called directly; wrap it in a literal: func() { f() }"

// TestExecStopTakes fixes the post-Stop use diagnostic. It kills CU4b's
// lost-takes signature mutant: the generic ownership property cannot
// detect a missing contract in a particular standard-library declaration.
func TestExecStopTakes(t *testing.T) {
	_, diags := check.CheckSource(`import exec
func stopped(p own exec.Running) int {
	_ = p.Stop()
	p.Wait()
}
func main() {}
`)
	if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "Stop takes it") {
		t.Fatalf("want S108 after Stop takes p at line 4, got %v", diags)
	}
}

// p3Named is the P3 message that names the extern and the literal to pass
// (K4, G-ex-f-3): "Upper is an extern and is not a value; pass
// func(s string) string { Upper(s) } (grammar.md, Extern)".
var p3Named = regexp.MustCompile(`^([\w.]+) is an extern and is not a value; pass func\([^{]*\)[^{]* \{ ([\w.]+)\([\w, ]*\) \} \(grammar\.md, Extern\)$`)

// isP3 reports the P3 message in either form: the named form when the
// extern's literal can be spelled, the plain text otherwise.
func isP3(msg string) bool {
	if msg == p3Text {
		return true
	}
	m := p3Named.FindStringSubmatch(msg)
	return m != nil && m[1] == m[2]
}

// judgeWant checks src as accept leg 2 judges a rule: exactly its
// `// want CODE` lines, one diagnostic per code on its line, and no cascade.
// In these programs every S108 is the P3 refusal, so each carries the P3
// message, quoted exactly.
func judgeWant(t *testing.T, src string) []check.Diagnostic {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
	}
	for _, d := range diags {
		if d.Code == "S108" && !isP3(d.Message) {
			t.Errorf("%s: want the P3 message", d)
		}
	}
	return diags
}

// TestExternRules is the A2 table of check-v1 stage E4: the call-only rule
// (C25, P3) and the std imports (C27).
func TestExternRules(t *testing.T) {
	cases := map[string]string{
		// probe stdvalue (the former lower TestLowerE1StdValueRefused):
		// the reference is invalid after its S108, so mk adds nothing.
		"std-instance-value": `import queue

func main() needs Console {
	let mk = queue.New[int] // want S108
	let q = mk(1)
	q.End()
	Console.Println("made")
}
`,
		"std-generic-uninstantiated": `import queue

func main() {
	let mk = queue.New // want S108
	_ = mk
}
`,
		// probe slots: "abc".Upper is a method lookup, not the extern. (The
		// probe's value slots, S108 before G1, are TestExternCallOnlyDomain's
		// property now.)
		"user-extern-slots": `extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let b = (Upper)("x")
	let e = "abc".Upper // want S104
	let g = "abc".Upper() // want S104
	Console.Println(b)
}
`,
		// probe methodvalue: xs.Append keeps its S104; a std and a prelude
		// extern as values are P3; h == h then meets an invalid operand.
		"std-and-prelude-values": `import scope

func main() needs Console {
	let xs = []int{1}
	let f = xs.Append // want S104
	let g = scope.OnExit[Console] // want S108
	let h = Sleep // want S104
	Console.Println("\(len(f(2)))")
	g(func() { Console.Println("x") })
	Console.Println("\(h == h)")
}
`,
		"scope-onexit-value": `import scope

func main() needs Console {
	let f = scope.OnExit[Console] // want S108
	_ = f
}
`,
		"prelude-sleep-value": `func main() {
	let h = Sleep // want S104
	_ = h
}
`,
		"wrapping-literal": `import slices

extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let f = func() string { Upper("x") }
	let g = func(n int) []int { slices.WithCapacity[int](n) }
	Console.Println(f())
	_ = g
}
`,
		"direct-call-forms": `import queue

extern go strings.ToUpper func Upper(s string) string

func main() needs Console {
	let a = Upper("x")
	let b = (Upper)("x")
	let c = ((Upper))("x")
	let q = queue.New[int](1)
	let r = (queue.New[int])(1)
	q.End()
	r.End()
	Console.Println(a + b + c)
}
`,
		"append-uncalled": `func main() {
	let xs = []int{1}
	let f = xs.Append // want S104
	_ = f
}
`,
		// Satisfaction through an extern operation (196m) is a direct call
		// by the conversion's adapter, not a value use.
		"live-scheduler": `func wait(s Scheduler) { s.Sleep(1s) }

func main() needs Ambient {
	wait(NewLiveScheduler())
}
`,
		// C27: lower-v1 3b-std landed task, scope, slice and queue; each
		// import checks clean.
		"import-task": `import task

func main() needs Console {
	let t = task.Go() { 1 }
	Console.Println("\(t.Wait())")
}
`,
		"import-scope": `import scope

func main() needs Console {
	scope.OnExit() { Console.Println("bye") }
}
`,
		"import-slice": `import slices

func main() needs Console {
	let xs = slices.WithCapacity[int](2)
	Console.Println("\(len(xs))")
}
`,
		"import-queue": `import queue

func main() needs Console {
	let q = queue.New[int](1)
	q.End()
	Console.Println("ok")
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeWant(t, src) })
	}
	for _, rule := range []string{"Esub4-extern-call-only.pos", "Esub4-extern-call-only.neg"} {
		t.Run(rule, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "v1", rule+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			judgeWant(t, string(data))
		})
	}
}

// shadowImporter serves a program package at every path it holds.
type shadowImporter map[string]*check.Package

func (s shadowImporter) Import(path string) (*check.Package, error) {
	if p, ok := s[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

// judgeOutside checks src as judgeWant does, except that every diagnostic
// is an S109 whose message holds each of texts.
func judgeOutside(t *testing.T, src string, texts ...string) {
	t.Helper()
	_, diags := check.CheckSource(src)
	want, have := expected(src), got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
	}
	for _, d := range diags {
		for _, text := range texts {
			if d.Code != "S109" || !strings.Contains(d.Message, text) {
				t.Errorf("%s: want an S109 naming %q", d, text)
			}
		}
	}
}

// queueMine is a program record whose functions satisfy queue.Queue[int]
// (196m): the probe convstd's declarations.
const queueMine = `import queue

type Mine {}

func Push(_ Mine, v int) fails queue.Closed {}

func Pop(_ Mine) int fails queue.Closed {
	0
}

func End(_ Mine) {}

func Values(_ Mine) Seq[int] {
	queue.New[int](0).Values()
}

func Poll(_ Mine) Option[int] {
	None
}

func Len(_ Mine) int {
	0
}
`

// sealedPrograms holds, for each operation type of std and the prelude, a
// program whose value of type Mine satisfies it and is passed to a
// parameter of that type at the one line that starts "\tuse(". A std
// operation type with no row here fails TestSealingIsTheVisibilityRule, and so does
// a row whose type std no longer declares.
var sealedPrograms = map[string]string{
	"atomic.Int64": `import atomic
type Mine {}
func Load(_ Mine) int64 { 0 }
func Store(_ Mine, value int64) {}
func Add(_ Mine, delta int64) int64 { delta }
func Swap(_ Mine, value int64) int64 { value }
func CompareAndSwap(_ Mine, old int64, value int64) bool { false }
func use(c atomic.Int64) { _ = c.Load() }
func main() {
	use(Mine)
}
`,
	"atomic.Bool": `import atomic
type Mine {}
func Load(_ Mine) bool { false }
func Store(_ Mine, value bool) {}
func Swap(_ Mine, value bool) bool { value }
func CompareAndSwap(_ Mine, old bool, value bool) bool { false }
func use(f atomic.Bool) { _ = f.Load() }
func main() {
	use(Mine)
}
`,
	"embed.FS": `import embed
import fs
type Mine {} implements fs.Files
func ReadFile(_ Mine, path string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ Mine, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.Info{Name: path, Size: 0, Dir: false, Perm: 292} }

func Lstat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.Info{Name: path, Size: 0, Dir: false, Perm: 292} }
func use(f embed.FS) { _ = f.ReadDir(".") catch { case fs.NotFound, fs.Denied, fs.Failed: []string{} } }
func main() {
	use(Mine)
}
`,
	"queue.Queue": queueMine + `
func use(q queue.Queue[int]) { q.End() }

func main() {
	use(Mine)
}
`,
	// std json (S-json): NewText's extern returns the Text, open's the
	// Cursor.
	"json.Text": `import json

type Mine {}

func Bool(_ Mine, b bool)       {}
func Int(_ Mine, n int64)       {}
func Uint(_ Mine, n uint64)     {}
func Float(_ Mine, f float64)   {}
func String(_ Mine, s string)   {}
func StringBytes(_ Mine, b []byte) {}
func BeginList(_ Mine, n int)   {}
func EndList(_ Mine)            {}
func BeginObject(_ Mine, n int) {}
func Tag(_ Mine, name string)   {}
func Key(_ Mine, k string)      {}
func EndObject(_ Mine)          {}
func Null(_ Mine)                {}
func Number(_ Mine, text string) {}
func Written(_ Mine) string     { "" }
func raw(_ Mine, text string)    {}
func sep(_ Mine)                 {}
func commaDue(_ Mine) bool       { false }
func setComma(_ Mine, due bool)  {}
func html(_ Mine) bool           { false }
func keyBody(_ Mine, k string)   {}
func stringBody(_ Mine, s string) {}
func bytesBody(_ Mine, b []byte) {}
func intBody(_ Mine, n int64)    {}
func uintBody(_ Mine, n uint64)  {}
func boolBody(_ Mine, b bool)    {}
func floatBody(_ Mine, f float64) {}

func use(t json.Text) { t.EndList() }

func main() {
	use(Mine)
}
`,
	// std explore (T2c): size is unexported, so only explore's hasher
	// provides Hasher.
	"explore.Hasher": `import explore

type Mine {}

func Null(_ Mine)                  {}
func Number(_ Mine, text string)   {}
func Bool(_ Mine, b bool)          {}
func Int(_ Mine, n int64)          {}
func Uint(_ Mine, n uint64)        {}
func Float(_ Mine, f float64)      {}
func String(_ Mine, s string)      {}
func StringBytes(_ Mine, b []byte) {}
func BeginList(_ Mine, n int)      {}
func EndList(_ Mine)               {}
func BeginObject(_ Mine, n int)    {}
func Tag(_ Mine, name string)      {}
func Key(_ Mine, k string)         {}
func EndObject(_ Mine)             {}
func Sum(_ Mine) explore.Hash      { explore.Hash{Hi: uint64(0), Lo: uint64(0)} }
func size(_ Mine) int              { 0 }

func use(h explore.Hasher) { _ = h.Sum() }

func main() {
	use(Mine)
}
`,
	// std xml (XML-STD): Text is sealed by its private parts (raw, open,
	// close, enter, finish), so its one provider, text, is a cell that
	// encode writes by direct calls.
	"xml.Text": `import xml

type Mine {}

func Null(_ Mine)                 {}
func Number(_ Mine, text string)  {}
func Bool(_ Mine, b bool)         {}
func Int(_ Mine, n int64)         {}
func Uint(_ Mine, n uint64)       {}
func Float(_ Mine, f float64)     {}
func String(_ Mine, s string)     {}
func StringBytes(_ Mine, b []byte) {}
func BeginList(_ Mine, n int)     {}
func EndList(_ Mine)              {}
func BeginObject(_ Mine, n int)   {}
func Tag(_ Mine, name string)     {}
func Key(_ Mine, k string)        {}
func EndObject(_ Mine)            {}
func Written(_ Mine) string       { "" }
func raw(_ Mine, s string)        {}
func open(_ Mine, name string)    {}
func close(_ Mine)                {}
func enter(_ Mine)                {}
func finish(_ Mine, hint int) string { "" }

func use(t xml.Text) { t.EndList() }

func main() {
	use(Mine)
}
`,
	// std xml (XML-STD): Cursor is the prelude's Reader sealed (seal), so
	// its one provider, reader, is a cell decode[T] may borrow.
	"xml.Cursor": `import xml

type Mine {}

func TakeNull(_ Mine) bool              { false }
func Peek(_ Mine) WireKind              { WireNull }
func Number(_ Mine) string              { "" }
func Path(_ Mine) string                { "" }
func Bool(_ Mine) bool                  { false }
func Int(_ Mine) int64                  { 0 }
func Uint(_ Mine) uint64                { 0 }
func Float(_ Mine) float64              { 0.0 }
func String(_ Mine) string              { "" }
func StringBytes(_ Mine, into []byte) []byte { into }
func BeginList(_ Mine)                  {}
func BeginObject(_ Mine)                {}
func Tag(_ Mine) string                 { "" }
func More(_ Mine) bool                  { false }
func Key(_ Mine) string                 { "" }
func Skip(_ Mine)                       {}
func End(_ Mine)                        {}
func seal(_ Mine)                       {}

func use(c xml.Cursor) { c.End() }

func main() {
	use(Mine)
}
`,
	"json.Cursor": `import json

type Mine {}

func Path(_ Mine) string             { "" }
func TakeNull(_ Mine) bool           { false }
func Kind(_ Mine) int                { 0 }
func Number(_ Mine) string           { "" }
func Bool(_ Mine) bool               { false }
func Int(_ Mine) int64               { 0 }
func Uint(_ Mine) uint64             { 0 }
func Float(_ Mine) float64           { 0.0 }
func String(_ Mine) string           { "" }
func StringBytes(_ Mine, into []byte) []byte { into }
func BeginList(_ Mine) bool          { false }
func BeginObject(_ Mine) bool        { false }
func Tag(_ Mine) string              { "" }
func Wrong(_ Mine) bool              { false }
func More(_ Mine) bool               { false }
func Key(_ Mine) string              { "" }
func Skip(_ Mine)                    {}
func End(_ Mine)                     {}

func use(c json.Cursor) { c.End() }

func main() {
	use(Mine)
}
`,
	// std cmp (CONSTRAINTS C3): an order a program writes.
	"cmp.Order": `import cmp

type Mine {} implements cmp.Order[int]

func Compare(_ Mine, a int, b int) int { a - b }

func use(o cmp.Order[int]) { _ = o.Compare(1, 2) }

func main() {
	use(Mine)
}
`,
	// The prelude's Map provider (CONSTRAINTS C5).
	"prelude.Hashing": `type Mine {} implements Hashing[int]

func Hash(_ Mine, k int) uint64 { uint64(k) }
func Equal(_ Mine, a int, b int) bool { a == b }

func use(h Hashing[int]) { _ = h.Equal(1, 2) }

func main() {
	use(Mine)
}
`,
	// std sync (sync-ref): NewRef's extern returns the Ref.
	"sync.Ref": `import sync

type Mine {}

func Get(_ Mine) int { 0 }
func Set(_ Mine, v int) {}
func Update(_ Mine, f func(v int) int) {}

func use(r sync.Ref[int]) { _ = r.Get() }

func main() {
	use(Mine)
}
`,
	// std actor (MU3): start's extern returns the Actor.
	"actor.Actor": `import actor

type Mine {}

type Bad {}

func Send(_ Mine, e int) fails actor.Stopped {}
func Ask(_ Mine, e int) int fails actor.Stopped | Bad { 0 }
func State(_ Mine) int { 0 }
func Stop(_ Mine) fails Bad {}
func Dropped(_ Mine) int { 0 }
func Changes(_ Mine) Seq[int] { func(yield func(v int) bool) {} }
func Drain(_ Mine) fails Bad {}

func use(a actor.Actor[int, int, int, Bad]) { _ = a.Dropped() }

func main() {
	use(Mine)
}
`,
	// std tasks (batch 13 tasks-set): newSet's extern returns the Set.
	"tasks.Set": `import tasks

type Mine {} implements Closer

func Wait(_ Mine) fails tasks.Closed {}
func Stop(_ Mine) {}
func Close(_ Mine) {}
func Len(_ Mine) int { 0 }

func use(s tasks.Set[tasks.Closed]) { _ = s.Len() }

func main() {
	use(Mine)
}
`,
	// std journal (MU9): open; journal/memory and journal/file are
	// providers that programs could write too.
	"journal.Journal": `import journal

type Mine {} implements journal.Journal[int, int]

func Append(_ Mine, e int) fails journal.Failed {}
func Snapshot(_ Mine, s int) fails journal.Failed {}
func Load(_ Mine) journal.Log[int, int] fails journal.Failed { journal.Log[int, int]{Snapshot: None, Signals: []int{}} }

func use(j journal.Journal[int, int]) {
	j.Append(1) catch {
	case journal.Failed:
	}
}

func main() {
	use(Mine)
}
`,
	"journal.Codec": `import journal

type Mine {} implements journal.Codec[int]

func Encode(_ Mine, v int) string { "" }
func Decode(_ Mine, text string) int fails journal.Failed { 0 }

func use(c journal.Codec[int]) { _ = c.Encode(1) }

func main() {
	use(Mine)
}
`,
	// std actor (mu-host): NewHost's extern returns the Host.
	"actor.Host": `import actor

type Mine {}

func Get(_ Mine) int fails actor.Stopped { 0 }

func use(h actor.Host[int]) {
	let _ = h.Get() catch {
	case actor.Stopped:
		return
	}
}

func main() {
	use(Mine)
}
`,
	// std testing (L5b): newOp's extern returns the Op of a Script.
	"testing.Op": `import testing

type Mine {}

func Answer(_ Mine, f own func() int) {}
func Calls(_ Mine) []testing.NoArgs { []testing.NoArgs{} }

func use(o testing.Op[testing.NoArgs, func() int]) { _ = o.Calls() }

func main() {
	use(Mine)
}
`,
	// std testing (SC2): a stage is any running program a test acts on.
	"testing.Stage": `import testing

type Mine {} implements testing.Stage[int, int]

func Do(_ Mine, a int) fails testing.Refused {}
func Advance(_ Mine, d duration) {}
func Settle(_ Mine) {}
func Show(_ Mine) int { 0 }

func use(s testing.Stage[int, int]) { _ = s.Show() }

func main() {
	use(Mine)
}
`,
	// HTTP-FAST-b: Scheduler.Alarms returns it; only the runtime makes one.
	"prelude.AlarmClock": `type Mine {}

func use(c AlarmClock) {}

func main() {
	use(Mine)
}
`,
	// std time (S2b): NewManual's extern returns the Timeline.
	"time.Timeline": `import time

type Mine {}

func Read(_ Mine) time.Reading { time.Reading{Sec: 0, Nsec: 0, Mono: 0} }
func Wait(_ Mine, d duration) {}
func Advance(_ Mine, d duration) {}
func Sleepers(_ Mine) int { 0 }

func use(l time.Timeline) { _ = l.Sleepers() }

func main() {
	use(Mine)
}
`,
	// std rand (S2c): Seeded's extern returns the Stream.
	"rand.Stream": `import rand

type Mine {} implements Random

func Uint64(_ Mine) uint64 { 0 }
func IntN(_ Mine, n int) int { 0 }
func Float64(_ Mine) float64 { 0.5 }

func use(s rand.Stream) { _ = s.Uint64() }

func main() {
	use(Mine)
}
`,
	// std rand (std-spike D3): Secure is open, as Random is; a program
	// provides its own (a fixed source for a test, a hardware one).
	"rand.Secure": `import rand

type Mine {} implements rand.Secure

func Bytes(_ Mine, n int) string { "" }

func use(s rand.Secure) { _ = s.Bytes(0) }

func main() {
	use(Mine)
}
`,
	"prelude.Closer": `type Mine {} implements Closer

func Close(_ Mine) {}

func use(c Closer) {}

func main() {
	use(Mine)
}
`,
	"prelude.Task": `type Fault {}
type Mine {} implements Closer

func Wait(m own Mine) int fails Fault { 0 }
func Stop(_ Mine) {}
func Close(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine)
}
`,
	"prelude.Keeping": `type Mine {}

func drain(_ Mine) []int { []int{} }

func use(_ Keeping[int]) {}

func main() {
	use(Mine)
}
`,
	"prelude.Console": `type Mine {} implements Console

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
	// Sleep is a prelude name no program redeclares, so the program value
	// is one of a program operation type.
	"prelude.Scheduler": `type Mine { ..Scheduler }

func use(s Scheduler) { s.Sleep(1ms) }

func pass(m Mine) {
	use(m)
}
`,
	// The codec events (L11): a format or a program provides each.
	"prelude.Writer": `type Mine {} implements Writer

func Null(_ Mine) {}
func Number(_ Mine, text string) {}
func Bool(_ Mine, b bool) {}
func Int(_ Mine, n int64) {}
func Uint(_ Mine, n uint64) {}
func Float(_ Mine, f float64) {}
func String(_ Mine, s string) {}
func StringBytes(_ Mine, b []byte) {}
func BeginList(_ Mine, n int) {}
func EndList(_ Mine) {}
func BeginObject(_ Mine, n int) {}
func Tag(_ Mine, name string) {}
func Key(_ Mine, k string) {}
func EndObject(_ Mine) {}

func use(w Writer) { w.Bool(true) }

func main() {
	use(Mine)
}
`,
	"prelude.Reader": `type Mine {} implements Reader

func TakeNull(_ Mine) bool { false }
func Peek(_ Mine) WireKind { WireEnd }
func Number(_ Mine) string fails WireWrongKind { "" }
func Path(_ Mine) string { "" }
func Bool(_ Mine) bool fails WireWrongKind { true }
func Int(_ Mine) int64 fails WireWrongKind { 0 }
func Uint(_ Mine) uint64 fails WireWrongKind { 0 }
func Float(_ Mine) float64 fails WireWrongKind { 0.0 }
func String(_ Mine) string fails WireWrongKind { "" }
func StringBytes(_ Mine, into []byte) []byte fails WireWrongKind { into }
func BeginList(_ Mine) fails WireWrongKind {}
func BeginObject(_ Mine) fails WireWrongKind {}
func Tag(_ Mine) string fails WireMissingField { "" }
func More(_ Mine) bool { false }
func Key(_ Mine) string { "" }
func Skip(_ Mine) {}
func End(_ Mine) {}

func use(r Reader) { _ = r.More() }

func main() {
	use(Mine)
}
`,
	// std time (S2b): the prelude's Clock moved here, with Now() time.Time.
	"time.Clock": `import time

type Mine {} implements time.Clock

func Now(_ Mine) time.Time { time.Unix(0, 0) }
func Elapsed(_ Mine, t time.Time) duration { 0s }

func use(c time.Clock) { _ = c.Now() }

func main() {
	use(Mine)
}
`,
	"log.Sink": `import log

type Mine {} implements log.Sink

func Enabled(_ Mine, l log.Level) bool { true }
func Write(_ Mine, e log.Entry) {}

func use(s log.Sink) { s.Write(log.Entry{Level: log.LevelInfo, Message: "x", Attrs: []log.Attr{}}) }

func main() {
	use(Mine)
}
`,
	"tz.Zones": `import tz

type Mine {} implements tz.Zones

func Load(_ Mine, name string) tz.Zone fails tz.UnknownZone { tz.UTC() }

func use(z tz.Zones) { _ = z.Load("UTC") catch { case tz.UnknownZone: tz.UTC() } }

func main() {
	use(Mine)
}
`,
	"tz.Local": `import tz

type Mine {} implements tz.Local

func Zone(_ Mine) tz.Zone fails tz.UnknownZone { tz.UTC() }

func use(l tz.Local) { _ = l.Zone() catch { case tz.UnknownZone: tz.UTC() } }

func main() {
	use(Mine)
}
`,
	"prelude.Random": `type Mine {} implements Random

func Uint64(_ Mine) uint64 { 0 }
func IntN(_ Mine, n int) int { 0 }
func Float64(_ Mine) float64 { 0.5 }

func use(r Random) { _ = r.IntN(1) }

func main() {
	use(Mine)
}
`,
	// std os (G2): open, as no std extern's result names it (stdlib.md,
	// os 5).
	"os.Process": `import os

type Mine {} implements os.Process

func Args(_ Mine) []string { []string{} }
func Getenv(_ Mine, key string) Option[string] { None }
func Environ(_ Mine) Map[string, string] { Map[string, string]{} }
func Getwd(_ Mine) string fails os.NoWorkdir { "/work" }

func use(p os.Process) { _ = p.Args() }

func main() {
	use(Mine)
}
`,
	// std fs (G4): open, as no std extern's result names it (stdlib.md,
	// fs 5).
	"fs.Files": `import fs

type Mine {} implements fs.Files

func ReadFile(_ Mine, path string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ Mine, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false, Perm: 0}
}

func Lstat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false, Perm: 0}
}

func use(f fs.Files) { _ = f.ReadFile("a") catch { case fs.NotFound, fs.Denied, fs.Failed: "" } }

func main() {
	use(Mine)
}
`,
	// std fs writes (G4b): open, as fs.Files is.
	"fs.Writes": `import fs

type Mine {} implements fs.Writes

func WriteFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAll(_ Mine, path string) fails fs.Denied | fs.Failed {}
func WriteFileMode(_ Mine, path string, text string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAllMode(_ Mine, path string, mode fs.Mode) fails fs.Denied | fs.Failed {}
func AppendFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Chmod(_ Mine, path string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func Rename(_ Mine, from string, to string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Remove(_ Mine, path string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Mkdir(_ Mine, path string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed | fs.Exists {}

func use(w fs.Writes) { w.MkdirAll("a") catch { case fs.Denied, fs.Failed: } }

func main() {
	use(Mine)
}
`,
	// std io (std-align S1b): open, as no std extern's result names them
	// (stdlib.md, io 6).
	"io.Stdout": `import io

type Mine {} implements io.Stdout

func Print(_ Mine, text string) {}
func Println(_ Mine, line string) {}
func IsTerminal(_ Mine) bool { false }

func use(o io.Stdout) { o.Print("x") }

func main() {
	use(Mine)
}
`,
	"io.Stderr": `import io

type Mine {} implements io.Stderr

func Eprint(_ Mine, text string) {}
func Eprintln(_ Mine, line string) {}
func IsTerminal(_ Mine) bool { false }

func use(e io.Stderr) { e.Eprintln("x") }

func main() {
	use(Mine)
}
`,
	"io.Stdin": `import io

type Mine {} implements io.Stdin

func ReadAll(_ Mine) string fails io.ReadFailed { "" }
func Lines(_ Mine) Seq[string] { io.Input{Text: ""}.Lines() }
func IsTerminal(_ Mine) bool { false }

func use(i io.Stdin) { _ = i.ReadAll() catch { case io.ReadFailed: "" } }

func main() {
	use(Mine)
}
`,
	"config.Config": `import config

type Mine {} implements config.Config

func Lookup(_ Mine, name string) Option[string] { None }
func Environ(_ Mine, prefix string) []config.Variable { []config.Variable{} }
func ReadFile(_ Mine, path string) Option[string] fails config.Unreadable { None }

func use(_ config.Config) {}

func main() {
	use(Mine)
}
`,
	"config.Redacted": `import config

type Mine {}

func Reveal(_ Mine) string { "value" }

func use(r config.Redacted[string]) { _ = r.Reveal() }

func main() {
	use(Mine)
}
`,
	"term.Terminal": `import io
import term

type Mine {} implements term.Terminal

func SetMode(_ Mine, m term.Mode) fails term.NotTerminal {}
func Restore(_ Mine) {}
func ReadKey(_ Mine) term.Key fails io.ReadFailed { term.EndOfInput }
func Size(_ Mine) term.Window fails term.NotTerminal { term.Window{Cols: 80, Rows: 24} }

func use(_ term.Terminal) {}

func main() {
	use(Mine)
}
`,
	// std ansi (UC2): open; a program may decide colour its own way, as
	// Always, Never and Detect's provider do.
	"ansi.Colors": `import ansi

type Mine {} implements ansi.Colors

func On(_ Mine) bool { true }

func use(_ ansi.Colors) {}

func main() {
	use(Mine)
}
`,
	// std exec (S3c): open, as no std extern's result names it (stdlib.md,
	// exec). exec.Script is the std's test provider of it.
	"exec.Runner": `import exec

type Mine {} implements exec.Runner

func Env(_ Mine) Map[string, string] { Map[string, string]{} }

func Run(_ Mine, c exec.Command) exec.Result fails exec.NotFound | exec.Denied | exec.Failed {
	exec.Result{Status: 0, Stdout: "", Stderr: ""}
}

func Start(_ Mine, c exec.Command, s exec.Streams) exec.Running fails exec.NotFound | exec.Denied | exec.Failed {
	try exec.Script{Results: Map[string, exec.Result]{}}.Start(c, s)
}

func use(r exec.Runner) { _ = r.Run(exec.New("true", []string{})) catch { case exec.NotFound, exec.Denied, exec.Failed: exec.Result{Status: 0, Stdout: "", Stderr: ""} } }

func main() {
	use(Mine)
}
`,
	// std ui (merged with ia2b): open; a program or a host module
	// provides each (ui.MemoryClipboard, ui.Monospace, the hosts' Host).
	"ui.Clipboard": `import ui

type Mine {} implements ui.Clipboard

func Read(_ Mine) string { "" }
func Write(_ Mine, text string) {}

func use(c ui.Clipboard) { _ = c.Read() }

func main() {
	use(Mine)
}
`,
	"ui.Measure": `import ui

type Mine {} implements ui.Measure

func Width(_ Mine, text string, font ui.Font) ui.Length { 0.0 }
func LineHeight(_ Mine, font ui.Font) ui.Length { 0.0 }

func use(m ui.Measure) { _ = m.LineHeight(ui.Font{Family: "x", Size: 1.0}) }

func main() {
	use(Mine)
}
`,
	"ui.Host": `import ui

type Mine {} implements ui.Host[int]

func Show(_ Mine, ops []ui.Op[int]) {}
func Next(_ Mine) ui.Event[int] fails ui.Closed { ui.Closed }
func Post(_ Mine, ev ui.Event[int]) {}
func Frames(_ Mine, on bool) {}

func use(h ui.Host[int]) { h.Frames(false) }

func main() {
	use(Mine)
}
`,
	// exec.Spawner (IA2b): open, as no std extern's result names it;
	// exec.Processes is the std's memory provider of it.
	"exec.Spawner": `import exec

type Mine {} implements exec.Spawner

func Spawn(_ Mine, c exec.Command, s exec.Streams) exec.Detached fails exec.NotFound | exec.Denied | exec.Failed {
	exec.Detached{Pid: 1, Start: 1, Name: c.Name}
}

func Alive(_ Mine, d exec.Detached) bool { false }

func Kill(_ Mine, d exec.Detached, grace duration) {}

func use(s exec.Spawner) { _ = s.Alive(exec.Detached{Pid: 1, Start: 1, Name: "x"}) }

func main() {
	use(Mine)
}
`,
	// exec.Running (CU4): the Script's and the Host's Start externs
	// return it, so it is sealed.
	"exec.Running": `import exec

type Mine {} implements Closer

func Lines(_ Mine) Seq[exec.Line] { Seq[exec.Line](func(yield func(v exec.Line) bool) {}) }
func Chunks(_ Mine) Seq[exec.Chunk] { Seq[exec.Chunk](func(yield func(v exec.Chunk) bool) {}) }
func Wait(_ Mine) int { 0 }
func Stop(m own Mine) int { Close(m); 0 }
func Close(_ Mine) {}

func use(r exec.Running) { _ = r.Wait() }

func main() {
	use(Mine)
}
`,
	"sha256.Hasher": `import sha256

type Mine {} implements Closer

func Write(_ Mine, s string) {}
func Digest(_ Mine) string { "" }
func Clone(_ Mine) sha256.Hasher { sha256.New() }
func Close(_ Mine) {}

func use(h sha256.Hasher) { _ = h.Digest() }

func main() {
	use(Mine)
}
`,
	"fs/mapped.Maps": `import fs
import fs/mapped

type Mine {} implements mapped.Maps

func ReadFile(_ Mine, path string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ Mine, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }
func Lstat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed { fs.NotFound{Path: path} }
func WriteFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAll(_ Mine, path string) fails fs.Denied | fs.Failed {}
func WriteFileMode(_ Mine, path string, text string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAllMode(_ Mine, path string, mode fs.Mode) fails fs.Denied | fs.Failed {}
func AppendFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Chmod(_ Mine, path string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func Rename(_ Mine, from string, to string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Remove(_ Mine, path string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Mkdir(_ Mine, path string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed | fs.Exists {}
func MapBounded(_ Mine, path string, limit int) fs.Mapped fails fs.NotFound | fs.Denied | fs.Failed | fs.NotRegular | fs.TooLarge { fs.MapText("") }
func SameAs(_ Mine, m fs.Mapped, path string) bool fails fs.NotFound | fs.Denied | fs.Failed { false }

func use(m mapped.Maps) { _ = m.SameAs }

func main() {
	use(Mine)
}
`,
	"os.Self": `import os

type Mine {} implements os.Self

func Pid(_ Mine) int { 1 }
func HomeDir(_ Mine) string fails os.NoDir { "/home" }
func CacheDir(_ Mine) string fails os.NoDir { "/home/.cache" }
func ConfigDir(_ Mine) string fails os.NoDir { "/home/.config" }
func Executable(_ Mine) string fails os.NoDir { "/bin/mine" }

func use(s os.Self) { _ = s.Pid() }

func main() {
	use(Mine)
}
`,
	"signal.Stops": `import signal

type Mine {} implements signal.Stops

func Next(_ Mine) signal.Stop { signal.Interrupt }
func Poll(_ Mine) Option[signal.Stop] { None }

func use(s signal.Stops) { _ = s.Poll() }

func main() {
	use(Mine)
}
`,
	// The prelude's Ambient (host-authority H1): its one operation is
	// unexported, so no program type implements it (sealedBy), though no
	// std extern names it (unexportedStd).
	// std net (NET1): a Network's extern returns the Listener and the
	// Conn, the runtime's; http's Listen binds the same Listener. A program
	// provides a Network (openStd), never a Listener, a Conn or a PipeNet.
	"net.Listener": `import net

type Mine {} implements Closer

func Accept(_ Mine) net.Conn fails net.Failed { net.Failed{Reason: ""} }
func Addr(_ Mine) string { "" }
func Close(_ Mine) {}

func use(l net.Listener) { _ = l.Addr() }

func main() {
	use(Mine)
}
`,
	"net.Conn": `import net

type Mine {} implements Closer

func Read(_ Mine) string fails net.Failed { "" }
func Write(_ Mine, data string) fails net.Failed {}
func LocalAddr(_ Mine) string { "" }
func RemoteAddr(_ Mine) string { "" }
func Close(_ Mine) {}

func use(c net.Conn) { _ = c.LocalAddr() }

func main() {
	use(Mine)
}
`,
	"nettest.PipeNet": `import net
import nettest

type Mine {}

func Listen(_ Mine, addr string) net.Listener fails net.ListenFailed { net.ListenFailed{Addr: addr, Reason: ""} }
func Dial(_ Mine, addr string) net.Conn fails net.DialFailed { net.DialFailed{Addr: addr, Reason: ""} }

func use(p nettest.PipeNet) { _ = p }

func main() {
	use(Mine)
}
`,
	"net.Network": `import net
import nettest

type Mine {} implements net.Network

func Listen(_ Mine, addr string) net.Listener fails net.ListenFailed { try nettest.NewPipe().Listen(addr) }
func Dial(_ Mine, addr string) net.Conn fails net.DialFailed { try nettest.NewPipe().Dial(addr) }

func use(n net.Network) { _ = n }

func main() {
	use(Mine)
}
`,
	"http.Exchange": `import http

type Mine {}

func Begin(_ Mine, status int, header []http.Field) {}
func Write(_ Mine, text string) fails http.WriteFailed {}
func End(_ Mine) {}

func use(x http.Exchange) { x.End() }

func main() {
	use(Mine)
}
`,
	// Streaming is open but its operations are http's own: Serve's live
	// provider and NewRecorder are its only providers.
	"http.Streaming": `import http

type Mine {}

func open(_ Mine, status int, header []http.Field) http.BodyWriter { Unreachable("mine") }
func close(_ Mine) string { "" }

func use(_ http.Streaming) {}

func main() {
	use(Mine)
}
`,
	// Net (H1) is open: a test binds its own transport (CU6b adds SendInto).
	"http.Net": `import http

type Mine {} implements http.Net

func Send(_ Mine, r http.Request) http.Response fails http.RequestFailed { http.Response{Status: 200, Header: []http.Field{}, Body: ""} }
func SendInto(_ Mine, r http.Request, accept http.Accept, put func(part string) fails http.SinkFailed) http.Response fails http.RequestFailed | http.StatusRefused | http.SinkFailed { http.Response{Status: 200, Header: []http.Field{}, Body: ""} }

func use(_ http.Net) {}

func main() {
	use(Mine)
}
`,
	// Network (CU5) is open: a test binds its own transport (CU6 adds
	// SendURLInto).
	"http.Network": `import http

type Mine {} implements http.Network

func SendURL(_ Mine, r http.URLRequest) http.Response fails http.RequestFailed | http.OriginRefused | http.RedirectLimit { http.Response{Status: 200, Header: []http.Field{}, Body: ""} }
func SendURLInto(_ Mine, r http.URLRequest, accept http.Accept, put func(part string) fails http.SinkFailed) http.Response fails http.RequestFailed | http.OriginRefused | http.RedirectLimit | http.StatusRefused | http.SinkFailed { http.Response{Status: 200, Header: []http.Field{}, Body: ""} }

func use(_ http.Network) {}

func main() {
	use(Mine)
}
`,
	// FetchFile's new file (CU6): only its extern makes one.
	"http.BodyFile": `import http

type Mine {}

func Put(_ Mine, part string) fails http.SinkFailed {}
func partOnly(_ Mine) {}

func use(_ http.BodyFile) {}

func main() {
	use(Mine)
}
`,
	"prelude.Ambient": `type Mine {}

func ambient(_ Mine) {}

func use(_ Ambient) {}

func main() {
	use(Mine)
}
`,
	// The test's ambient (TH1): sealed by its unexported operation, as
	// the prelude's Ambient is.
	"prelude.TestAmbient": `type Mine {}

func folder(_ Mine) string { "/" }

func use(_ TestAmbient) {}

func main() {
	use(Mine)
}
`,
	// fstest.Confined (TH1): open, an ordinary operation type with laws.
	"fstest.Confined": `import fs
import fstest

type Mine {} implements fstest.Confined

func Folder(_ Mine) string { "/" }
func ReadFile(_ Mine, path string) string fails fs.Denied { fs.Denied{Path: path} }
func ReadDir(_ Mine, path string) []string fails fs.Denied { fs.Denied{Path: path} }
func WriteFile(_ Mine, path string, text string) fails fs.Denied { fs.Denied{Path: path} }

func use(_ fstest.Confined) {}

func main() {
	use(Mine)
}
`,
	// std fs Root (G1f): open; fstest.Tree is a program-side provider.
	"fs.Root": `import fs

type Mine {} implements fs.Root

func ReadFile(_ Mine, path string) string fails fs.Escapes { fs.Escapes{Path: path} }
func ReadDir(_ Mine, path string) []string fails fs.Escapes { fs.Escapes{Path: path} }
func Stat(_ Mine, path string) fs.Info fails fs.Escapes { fs.Escapes{Path: path} }
func Name(_ Mine) string { "mine" }

func use(r fs.Root) { _ = r.Name() }

func main() {
	use(Mine)
}
`,
	// std fs HostRoot (G1f): OpenRoot's extern returns it, and its
	// unexported hostRooted keeps every program value out.
	"fs.HostRoot": `import fs

type Mine {} implements fs.Root

func ReadFile(_ Mine, path string) string fails fs.Escapes { fs.Escapes{Path: path} }
func ReadDir(_ Mine, path string) []string fails fs.Escapes { fs.Escapes{Path: path} }
func Stat(_ Mine, path string) fs.Info fails fs.Escapes { fs.Escapes{Path: path} }
func Name(_ Mine) string { "mine" }
func hostRooted(_ Mine) {}

func use(r fs.HostRoot) { _ = r.Name() }

func main() {
	use(Mine)
}
`,
	// std fs Mapped (wf10 MAPPED): MapFile and MapText return it, and its
	// runtimeOnly keeps every program value out. Mine has no Text: fs.Text
	// is never a program function's result (types.md, Borrows 8).
	"fs.Mapped": `import fs

type Mine {} implements Closer

func Close(_ Mine) {}

func use(m fs.Mapped) { _ = len(m.Text()) }

func main() {
	use(Mine)
}
`,
	"ui/layout.Measure": `import ui/layout

type Mine {} implements layout.Measure

func Widths(_ Mine, text string, size float32) layout.Intrinsic { layout.Intrinsic{Max: 0.0, Min: 0.0} }
func Height(_ Mine, text string, size float32, width float32) float32 { 0.0 }

func use(m layout.Measure) { _ = m.Height("x", 1.0, 1.0) }

func main() {
	use(Mine)
}
`,
	"io.Source": `import io

type Mine {} implements io.Source

func Read(_ Mine) Option[string] fails io.ReadFailed { None }

func use(r io.Source) { _ = r.Read() catch { case io.ReadFailed: None } }

func main() {
	use(Mine)
}
`,
	"bench.Meter": `import bench

type Mine {} implements bench.Meter

func Measure(_ Mine, b bench.Block, series []bench.Series) []bench.Sample { []bench.Sample{} }

func use(m bench.Meter) { _ = len(m.Measure(bench.Block{Side: bench.Base, Number: 0, Warm: false}, []bench.Series{})) }

func main() {
	use(Mine)
}
`,
	// std code's operation types (SEMTOOLS SEM2): a program provides each
	// one; these rows delegate to std code's own providers.
	"code.Reader": `import code

type Mine { s code.Scan } implements code.Reader

func Describe(m Mine) code.Summary { m.s.Describe() }
func Symbols(m Mine, q code.SymbolQuery) code.SymbolPage { m.s.Symbols(q) }
func Symbol(m Mine, a string) code.Decl fails code.NoSymbol | code.Ambiguous { try m.s.Symbol(a) }
func References(m Mine, q code.RefQuery) code.SitePage fails code.NoSymbol | code.Ambiguous { try m.s.References(q) }
func Calls(m Mine, q code.CallQuery) code.CallPage fails code.NoSymbol | code.Ambiguous | code.NotCallable { try m.s.Calls(q) }
func Api(m Mine, q code.ApiQuery) code.ApiView fails code.NoPackage { try m.s.Api(q) }
func Reach(m Mine, q code.ReachQuery) code.ReachPage fails code.NoSymbol | code.Ambiguous { try m.s.Reach(q) }
func MachineOf(m Mine, a string) code.MachineView fails code.NoSymbol | code.Ambiguous | code.NotMachine { try m.s.MachineOf(a) }

func use(r code.Reader) { _ = r.Describe() }

func main() {
	use(Mine{s: code.NewScan(0)})
}
`,
	// std api's Transport (API-FIRST AF1): each generated transport package
	// provides it (its Wire), so a program provides it too.
	"api.Transport": `import api

type Mine {} implements api.Transport[string, int]

func Carry(_ Mine, c string, answer own func(c string) int) int { answer(c) }

func use(t api.Transport[string, int]) { _ = t.Carry("x", func(c string) int { len(c) }) }

func main() {
	use(Mine)
}
`,
	"code.Files": `import code

type Mine { d code.Disk } implements code.Files

func Read(m Mine, path string) string fails code.Missing | code.IOFailed { try m.d.Read(path) }
func Write(m Mine, path string, text string) fails code.IOFailed { try m.d.Write(path, text) }
func Exchange(m Mine, a string, b string) fails code.Missing | code.IOFailed { try m.d.Exchange(a, b) }
func Remove(m Mine, path string) fails code.IOFailed { try m.d.Remove(path) }
func Exists(m Mine, path string) bool fails code.IOFailed { try m.d.Exists(path) }
func Lock(m Mine) fails code.Busy | code.IOFailed { try m.d.Lock() }
func Unlock(m Mine) fails code.IOFailed { try m.d.Unlock() }

func use(f code.Files) { _ = f.Exists("a") catch { case code.IOFailed: false } }

func main() {
	use(Mine{d: code.NewDisk(0)})
}
`,
	"code.Checker": `import code

type Mine {} implements code.Checker

func Check(_ Mine, sources []code.Source) code.Report fails code.CheckFailed { code.CheckFailed{Reason: "none"} }
func Format(_ Mine, text string) string fails code.CheckFailed { text }

func use(c code.Checker) { _ = c.Format("x") catch { case code.CheckFailed: "" } }

func main() {
	use(Mine)
}
`,
	"code.Planner": `import code

type Mine { s code.Session } implements code.Planner

func Plan(m Mine, c code.Change) code.Planned fails code.BadChange | code.NameTaken | code.OutsideWorkspace | code.Capture { try m.s.Plan(c) }
func PlanOf(m Mine, id string) code.Planned fails code.NoPlan { try m.s.PlanOf(id) }

func use(p code.Planner) { _ = p.PlanOf("x") catch { case code.NoPlan: return } }

func main() {
	use(Mine{s: code.NewSession(0)})
}
`,
	"code.Writer": `import code

type Mine { a code.Applier } implements code.Writer

func Apply(m Mine, id string) code.Receipt fails code.NoPlan | code.NotReady | code.Stale | code.Busy | code.IOFailed { try m.a.Apply(id) }
func Recover(m Mine) code.Recovered fails code.Busy | code.IOFailed { try m.a.Recover() }

func use(w code.Writer) { _ = w.Recover() catch { case code.Busy, code.IOFailed: return } }

func main() {
	use(Mine{a: code.NewApplier(0)})
}
`,
	// ui GroupArgs (P-ui): the frame's memo of a component's arguments,
	// runtimeOnly, made only by the composer.
	"ui.GroupArgs": `import ui

type Mine {}

func use(m ui.GroupArgs) {}

func main() {
	use(Mine)
}
`,
	"ui.Location": `import ui
type Mine {} implements ui.Location
func Current(_ Mine) ui.URL { ui.ParseURL("/") }
func Push(_ Mine, u ui.URL) {}
func Replace(_ Mine, u ui.URL) {}
func Back(_ Mine) bool { false }
func use(l ui.Location) { l.Push(ui.ParseURL("/a")) }
func main() {
	use(Mine)
}
`,
	// std pace (PACE1): open; a program provides it (pace.Default,
	// pace.Percent).
	// The store contracts of ST3 (stdlib.md, Stores): a program or a
	// module provides each one over its own store and runs the same laws.
	"jobs.Queue": `import jobs
import time

type Mine {} implements jobs.Queue[int]

func EnqueueAt(_ Mine, job int, at time.Time) int fails jobs.Failed { return jobs.Failed{Reason: "x"} }
func ReserveAt(_ Mine, now time.Time) Option[jobs.Lease[int]] fails jobs.Failed { None }
func AckAt(_ Mine, t jobs.Ticket, now time.Time) fails jobs.Unknown | jobs.Failed { return jobs.Unknown{Ticket: t} }
func FailAt(_ Mine, t jobs.Ticket, why string, now time.Time) fails jobs.Unknown | jobs.Failed { return jobs.Unknown{Ticket: t} }
func DeadLetters(_ Mine) []jobs.Dead[int] fails jobs.Failed { []jobs.Dead[int]{} }
func Count(_ Mine) jobs.Counts fails jobs.Failed { jobs.Counts{Waiting: 0, Leased: 0, Done: 0, Dead: 0} }

func use(_ jobs.Queue[int]) {}

func main() {
	use(Mine)
}
`,
	"kv.Store": `import kv

type Mine {} implements kv.Store

func Get(_ Mine, key string) Option[string] fails kv.Failed { None }
func Put(_ Mine, key string, value string) fails kv.Failed { return kv.Failed{Reason: "x"} }
func Delete(_ Mine, key string) bool fails kv.Failed { false }
func Scan(_ Mine, prefix string) Seq[kv.Entry] fails kv.Failed { return kv.Failed{Reason: "x"} }

func use(_ kv.Store) {}

func main() {
	use(Mine)
}
`,
	"proof.Delivery": `import proof

type Mine {} implements proof.Delivery

func Send(_ Mine, dest string, purpose string, code string) fails proof.Undelivered { return proof.Undelivered{Reason: "x"} }

func use(_ proof.Delivery) {}

func main() {
	use(Mine)
}
`,
	"proof.Store": `import proof
import time

type Mine {} implements proof.Store

func Issue(_ Mine, id string, d proof.Digest, expires time.Time, attempts int) fails proof.Conflict | proof.Failed { return proof.Conflict }
func Redeem(_ Mine, id string, d proof.Digest, now time.Time) fails proof.Unknown | proof.Expired | proof.Spent | proof.TooManyAttempts | proof.Wrong | proof.Failed { return proof.Unknown }
func Claim(_ Mine, key string, until time.Time, now time.Time) fails proof.Spent | proof.Failed { return proof.Spent }

func use(_ proof.Store) {}

func main() {
	use(Mine)
}
`,
	"ratelimit.Limiter": `import ratelimit
import time

type Mine {} implements ratelimit.Limiter

func TakeAt(_ Mine, key string, cost int, now time.Time) ratelimit.Grant fails ratelimit.Limited | ratelimit.BadCost { return ratelimit.BadCost{Cost: cost, Limit: 0} }

func use(_ ratelimit.Limiter) {}

func main() {
	use(Mine)
}
`,
	"session.Store": `import session
import time

type Mine {} implements session.Store

func Establish(_ Mine, d session.Digest, subject string, revision int, now time.Time) fails session.Conflict | session.Failed { return session.Conflict }
func Verify(_ Mine, d session.Digest, now time.Time) session.Verdict fails session.Failed { session.Invalid }
func Rotate(_ Mine, old session.Digest, new session.Digest, now time.Time) session.Record fails session.Conflict | session.Failed { return session.Conflict }
func Revoke(_ Mine, d session.Digest) bool fails session.Failed { false }
func RevokeAll(_ Mine, subject string, revision int) fails session.Failed { return session.Failed{Reason: "x"} }

func use(_ session.Store) {}

func main() {
	use(Mine)
}
`,
	"pace.Pacing": `import pace
type Mine {} implements pace.Pacing
func Edge(_ Mine) {}
func Hold(_ Mine) {}
func Release(_ Mine) {}
func use(p pace.Pacing) { p.Edge() }
func main() {
	use(Mine)
}
`,
	// std timeout: NewDeadline's extern makes the Deadline, and runtimeOnly seals it.
	"timeout.Deadline": `import timeout

type Mine {}

func Arm(_ Mine, d duration) {}
func Armed(_ Mine) bool { false }
func Stop(_ Mine) {}

func use(dl timeout.Deadline) { dl.Stop() }

func main() {
	use(Mine)
}
`,
	// std testing (MX-P3 F6): the authority to read the heap's counters is AllocCounter's (its unexported field and NewAllocCounter's TestAmbient); the Allocs contract itself takes a program's counter, as time.Clock takes a Manual.
	"testing.Allocs": `import testing

type Mine {} implements testing.Allocs

func PerRun(_ Mine, runs int, f func()) int { 0 }

func use(a testing.Allocs) { _ = a.PerRun(1, func() {}) }

func main() {
	use(Mine)
}
`,
	// std llm (L1): a vendor's client is a program's provider of the model contract, as llm/memory's Scripted and llm/recorded are std's.
	"llm.Model": `import llm

type Mine {} implements llm.Model

func Generate(_ Mine, r llm.Request) llm.Turn fails llm.RateLimited | llm.Overloaded | llm.Unauthorized | llm.Refused | llm.Truncated | llm.Transport | llm.InvalidRequest | llm.InvalidResponse | llm.UnsupportedSchema {
	llm.Turn{Text: "", Calls: []llm.Call{}, Stop: llm.Ended, Usage: llm.Usage{Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0}}
}
func Stream(_ Mine, r llm.Request, put func(d llm.Delta) fails llm.SinkFailed) llm.Turn fails llm.RateLimited | llm.Overloaded | llm.Unauthorized | llm.Refused | llm.Truncated | llm.Transport | llm.InvalidRequest | llm.InvalidResponse | llm.UnsupportedSchema | llm.SinkFailed {
	llm.Turn{Text: "", Calls: []llm.Call{}, Stop: llm.Ended, Usage: llm.Usage{Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0}}
}
func StreamEach(_ Mine, r llm.Request, put func(d llm.Delta)) llm.Turn fails llm.RateLimited | llm.Overloaded | llm.Unauthorized | llm.Refused | llm.Truncated | llm.Transport | llm.InvalidRequest | llm.InvalidResponse | llm.UnsupportedSchema {
	llm.Turn{Text: "", Calls: []llm.Call{}, Stop: llm.Ended, Usage: llm.Usage{Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0}}
}

func use(_ llm.Model) {}

func main() {
	use(Mine)
}
`,
	// std mcp (MCP0): a transport is a program's provider, as mcp/memory's and mcp/stdio's are std's.
	"mcp.Transport": `import mcp

type Mine {} implements mcp.Transport

func Messages(_ Mine) Seq[string] { func(yield func(m string) bool) {} }
func Send(_ Mine, message string) {}

func use(_ mcp.Transport) {}

func main() {
	use(Mine)
}
`,
	// std sql: a database's provider is a module's (sqltest.Script is std's, for tests).
	"sql.DB": `import sql

type Mine {} implements sql.DB

func Exec(_ Mine, stmt string, args []sql.Value) sql.Result fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { sql.Result{Affected: 0} }
func Query(_ Mine, stmt string, args []sql.Value) []sql.Row fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { []sql.Row{} }
func Begin(_ Mine) sql.Conn fails sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { return sql.Closed{Message: "x"} }

func use(_ sql.DB) {}

func main() {
	use(Mine)
}
`,
	"sql.Conn": `import sql

type Mine {} implements sql.Conn

func Exec(_ Mine, stmt string, args []sql.Value) sql.Result fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { sql.Result{Affected: 0} }
func Query(_ Mine, stmt string, args []sql.Value) []sql.Row fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { []sql.Row{} }
func Commit(_ Mine) fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other {}
func Rollback(_ Mine) {}
func Savepoint(_ Mine, name string) fails sql.Timeout | sql.Closed | sql.Other {}
func Release(_ Mine, name string) fails sql.Timeout | sql.Closed | sql.Other {}
func RollbackTo(_ Mine, name string) fails sql.Timeout | sql.Closed | sql.Other {}

func use(_ sql.Conn) {}

func main() {
	use(Mine)
}
`,
	"sql.Dialect": `import sql

type Mine {} implements sql.Dialect

func Classify(_ Mine, e sql.HostError) sql.Failure { sql.Other{Code: e.Code, Message: e.Message} }

func use(_ sql.Dialect) {}

func main() {
	use(Mine)
}
`,
	// Only Transaction binds Tx: a program's Tx would fake a transaction boundary.
	"sql.Tx": `import sql

type Mine {}

func Exec(_ Mine, stmt string, args []sql.Value) sql.Result fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { sql.Result{Affected: 0} }
func Query(_ Mine, stmt string, args []sql.Value) []sql.Row fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { []sql.Row{} }
func bound(_ Mine) {}

func use(_ sql.Tx) {}

func main() {
	use(Mine)
}
`,
}

// unexportedStd is the std operation types sealed by an unexported
// operation: a program value is refused at the use line by the sealedBy
// text. The other runtime protocols declare runtimeOnly for the same rule.
var unexportedStd = map[string]string{
	"prelude.Ambient":     "ambient is an unexported operation of prelude.Ambient, so only a type of package prelude implements it",
	"prelude.TestAmbient": "folder is an unexported operation of prelude.TestAmbient, so only a type of package prelude implements it",
	"prelude.Keeping":     "drain is an unexported operation of prelude.Keeping, so only a type of package prelude implements it",
	"http.Streaming":      "open is an unexported operation of http.Streaming, so only a type of package http implements it",
	"fs.HostRoot":         "hostRooted is an unexported operation of fs.HostRoot, so only a type of package fs implements it",
	"http.BodyFile":       "partOnly is an unexported operation of http.BodyFile, so only a type of package http implements it",
	"json.Text":           "raw is an unexported operation of json.Text, so only a type of package json implements it",
	"xml.Text":            "raw is an unexported operation of xml.Text, so only a type of package xml implements it",
	"explore.Hasher":      "size is an unexported operation of explore.Hasher, so only a type of package explore implements it",
	"sql.Tx":              "bound is an unexported operation of sql.Tx, so only a type of package sql implements it",
}

// openStd records the operation types that did not use the old implicit
// std seal at the migration base. Some already have private operations,
// listed in unexportedStd. Every other entry now declares its seal.
var openStd = map[string]bool{
	"api.Transport":     true, // open: each generated transport package (ergo gen rpc, cli, mcp) provides it as its Wire, in the program's own module
	"prelude.Closer":    true,
	"prelude.Console":   true,
	"prelude.Scheduler": true,
	"time.Clock":        true,
	"testing.Stage":     true,
	"prelude.Random":    true,
	"rand.Secure":       true,
	"prelude.Writer":    true,
	"prelude.Reader":    true,
	"os.Process":        true,
	"os.Self":           true,
	"fs/mapped.Maps":    true, // open: a program may provide its own store of mapped files (CAP-FILE), as fs.Files and fs.Writes are open
	"signal.Stops":      true,
	"fs.Files":          true,
	"fs.Writes":         true,
	"fs.Root":           true,
	"net.Network":       true,
	"fstest.Confined":   true,
	"io.Stdout":         true,
	"io.Stderr":         true,
	"io.Stdin":          true,
	"exec.Runner":       true,
	"exec.Spawner":      true,
	"ui.Clipboard":      true,
	"ui.Measure":        true,
	"ui.Host":           true,
	"journal.Journal":   true,
	"journal.Codec":     true,
	"http.Streaming":    true,
	"http.Net":          true,
	"http.Network":      true,
	"config.Config":     true,
	"term.Terminal":     true,
	// Declared after the migration with no private operation: a program
	// provides each one, and cmd/<name>/main.ergo binds it.
	"ui.Location": true,
	// Constraints a program's own types implement (CONSTRAINTS C3, C5).
	"cmp.Order":         true,
	"prelude.Hashing":   true,
	"ui/layout.Measure": true,
	"io.Source":         true,
	"bench.Meter":       true,
	"pace.Pacing":       true,
	"ansi.Colors":       true,
	"code.Reader":       true,
	"code.Files":        true,
	"code.Checker":      true,
	"code.Planner":      true,
	"code.Writer":       true,
	"log.Sink":          true,
	"tz.Zones":          true,
	"tz.Local":          true,
	"jobs.Queue":        true, // open: std's own providers are other packages (jobs/memory.Queue, jobs/file), and a database's queue is a module's
	"kv.Store":          true, // open: examples/kvstore's memory.Table and file.Log implement it, beside std's kv/memory and kv/file
	"proof.Delivery":    true, // open: the program sends the code (mail, SMS) by its own provider; std has only proof.Recorder, for tests
	"proof.Store":       true, // open: std's own providers are other packages (proof/memory.Store, proof/file), and a database's is a module's
	"ratelimit.Limiter": true, // open: std's provider is another package (ratelimit/memory), and a limiter shared across processes is a module's
	"session.Store":     true, // open: std's own providers are other packages (session/memory.Store, session/file), and a database's is a module's
	"testing.Allocs":    true,
	"llm.Model":         true,
	"mcp.Transport":     true, // open: std's own providers are other packages (mcp/memory, mcp/stdio), and a socket's is a module's
	"sql.DB":            true, // open: a database's provider is a module's; std has sqltest.Script for tests
	"sql.Conn":          true, // open: as sql.DB's
	"sql.Dialect":       true, // open: a database's failure codes are its module's (std has sql.Standard)
	// The layout's text measure, which a host provides with its fonts.
}

// stdOperationTypes lists, as path.Name, every exported operation type
// (a braced type of operations, not a union) the prelude and each std
// package declare, read from their source with the parser: independent of
// the checker.
func stdOperationTypes(t *testing.T) []string {
	t.Helper()
	sources := map[string]map[string]string{stdlib.PreludePath: stdlib.Prelude()}
	for _, path := range stdlib.Packages() {
		files, ok := stdlib.Package(path)
		if !ok {
			t.Fatalf("std lists %s but does not serve it", path)
		}
		sources[path] = files
	}
	var out []string
	for path, files := range sources {
		for name, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%s/%s: %v", path, name, err)
			}
			for _, d := range f.Decls {
				td, ok := d.(*syntax.TypeDecl)
				if !ok || !unicode.IsUpper([]rune(td.Name.Name)[0]) {
					continue
				}
				if it, ok := td.Spec.(*syntax.InterfaceType); ok && len(it.Union) == 0 {
					out = append(out, path+"."+td.Name.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestSealingIsTheVisibilityRule covers every std operation type and the
// assignment sites that used to require a std-only refusal. Removing a
// private operation from a runtime protocol must make this property red;
// the old extern-result seal must not hide it. Open capabilities remain
// implementable. User-package seals use the same rule (TestFieldVisibility).
func TestSealingIsTheVisibilityRule(t *testing.T) {
	cases := map[string]string{
		// probe convstd, verbatim.
		"convstd": queueMine + `
func use(q queue.Queue[int]) {
	q.End()
}

func main() needs Console {
	use(Mine) // want S109
	Console.Println("converted")
}
`,
		"written-conversion": queueMine + `
func main() {
	let q = queue.Queue[int](Mine) // want S109
	q.End()
}
`,
		"let-typed": queueMine + `
func main() {
	let q queue.Queue[int] = Mine // want S109
	q.End()
}
`,
		"tail-value": queueMine + `
func make() queue.Queue[int] {
	Mine // want S109
}
`,
		"let-elements": queueMine + `
func pair() (Mine, Mine) { (Mine, Mine) }

func main() {
	let a, b queue.Queue[int] = pair() // want S109
	a.End()
	b.End()
}
`,
		"compare": queueMine + `
func same(q queue.Queue[int]) bool {
	q == Mine // want S109
}
`,
		// A program operation type's value is a program value too.
		"program-operation-type": `import queue

type Mine {
	Push(v int) fails queue.Closed,
	Pop() int fails queue.Closed,
	End(),
	Values() Seq[int],
	Poll() Option[int],
	Len() int,
}

func use(q queue.Queue[int]) { q.End() }

func pass(m Mine) {
	use(m) // want S109
}
`,
		"task": `type Fault {}
type Mine {} implements Closer

func Wait(m own Mine) int fails Fault { 0 }
func Stop(_ Mine) {}
func Close(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine) // want S109
}
`,
		// A program's Tx would fake a transaction boundary: only
		// Transaction binds it (std sql, Operation types 1).
		"sql-tx-faked": `import sql

type Mine {} implements sql.Tx // want S106

func Exec(_ Mine, stmt string, args []sql.Value) sql.Result fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { sql.Result{Affected: 0} }
func Query(_ Mine, stmt string, args []sql.Value) []sql.Row fails sql.Unique | sql.Serialization | sql.Deadlock | sql.Timeout | sql.Closed | sql.Other { []sql.Row{} }
func bound(_ Mine) {}
`,
		// Controls: a value of the std type itself converts nothing, and
		// the prelude's Console takes a program provider.
		"queue-identity": `import queue

func use(q queue.Queue[int]) { q.End() }

func main() {
	use(queue.New[int](1))
}
`,
		"console-param": `type Mine {} implements Console

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
		"console-provider": `type Quiet {} implements Console

func Println(_ Quiet, line string) {}

let W = context { Console: Quiet }

func main() {
	with W { Console.Println("x") }
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeSealSites(t, src) })
	}
	types := stdOperationTypes(t)
	for _, key := range types {
		t.Run("enumerate/"+key, func(t *testing.T) {
			src, ok := sealedPrograms[key]
			if !ok {
				t.Fatalf("the std operation type %s has no row in sealedPrograms", key)
			}
			src = withForgedSeal(src)
			// The oracle, written by hand: the operation types a std
			// extern's result names (task.Go, queue.New) are sealed.
			isSealed := !openStd[key]
			at, uses := 0, 0
			for i, line := range strings.Split(src, "\n") {
				if strings.HasPrefix(line, "\tuse(") {
					at, uses = i+1, uses+1
				}
			}
			if uses != 1 {
				t.Fatalf("the row has %d use( lines, want 1", uses)
			}
			_, diags := check.CheckSource(src)
			if text, ok := unexportedStd[key]; ok {
				if len(diags) != 1 || diags[0].Line != at || !strings.Contains(diags[0].Message, text) {
					t.Fatalf("want one diagnostic %q at line %d, got\n\t%s", text, at, describe(diags))
				}
				return
			}
			if !isSealed {
				if len(diags) != 0 {
					t.Fatalf("want clean (a program provides %s), got\n\t%s", key, describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != at || !strings.Contains(diags[0].Message, "unexported operation") {
				t.Fatalf("want the visibility refusal at line %d, got\n\t%s", at, describe(diags))
			}
		})
	}
	for key := range sealedPrograms {
		if !sortedHas(types, key) {
			t.Errorf("sealedPrograms row %s: std declares no such operation type", key)
		}
	}
	ds := outside(t, checkedLib(t), "type Mine {}\nfunc inner(_ Mine) int { 1 }\nfunc Outer(_ Mine) int { 2 }\nfunc f() lib.Ops { Mine }\n")
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "inner is an unexported operation of lib.Ops, so only a type of package lib implements it") {
		t.Fatalf("user package visibility: %s", describe(ds))
	}
}

// withForgedSeal attempts to spoof the private operation by spelling it
// in the importing package too. Its package is part of its identity.
func withForgedSeal(src string) string {
	// After the declaration's whole line: it may end in an implements clause.
	if i := strings.Index(src, "type Mine {}"); i >= 0 {
		end := i + strings.IndexByte(src[i:]+"\n", '\n')
		src = src[:end] + "\nfunc runtimeOnly(_ Mine) {}" + src[end:]
	}
	return strings.Replace(src, "type Mine {\n", "type Mine {\n\truntimeOnly(),\n", 1)
}

func judgeSealSites(t *testing.T, src string) {
	t.Helper()
	src = withForgedSeal(strings.ReplaceAll(src, "want S109", "want S108"))
	_, ds := check.CheckSource(src)
	if want, have := expected(src), got(ds); strings.Join(want, " ") != strings.Join(have, " ") {
		t.Fatalf("want %v, got %v: %s", want, have, describe(ds))
	}
}

// TestStdProtocolOpen checks that os.Process takes program providers at
// the same assignment sites where queue.Queue's private operation refuses
// them by the ordinary visibility rule.
func TestStdProtocolOpen(t *testing.T) {
	const fake = `import os

type FakeEnv { Argv []string } implements os.Process

func Args(e FakeEnv) []string { e.Argv }

func Getenv(_ FakeEnv, key string) Option[string] { None }

func Environ(_ FakeEnv) Map[string, string] { Map[string, string]{} }

func Getwd(_ FakeEnv) string fails os.NoWorkdir { "/work" }
`
	cases := map[string]string{
		"process-context": fake + `
func run() needs os.Process { _ = os.Process.Args() }

func main() {
	with context { os.Process: FakeEnv{Argv: []string{}} } { run() }
}
`,
		"process-host-context": `import os

func run() needs os.Process { _ = os.Process.Args() }

func main() needs Ambient {
	let live = context { os.Process: os.NewHost() }
	with live { run() }
}
`,
		"process-param": fake + `
func use(p os.Process) { _ = p.Args() }

func main() needs Ambient {
	use(FakeEnv{Argv: []string{}})
	let p os.Process = os.NewHost()
	use(p)
}
`,
		// IA2 (STD-NOPRIV): a module's stateful fake implements
		// exec.Runner, its Start giving exec.Ended's Running. Mutant
		// exec-ended-unexported (std exec has no Ended) refuses it.
		"exec-runner": `import exec

type Fake { Ran Map[string, int] } implements exec.Runner

func Env(_ Fake) Map[string, string] { Map[string, string]{} }

func Run(f Fake, c exec.Command) exec.Result fails exec.NotFound | exec.Denied | exec.Failed {
	exec.Result{Status: 0, Stdout: c.Name, Stderr: ""}
}

func Start(f Fake, c exec.Command, s exec.Streams) exec.Running fails exec.NotFound | exec.Denied | exec.Failed {
	exec.Ended(try f.Run(c), s)
}

func run() needs exec.Runner { _ = exec.Runner.Run(exec.New("true", []string{})) catch { case exec.NotFound, exec.Denied, exec.Failed: exec.Result{Status: 1, Stdout: "", Stderr: ""} } }

func main() {
	with context { exec.Runner: Fake{Ran: Map[string, int]{}} } { run() }
}
`,
		// NP3c: Keeping is the release's contract; a queue implements it
		// and still never converts to it (its drain is the runtime's).
		// Mutant keeps-converts admits it.
		"keeps-value": `import queue

func use(k Keeping[int]) {}

func main() {
	let q = queue.New[int](1)
	use(q) // want S108
	q.End()
}
`,
		"queue-param": queueMine + `
func use(q queue.Queue[int]) { q.End() }

func main() {
	use(Mine) // want S109
	let q queue.Queue[int] = Mine // want S109
	q.End()
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeSealSites(t, src) })
	}
}

func sortedHas(xs []string, x string) bool {
	i := sort.SearchStrings(xs, x)
	return i < len(xs) && xs[i] == x
}

// TestCloseRefusals is C29 (check-v1 stage E4 A6; lower-v1 stage 4): every
// checked Close of an owning with's provider releases, an operation of its
// type, a generic Close and a Close on a pointer while the provider is a
// value included, so each checks clean (lower/stage4_close_test.go runs
// them). One form stays refused, once, at the provider: a generic Close
// whose type parameter is constrained by operations, whose release would
// pass witnesses no fact records (S109 naming resource ownership, closeWitnessed). A std
// or extern Close is unreachable: no std or prelude Close is an extern, and
// a user extern Close takes a named type, outside the C26 domain.
func TestCloseRefusals(t *testing.T) {
	cases := map[string]string{
		"operation": `type A { Use() }
type Releaser { ..A, ..Closer }
type R {} implements Releaser, Closer
func Use(_ R) {}
func Close(_ R) {}
func make() Releaser { R }
let W = context { A: make() }
func main() { with W { A.Use() } }
`,
		"generic": `type A { Use() }
type G[T] { Value T } implements A, Closer
func Use[T](_ G[T]) {}
func Close[T](_ G[T]) {}
let W = context { A: G[int]{Value: 1} }
func main() { with W { A.Use() } }
`,
		"pointer-close-value-provider": `type A { Use() }
type P {} implements A, Closer
func Use(_ P) {}
func Close(_ mut P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"value-close": `type A { Use() }
type P {} implements A, Closer
func Use(_ P) {}
func Close(_ P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"generic-witnessed": `type A { Use() }
type Named { Name() string }
type N {} implements Named
func Name(_ N) string { "n" }
type G[T] { Value T } implements A, Closer
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
		// Once per provider, though two withs release it.
		"generic-witnessed-once": `type A { Use() }
type Named { Name() string }
type N {} implements Named
func Name(_ N) string { "n" }
type G[T] { Value T } implements A, Closer
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} } // want S109
func main() {
	with W { A.Use() }
	with W { A.Use() }
}
`,
		// The refusal ends the provider's judgment: no release charge
		// follows it, though the Close needs what main lacks (ledger
		// mutant own-check-15: the continue after the refusal dropped).
		"generic-witnessed-no-charge": `type A { Use() }
type Named { Name() string }
type N {} implements Named
func Name(_ N) string { "n" }
type G[T] { Value T } implements A, Closer
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) needs Console { Console.Println("closed") }
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, "resource ownership") })
	}
	t.Run("user-extern-close", func(t *testing.T) {
		judgeOutside(t, "type R {} implements Closer\nextern go example.com/x.F func Close(r R) // want S109\n", "Go interop")
	})
	t.Run("no-std-extern-close", func(t *testing.T) {
		prog, ds := check.CheckSource("func f(c Console) { c.Println(\"x\") }\n")
		if len(ds) != 0 {
			t.Fatal(describe(ds))
		}
		var pkgs []*check.Package
		for id, obj := range prog.Info.Uses {
			if tn, ok := obj.(*check.TypeName); ok && id.Name == "Console" {
				pkgs = append(pkgs, tn.Pkg)
			}
		}
		if len(pkgs) != 1 || !pkgs[0].Std() {
			t.Fatalf("the prelude package: %v", pkgs)
		}
		for _, path := range stdlib.Packages() {
			pkg, err := check.Std.Import(path)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			pkgs = append(pkgs, pkg)
		}
		// The walk sees std's externs (queue.New, Sleep, …), so it would
		// see an extern Close.
		closes, externs := 0, 0
		for _, pkg := range pkgs {
			for _, f := range pkg.Funcs {
				if f.Decl != nil && f.Decl.Extern != nil {
					externs++
				}
				if f.Name != "Close" {
					continue
				}
				closes++
				if f.Decl == nil || f.Decl.Extern != nil {
					t.Errorf("%s: a Close function that is an extern: %s", pkg.Path, fmt.Sprint(f.Decl))
				}
			}
		}
		if externs == 0 {
			t.Fatal("the walk saw no std extern")
		}
		t.Logf("std and prelude: %d externs, %d Close functions", externs, closes)
	})
}
