package lower

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std laws (docs/spec/stdlib.md, Std laws; std-align S1a) are checked,
// not run, until #162. TestStdLawsRun is the ledger's runner for the laws
// that need the lowering and the runtime: one Ergo program restates each
// as a test of its own name, its parameters drawn by the property runner,
// and the test build runs them with --seed 1, as ergo test --seed 1 does.

// stdLawsProbe restates each law whose ledger runner is TestStdLawsRun.
// A test's name is the ledger key without spaces and dots, its package
// capitalized: queue's Queue.EndIsIdempotent is QueueQueueEndIsIdempotent.
// A member law is restated for each kind of provider the std makes: a
// queue of capacity 0 and one with room, a waiting task.
const stdLawsProbe = `import queue
import atomic
import tasks
import embed
import fs
import task
import time
import timeout
import scope
import trace
import log
import testing

test AtomicInitialValues(value int64, flag bool) {
	assert atomic.NewInt64(value).Load() == value && atomic.NewBool(flag).Load() == flag
}

test AtomicInt64StoresAndAdds(a int64, b int64) {
	let c = atomic.NewInt64(0)
	let alias = c
	alias.Store(a)
	let sum = c.Add(b)
	let seen = c.Load()
	let previous = c.Swap(a)
	let changed = c.CompareAndSwap(a, b)
	let unchanged = !c.CompareAndSwap(b + 1, a)
	assert sum == a + b && seen == sum && previous == sum && changed && unchanged && alias.Load() == b
}

test AtomicBoolExchanges(a bool, b bool) {
	let f = atomic.NewBool(false)
	f.Store(a)
	let previous = f.Swap(b)
	let seen = f.Load()
	let changed = f.CompareAndSwap(b, a)
	let unchanged = !f.CompareAndSwap(!a, b)
	assert previous == a && seen == b && changed && unchanged && f.Load() == a
}

test AtomicIncrementsCount(n int) {
	let k = 1 + (n % 8 + 8) % 8
	let c = atomic.NewInt64(0)
	let s = tasks.NewSet(tasks.FailFast)
	for _ in 0..k {
		try tasks.Go(s, func() { _ = c.Add(1) })
	}
	s.Wait()
	assert c.Load() == int64(k)
}

test EmbedEmptyHasNoFiles(path string) {
	let _ = embed.NewEmpty().ReadFile(path) catch {
	case fs.NotFound:
		return
	case fs.Denied, fs.Failed:
		assert false
		return
	}
	assert false
}

test EmbedFSReadIsRepeatable(path string) {
	let f = embed.NewEmpty()
	let first = f.ReadFile(path) catch {
	case fs.NotFound:
		return
	case fs.Denied, fs.Failed:
		assert false
		return
	}
	assert try f.ReadFile(path) == first
}

// small is a capacity in 0..2 drawn from n.
func small(n int) int {
	let c = n % 3
	if c < 0 {
		return -c
	}
	c
}

test PreludeTaskStopIsIdempotent(n int) {
	let waiting = with context { Scheduler: time.NewManual(0) } {
		task.Go() {
			Scheduler.Sleep(1h)
			n
		}
	}
	waiting.Stop()
	waiting.Stop()
	let done = task.Go() { n }
	done.Stop()
	done.Stop()
	assert true
}

test QueueQueueEndIsIdempotent(n int) {
	let empty = queue.New[int](0)
	empty.End()
	empty.End()
	let full = queue.New[int](1)
	try full.Push(n)
	full.End()
	full.End()
	assert try full.Pop() == n
}

test QueueQueuePushAfterEndFails(v int, cap int) {
	let c = small(cap)
	let q = queue.New[int](c)
	if c != 0 {
		try q.Push(v)
	}
	q.End()
	let mut failed = false
	q.Push(v) catch {
	case queue.Closed:
		failed = true
	}
	assert failed
}

test QueueQueueValuesStops(xs []int, extra int) {
	let n = len(xs)
	for k in 1..n+2 {
		let q = queue.New[int](n + small(extra))
		for x in xs {
			try q.Push(x)
		}
		q.End()
		let mut calls = 0
		let s = q.Values()
		s(func(v int) bool {
			calls = calls + 1
			calls < k
		})
		let taken = if k < n { k } else { n }
		assert calls == taken && q.Len() == n - taken
		for i in taken..n {
			assert q.Poll() == Some[int]{Value: xs[i]}
		}
		assert q.Poll() == None
	}
	let empty = queue.New[int](0)
	empty.End()
	let mut none = 0
	for _ in empty.Values() {
		none = none + 1
	}
	assert none == 0 && empty.Len() == 0
}

test QueueFirstInFirstOut(a int, b int) {
	let q = queue.New[int](2)
	try q.Push(a)
	try q.Push(b)
	let first = try q.Pop()
	let second = try q.Pop()
	assert first == a && second == b
}

test QueueEndKeepsValues(v int) {
	let q = queue.New[int](1)
	try q.Push(v)
	q.End()
	assert try q.Pop() == v
}

test ScopeActionsRunLastFirst() {
	let mut order = ""
	let f = func() {
		scope.OnExit() { order = order + "a" }
		scope.OnExit() { order = order + "b" }
	}
	f()
	assert order == "ba"
}

test TaskWaitReturnsTheBlock(n int) {
	let t = task.Go() { n }
	assert t.Wait() == n
}

test TaskStopIsIdempotent() {
	let t = with context { Scheduler: time.NewManual(0) } {
		task.Go() {
			Scheduler.Sleep(1h)
			0
		}
	}
	t.Stop()
	t.Stop()
	assert true
}

test TimeoutQuickBodyWins(n int) {
	let v = with context { Scheduler: time.NewManual(0) } {
		try timeout.After(1h) { n }
	}
	assert v == n
}

test TimeoutDeadlineFirstExpires() {
	let m = time.NewManual(0)
	let expired = with context { Scheduler: m } {
		timeout.After(1ms) {
			for m.Sleepers() == 0 {
			}
			m.Advance(1ms)
			Scheduler.Sleep(1h)
			false
		} catch {
		case timeout.Expired:
			true
		}
	}
	assert expired
}

test TraceInnerSpanShadows(outer string, inner string) {
	let got = with trace.Span(outer) {
		with trace.Span(inner) { trace.Current() }
	}
	assert got == Some[string]{Value: inner}
}

test TraceSpanEndRestores(outer string, name string) {
	let before = trace.Current()
	with trace.Span(name) {
		trace.Current()
	}
	assert trace.Current() == before
	let nested = with trace.Span(outer) {
		let mine = trace.Current()
		with trace.Span(name) {
			trace.Current()
		}
		trace.Current() == mine
	}
	assert nested
}

test LogInnerAnnotationShadows(key string, outer string, inner string) {
	let got = with log.Annotate(key, outer) {
		with log.Annotate(key, inner) { log.Annotation(key) }
	}
	assert got == Some[string]{Value: inner}
}

test LogAnnotationEndRestores(key string, outer string, text string) {
	let before = log.Annotation(key)
	with log.Annotate(key, text) {
		log.Annotation(key)
	}
	assert log.Annotation(key) == before
	let nested = with log.Annotate(key, outer) {
		let mine = log.Annotation(key)
		with log.Annotate(key, text) {
			log.Annotation(key)
		}
		log.Annotation(key) == mine
	}
	assert nested
}

// probe and Absent restate the capability of the testing laws, which this
// package cannot name.
type probe {
	Size() int,
	Has(k string) bool,
	Put(k string, v int),
	Get(k string) int fails Absent,
}

type Absent { Key string }

test TestingScriptRecordsEachCall(ks []string, n int) {
	let s = testing.Script[probe]{}
	s.Has.Answer(func(k string) bool { len(k) > 0 })
	with context { probe: s } {
		for i, k in ks {
			probe.Put(k, n + i)
			let _ = probe.Has(k)
		}
	}
	let puts = s.Put.Calls()
	assert len(puts) == len(ks) && len(s.Has.Calls()) == len(ks) && len(s.Size.Calls()) == 0
	for i, k in ks {
		assert puts[i] == (k, n + i) && s.Has.Calls()[i] == k
	}
}

test TestingScriptAnswersByTheLastAnswer(k string, a int, b int) {
	let s = testing.Script[probe]{}
	s.Size.Answer(func() int { a })
	s.Get.Answer(func(key string) int fails Absent { Absent{Key: key} })
	with context { probe: s } {
		let first = probe.Size()
		s.Size.Answer(func() int { b })
		let second = probe.Size()
		let got = probe.Get(k) catch {
		case Absent as e:
			if e.Key == k { -1 } else { -2 }
		}
		assert first == a && second == b && got == -1 && len(s.Size.Calls()) == 2 && s.Size.Calls()[0] == testing.NoArgs && s.Get.Calls()[0] == k
	}
}
`

// stdLawsTestName is the probe test that restates the law of a ledger row.
func stdLawsTestName(pkg, law string) string {
	return strings.ToUpper(pkg[:1]) + pkg[1:] + strings.ReplaceAll(law, ".", "")
}

// stdLawsStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key. lawledger.Run
// holds it equal to the ledger, so a law changed in source fails here
// until its restatement in the probe is changed with it.
var stdLawsStatements = map[string]string{
	"atomic InitialValues":                 `InitialValues(value int64, flag bool) { NewInt64(value).Load() == value && NewBool(flag).Load() == flag }`,
	"atomic Int64.StoresAndAdds":           `StoresAndAdds(a int64, b int64) { Store(a) let sum = Add(b) let seen = Load() let previous = Swap(a) let changed = CompareAndSwap(a, b) let unchanged = !CompareAndSwap(b+1, a) sum == a+b && seen == sum && previous == sum && changed && unchanged && Load() == b }`,
	"atomic Bool.Exchanges":                `Exchanges(a bool, b bool) { Store(a) let previous = Swap(b) let seen = Load() let changed = CompareAndSwap(b, a) let unchanged = !CompareAndSwap(!a, b) previous == a && seen == b && changed && unchanged && Load() == a }`,
	"atomic IncrementsCount":               `IncrementsCount(n int) { let k = 1 + (n%8+8)%8 let c = NewInt64(0) let s = tasks.NewSet(tasks.FailFast) for _ in 0..k { try tasks.Go(s, func() { _ = c.Add(1) }) } s.Wait() c.Load() == int64(k) }`,
	"embed EmptyHasNoFiles":                `EmptyHasNoFiles(path string) { let _ = NewEmpty().ReadFile(path) catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } false }`,
	"embed FS.ReadIsRepeatable":            `ReadIsRepeatable(path string) { let first = ReadFile(path) catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } try ReadFile(path) == first }`,
	"log AnnotationEndRestores":            `AnnotationEndRestores(key string, text string) { let before = Annotation(key) with Annotate(key, text) { Annotation(key) } Annotation(key) == before }`,
	"log InnerAnnotationShadows":           `InnerAnnotationShadows(key string, outer string, inner string) { with Annotate(key, outer) { with Annotate(key, inner) { Annotation(key) == Some[string]{Value: inner} } } }`,
	"prelude Task.StopIsIdempotent":        `StopIsIdempotent() { Stop() Stop() true }`,
	"queue EndKeepsValues":                 `EndKeepsValues(v int) { let q = New[int](1) try q.Push(v) q.End() try q.Pop() == v }`,
	"queue FirstInFirstOut":                `FirstInFirstOut(a int, b int) { let q = New[int](2) try q.Push(a) try q.Push(b) let first = try q.Pop() let second = try q.Pop() first == a && second == b }`,
	"queue Queue.EndIsIdempotent":          `EndIsIdempotent() { End() End() true }`,
	"queue Queue.PushAfterEndFails":        `PushAfterEndFails(v T) { End() Push(v) catch { case Closed: return true } false }`,
	"queue Queue.ValuesStops":              `ValuesStops(k int) { if k < 1 { return true } End() let n = Len() let mut calls = 0 let s = Values() s(func(v T) bool { calls = calls + 1 calls < k }) let taken = if k < n { k } else { n } calls == taken && Len() == n-taken }`,
	"scope ActionsRunLastFirst":            `ActionsRunLastFirst() { let mut order = "" let f = func() { OnExit() { order = order + "a" } OnExit() { order = order + "b" } } f() order == "ba" }`,
	"task StopIsIdempotent":                `StopIsIdempotent() { let t = with context { Scheduler: time.NewManual(0) } { Go() { Scheduler.Sleep(1h) 0 } } t.Stop() t.Stop() true }`,
	"task WaitReturnsTheBlock":             `WaitReturnsTheBlock(n int) { let t = Go() { n } t.Wait() == n }`,
	"timeout DeadlineFirstExpires":         `DeadlineFirstExpires() { let m = time.NewManual(0) with context { Scheduler: m } { After(1ms) { for m.Sleepers() == 0 { } m.Advance(1ms) Scheduler.Sleep(1h) false } catch { case Expired: true } } }`,
	"testing ScriptAnswersByTheLastAnswer": `ScriptAnswersByTheLastAnswer(k string, a int, b int) { let s = Script[probe]{} s.Size.Answer(func() int { a }) s.Get.Answer(func(key string) int fails Absent { Absent{Key: key} }) with context { probe: s } { let first = probe.Size() s.Size.Answer(func() int { b }) let second = probe.Size() let got = probe.Get(k) catch { case Absent as e: if e.Key == k { -1 } else { -2 } } first == a && second == b && got == -1 && len(s.Size.Calls()) == 2 && s.Size.Calls()[0] == NoArgs && s.Get.Calls()[0] == k } }`,
	"testing ScriptRecordsEachCall":        `ScriptRecordsEachCall(ks []string, n int) { let s = Script[probe]{} s.Has.Answer(func(k string) bool { len(k) > 0 }) with context { probe: s } { for i, k in ks { probe.Put(k, n + i) let _ = probe.Has(k) } } let puts = s.Put.Calls() let mut same = len(puts) == len(ks) && len(s.Has.Calls()) == len(ks) && len(s.Size.Calls()) == 0 for i, k in ks { same = same && puts[i] == (k, n + i) && s.Has.Calls()[i] == k } same }`,
	"timeout QuickBodyWins":                `QuickBodyWins(n int) { with context { Scheduler: time.NewManual(0) } { try After(1h) { n } == n } }`,
	"trace InnerSpanShadows":               `InnerSpanShadows(outer string, inner string) { with Span(outer) { with Span(inner) { Current() == Some[string]{Value: inner} } } }`,
	"trace SpanEndRestores":                `SpanEndRestores(name string) { let before = Current() with Span(name) { Current() } Current() == before }`,
}

// lawsPassed is the lawledger checks of a test build's run: the law of
// each key passes when stdout has the ok line of its probe test, named by
// testName. Every test of the probe is a law's, or one of extra: any other
// probe test fails t.
func lawsPassed(t *testing.T, statements map[string]string, testName func(key string) string, probe, stdout string, extra ...string) map[string]lawledger.Law {
	t.Helper()
	laws := map[string]lawledger.Law{}
	named := map[string]bool{}
	for _, name := range extra {
		named[name] = true
	}
	for key, statement := range statements {
		name := testName(key)
		named[name] = true
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if !regexp.MustCompile(`(?m)^ok ` + name + `( \((\d+ cases|proved: all \d+ cases)\))?$`).MatchString(stdout) {
				t.Errorf("no ok line for %s:\n%s", name, stdout)
			}
		}}
	}
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)`).FindAllStringSubmatch(probe, -1) {
		if !named[m[1]] {
			t.Errorf("the probe test %s restates no law of this runner and is not one of its extra tests %v", m[1], extra)
		}
	}
	return laws
}

// lawProbe runs the tests of the law probe name, a test build its caller
// declares in a program table (testBuildProgram(name, probe)), with --seed
// 1, and returns its laws for the runner's lawledger.Run: the probe's test
// of each ledger key is named by stdLawsTestName, and statements is each
// law's restatement by key (fu-ledger-runner; the runners of S2 and S3
// moved onto it at the batch-10 merge).
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func lawProbe(t *testing.T, name, probe string, statements map[string]string) map[string]lawledger.Law {
	t.Helper()
	stdout, stderr, code := runExeArgs(t, tableExe(t, name), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of %s failed: exit %d\n%s\n%s", name, code, stdout, firstLines(stderr, 20))
	}
	return lawsPassed(t, statements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, probe, stdout)
}

// TestStdLawsRun runs the probe's tests; lawledger.Run holds that the
// probe restates exactly the ledger's laws whose runner is this test, each
// with the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdlaws"), "--seed", "1")
	if code != 0 {
		t.Errorf("the std law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdLawsStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdLawsProbe, stdout))
}

// stdLawsZeroCost is a program that calls every std package with a law in
// this unit: a law adds no Go to a build that imports its package
// (types.md, Laws 11).
const stdLawsZeroCost = `import queue
import task
import timeout
import scope
import trace
import log

func run() fails queue.Closed | timeout.Expired needs Console | Scheduler {
	let q = queue.New[int](1)
	try q.Push(1)
	q.End()
	let t = task.Go() { try q.Pop() }
	let v = try t.Wait()
	scope.OnExit() { Console.Println("exit") }
	let w = try timeout.After(1h) { v + 1 }
	with trace.Span("s") {
		with log.Annotate("k", "x") {
			Console.Println("\(w) \(trace.Current() == None) \(log.Annotation("k") == None)")
		}
	}
}

func main() fails queue.Closed | timeout.Expired needs Console | Ambient {
	with context { Scheduler: NewLiveScheduler() } { try run() }
}
`

// stdLawsTail is declared at the end of both programs below; stdLawsOnly
// is a law appended to the second, with an import only it uses (hex), a
// generic instance only it makes (queue.New[string]) and a failure of
// LawOnly only it raises, so LawOnly is an error type only in a law.
const stdLawsTail = `
type LawOnly { n int }
`

const stdLawsOnly = `
law OnlyALawUsesThem(s string, n int) {
	let q = queue.New[string](1)
	try q.Push(hex.Encode(s))
	let f = func(k int) int fails LawOnly {
		if k < 0 {
			return LawOnly{n: k}
		}
		k
	}
	let m = f(n) catch {
	case LawOnly:
		0
	}
	try q.Pop() == hex.Encode(s) && m >= 0
}
`

// TestStdLawsAddNoGo: under both call lowerings (inline.go), the program
// above lowers to Go that type-checks and names no law of the std packages
// it imports, and a law appended to it, with an import, a generic instance
// and an error type only the law uses, changes no byte of its Go (types.md,
// Laws 11; a blank line stands for the import, so the lines agree). It
// replaces a golden of the Go text: the text is no promise, the absence of
// every law is.
func TestStdLawsAddNoGo(t *testing.T) {
	names := map[string]bool{"OnlyALawUsesThem": true}
	for _, p := range []string{"queue", "task", "timeout", "scope", "trace", "log"} {
		files, ok := std.Package(p)
		if !ok {
			t.Fatalf("no std package %s", p)
		}
		for _, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if d, ok := n.(*syntax.LawDecl); ok {
					names[d.Name.Name] = true
				}
				return true
			})
		}
	}
	var programs []*lowered
	for _, ref := range []bool{false, true} {
		var texts []string
		for _, src := range []string{"\n" + stdLawsZeroCost + stdLawsTail, "import hex\n" + stdLawsZeroCost + stdLawsTail + stdLawsOnly} {
			name := fmt.Sprintf("stdlawszero%d", len(programs))
			o, _ := lowerInlineMode(t, name, src, ref)
			programs = append(programs, o)
			text := o.mainGo()
			for law := range names {
				if regexp.MustCompile(`\b` + law + `\b`).MatchString(text) {
					t.Errorf("%s (reference %v): the Go names the law %s", name, ref, law)
				}
			}
			texts = append(texts, text)
		}
		if texts[0] != texts[1] {
			t.Errorf("reference %v: a law changes the Go:\n%s", ref, firstDiff(texts[0], texts[1]))
		}
	}
	typecheckGo(t, programs)
}

// firstDiff is the first differing line of a and b, with its number.
func firstDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(x), len(y)) {
		var p, q string
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return "line " + strconv.Itoa(i+1) + ":\n-" + p + "\n+" + q
		}
	}
	return ""
}
