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
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// p3Text is the P3 message (check-v1 spec add-on), quoted exactly.
const p3Text = "an extern function is called directly; wrap it in a literal: func() { f() }"

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
// operation type with no row here fails TestStdProtocolSealed, and so does
// a row whose type std no longer declares.
var sealedPrograms = map[string]string{
	"queue.Queue": queueMine + `
func use(q queue.Queue[int]) { q.End() }

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

type Mine {}

func Wait(_ Mine) fails tasks.Closed {}
func Stop(_ Mine) {}
func Len(_ Mine) int { 0 }

func use(s tasks.Set[tasks.Closed]) { _ = s.Len() }

func main() {
	use(Mine)
}
`,
	// std journal (MU9): open; journal/memory and journal/file are
	// providers that programs could write too.
	"journal.Journal": `import journal

type Mine {}

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

type Mine {}

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

type Mine {}

func Uint64(_ Mine) uint64 { 0 }
func IntN(_ Mine, n int) int { 0 }
func Float64(_ Mine) float64 { 0.5 }

func use(s rand.Stream) { _ = s.Uint64() }

func main() {
	use(Mine)
}
`,
	"prelude.Task": `type Fault {}
type Mine {}

func Wait(_ Mine) int fails Fault { 0 }
func Stop(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine)
}
`,
	"prelude.Console": `type Mine {}

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
	// Sleep is a prelude name no program redeclares, so the program value
	// is one of a program operation type.
	"prelude.Scheduler": `type Mine { Sleep(d duration) }

func use(s Scheduler) { s.Sleep(1ms) }

func pass(m Mine) {
	use(m)
}
`,
	// std time (S2b): the prelude's Clock moved here, with Now() time.Time.
	"time.Clock": `import time

type Mine {}

func Now(_ Mine) time.Time { time.Unix(0, 0) }

func use(c time.Clock) { _ = c.Now() }

func main() {
	use(Mine)
}
`,
	"prelude.Random": `type Mine {}

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

type Mine {}

func Args(_ Mine) []string { []string{} }
func Getenv(_ Mine, key string) Option[string] { None }

func use(p os.Process) { _ = p.Args() }

func main() {
	use(Mine)
}
`,
	// std fs (G4): open, as no std extern's result names it (stdlib.md,
	// fs 5).
	"fs.Files": `import fs

type Mine {}

func ReadFile(_ Mine, path string) string fails fs.NotFound | fs.Denied | fs.Failed { "" }
func ReadDir(_ Mine, path string) []string fails fs.NotFound | fs.Denied | fs.Failed { []string{} }
func Stat(_ Mine, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	fs.Info{Name: path, Size: 0, Dir: false, Perm: 0}
}

func use(f fs.Files) { _ = f.ReadFile("a") catch { case fs.NotFound, fs.Denied, fs.Failed: "" } }

func main() {
	use(Mine)
}
`,
	// std fs writes (G4b): open, as fs.Files is.
	"fs.Writes": `import fs

type Mine {}

func WriteFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAll(_ Mine, path string) fails fs.Denied | fs.Failed {}
func WriteFileMode(_ Mine, path string, text string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func MkdirAllMode(_ Mine, path string, mode fs.Mode) fails fs.Denied | fs.Failed {}
func AppendFile(_ Mine, path string, text string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Chmod(_ Mine, path string, mode fs.Mode) fails fs.NotFound | fs.Denied | fs.Failed {}
func Rename(_ Mine, from string, to string) fails fs.NotFound | fs.Denied | fs.Failed {}
func Remove(_ Mine, path string) fails fs.NotFound | fs.Denied | fs.Failed {}

func use(w fs.Writes) { w.MkdirAll("a") catch { case fs.Denied, fs.Failed: } }

func main() {
	use(Mine)
}
`,
	// std io (std-align S1b): open, as no std extern's result names them
	// (stdlib.md, io 6).
	"io.Stdout": `import io

type Mine {}

func Print(_ Mine, text string) {}
func Println(_ Mine, line string) {}
func IsTerminal(_ Mine) bool { false }

func use(o io.Stdout) { o.Print("x") }

func main() {
	use(Mine)
}
`,
	"io.Stderr": `import io

type Mine {}

func Eprint(_ Mine, text string) {}
func Eprintln(_ Mine, line string) {}

func use(e io.Stderr) { e.Eprintln("x") }

func main() {
	use(Mine)
}
`,
	"io.Stdin": `import io

type Mine {}

func ReadAll(_ Mine) string fails io.ReadFailed { "" }
func Lines(_ Mine) Seq[string] { io.Input{Text: ""}.Lines() }

func use(i io.Stdin) { _ = i.ReadAll() catch { case io.ReadFailed: "" } }

func main() {
	use(Mine)
}
`,
	// std exec (S3c): open, as no std extern's result names it (stdlib.md,
	// exec). exec.Script is the std's test provider of it.
	"exec.Runner": `import exec

type Mine {}

func Run(_ Mine, c exec.Command) exec.Result fails exec.NotFound | exec.Denied | exec.Failed {
	exec.Result{Status: 0, Stdout: "", Stderr: ""}
}

func use(r exec.Runner) { _ = r.Run(exec.New("true", []string{})) catch { case exec.NotFound, exec.Denied, exec.Failed: exec.Result{Status: 0, Stdout: "", Stderr: ""} } }

func main() {
	use(Mine)
}
`,
	// The prelude's Ambient (host-authority H1): its one operation is
	// unexported, so no program type implements it (sealedBy), though no
	// std extern names it (unexportedStd).
	"prelude.Ambient": `type Mine {}

func ambient(_ Mine) {}

func use(_ Ambient) {}

func main() {
	use(Mine)
}
`,
}

// unexportedStd is the std operation types sealed by an unexported
// operation: a program value is refused at the use line by the sealedBy
// text, not by SealedStd's S109.
var unexportedStd = map[string]string{
	"prelude.Ambient": "ambient is an unexported operation of prelude.Ambient, so only a type of package prelude implements it",
}

// openStd is the std operation types a program provides: no std extern's
// result names them (check.SealedStd; stdlib.md, Types 3). Every other
// std operation type is sealed.
var openStd = map[string]bool{
	"prelude.Console":   true,
	"prelude.Scheduler": true,
	"time.Clock":        true,
	"prelude.Random":    true,
	"os.Process":        true,
	"fs.Files":          true,
	"fs.Writes":         true,
	"io.Stdout":         true,
	"io.Stderr":         true,
	"io.Stdin":          true,
	"exec.Runner":       true,
	"journal.Journal":   true,
	"journal.Codec":     true,
}

// stdOperationTypes lists, as path.Name, every exported operation type
// (a braced type of operations, not a union) the prelude and each std
// package declare, read from their source with the parser: independent of
// the checker.
func stdOperationTypes(t *testing.T) []string {
	t.Helper()
	sources := map[string]map[string]string{std.PreludePath: std.Prelude()}
	for _, path := range std.Packages() {
		files, ok := std.Package(path)
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

// TestStdProtocolSealed is C28 of check-v1 stage E4 (A6): a value converted
// to a sealed std protocol (the prelude's Task, or an operation type of a
// std package other than the prelude, the types lower/std.go runtimeType
// represents by a runtime/v1 type) is S109 naming #206, at every site that
// converts by assignability. The prelude's capabilities stay open: a
// program provides them (the over-seal control).
func TestStdProtocolSealed(t *testing.T) {
	sealed := []string{"sealed std protocol", "#206"}
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
type Mine {}

func Wait(_ Mine) int fails Fault { 0 }
func Stop(_ Mine) {}

func use(t Task[int, Fault]) { t.Stop() }

func main() {
	use(Mine) // want S109
}
`,
		// Controls: a value of the std type itself converts nothing, and
		// the prelude's Console takes a program provider.
		"queue-identity": `import queue

func use(q queue.Queue[int]) { q.End() }

func main() {
	use(queue.New[int](1))
}
`,
		"console-param": `type Mine {}

func Println(_ Mine, line string) {}

func use(c Console) { c.Println("x") }

func main() {
	use(Mine)
}
`,
		"console-provider": `type Quiet {}

func Println(_ Quiet, line string) {}

let W = context { Console: Quiet }

func main() {
	with W { Console.Println("x") }
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, sealed...) })
	}
	types := stdOperationTypes(t)
	for _, key := range types {
		t.Run("enumerate/"+key, func(t *testing.T) {
			src, ok := sealedPrograms[key]
			if !ok {
				t.Fatalf("the std operation type %s has no row in sealedPrograms", key)
			}
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
			if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != at || !strings.Contains(diags[0].Message, "#206") {
				t.Fatalf("want one S109 naming #206 at line %d, got\n\t%s", at, describe(diags))
			}
		})
	}
	for key := range sealedPrograms {
		if !sortedHas(types, key) {
			t.Errorf("sealedPrograms row %s: std declares no such operation type", key)
		}
	}
}

// TestStdProtocolOpen is the sealed rule of G2 part 2 (stdlib.md, Types 3;
// check.SealedStd): os.Process, which no std extern's result names, takes
// a program provider at each site that converts by assignability, while
// queue.Queue, which queue.New returns, still refuses the same program
// value at the same sites with S109.
func TestStdProtocolOpen(t *testing.T) {
	const fake = `import os

type FakeEnv { Argv []string }

func Args(e FakeEnv) []string { e.Argv }

func Getenv(_ FakeEnv, key string) Option[string] { None }
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
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, "sealed std protocol", "#206") })
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
// pass witnesses no fact records (S109 naming #197, closeWitnessed). A std
// or extern Close is unreachable: no std or prelude Close is an extern, and
// a user extern Close takes a named type, outside the C26 domain.
func TestCloseRefusals(t *testing.T) {
	cases := map[string]string{
		"operation": `type A { Use() }
type Releaser { Use(), Close() }
type R {}
func Use(_ R) {}
func Close(_ R) {}
func make() Releaser { R }
let W = context { A: make() }
func main() { with W { A.Use() } }
`,
		"generic": `type A { Use() }
type G[T] { Value T }
func Use[T](_ G[T]) {}
func Close[T](_ G[T]) {}
let W = context { A: G[int]{Value: 1} }
func main() { with W { A.Use() } }
`,
		"pointer-close-value-provider": `type A { Use() }
type P {}
func Use(_ P) {}
func Close(_ *P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"value-close": `type A { Use() }
type P {}
func Use(_ P) {}
func Close(_ P) {}
let W = context { A: P }
func main() { with W { A.Use() } }
`,
		"generic-witnessed": `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) {}
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
		// Once per provider, though two withs release it.
		"generic-witnessed-once": `type A { Use() }
type Named { Name() string }
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
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
type N {}
func Name(_ N) string { "n" }
type G[T] { Value T }
func Use[T Named](_ G[T]) {}
func Close[T Named](_ G[T]) needs Console { Console.Println("closed") }
let W = context { A: G[N]{Value: N} } // want S109
func main() { with W { A.Use() } }
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { judgeOutside(t, src, "#197") })
	}
	t.Run("user-extern-close", func(t *testing.T) {
		judgeOutside(t, "type R {}\nextern go strings.F func Close(r R) // want S109\n", "#202")
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
		for _, path := range std.Packages() {
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
