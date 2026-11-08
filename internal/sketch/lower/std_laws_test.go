package lower

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// The std laws (docs/spec/stdlib.md, Std laws; std-align S1a) are checked,
// not run, until #162. TestStdLawsRun is the ledger's runner for the laws
// that need the lowering and the runtime: one Ergo program restates each
// as a test of its own name, its parameters drawn by the property runner,
// and the test build runs them with --seed 1, as ergo test --seed 1 does.

// stdLawsProbe restates each law whose ledger runner is TestStdLawsRun.
// A test's name is the ledger key without spaces and dots, its package
// capitalized: queue's Queue.CloseIsIdempotent is QueueQueueCloseIsIdempotent.
// A member law is restated for each kind of provider the std makes: a
// queue of capacity 0 and one with room, a waiting task.
const stdLawsProbe = `import queue
import task
import timeout
import scope
import trace
import log

// small is a capacity in 0..2 drawn from n.
func small(n int) int {
	let c = n % 3
	if c < 0 {
		return -c
	}
	c
}

test PreludeTaskStopIsIdempotent(n int) {
	let waiting = task.Go() {
		LiveScheduler.Sleep(1h)
		n
	}
	waiting.Stop()
	waiting.Stop()
	let done = task.Go() { n }
	done.Stop()
	done.Stop()
	assert true
}

test QueueQueueCloseIsIdempotent(n int) {
	let empty = queue.New[int](0)
	empty.Close()
	empty.Close()
	let full = queue.New[int](1)
	try full.Push(n)
	full.Close()
	full.Close()
	assert try full.Pop() == n
}

test QueueQueuePushAfterCloseFails(v int, cap int) {
	let c = small(cap)
	let q = queue.New[int](c)
	if c != 0 {
		try q.Push(v)
	}
	q.Close()
	let mut failed = false
	q.Push(v) catch {
	case queue.Closed:
		failed = true
	}
	assert failed
}

test QueueFirstInFirstOut(a int, b int) {
	let q = queue.New[int](2)
	try q.Push(a)
	try q.Push(b)
	let first = try q.Pop()
	let second = try q.Pop()
	assert first == a && second == b
}

test QueueCloseKeepsValues(v int) {
	let q = queue.New[int](1)
	try q.Push(v)
	q.Close()
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
	let t = task.Go() {
		LiveScheduler.Sleep(1h)
		0
	}
	t.Stop()
	t.Stop()
	assert true
}

test TimeoutQuickBodyWins(n int) {
	let v = with context { Scheduler: LiveScheduler } {
		try timeout.After(1h) { n }
	}
	assert v == n
}

test TimeoutDeadlineFirstExpires() {
	let expired = with context { Scheduler: LiveScheduler } {
		timeout.After(1ms) {
			LiveScheduler.Sleep(1h)
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
	"log AnnotationEndRestores":       `AnnotationEndRestores(key string, text string) { let before = Annotation(key) with Annotate(key, text) { Annotation(key) } Annotation(key) == before }`,
	"log InnerAnnotationShadows":      `InnerAnnotationShadows(key string, outer string, inner string) { with Annotate(key, outer) { with Annotate(key, inner) { Annotation(key) == Some[string]{Value: inner} } } }`,
	"prelude Task.StopIsIdempotent":   `StopIsIdempotent() { Stop() Stop() true }`,
	"queue CloseKeepsValues":          `CloseKeepsValues(v int) { let q = New[int](1) try q.Push(v) q.Close() try q.Pop() == v }`,
	"queue FirstInFirstOut":           `FirstInFirstOut(a int, b int) { let q = New[int](2) try q.Push(a) try q.Push(b) let first = try q.Pop() let second = try q.Pop() first == a && second == b }`,
	"queue Queue.CloseIsIdempotent":   `CloseIsIdempotent() { Close() Close() true }`,
	"queue Queue.PushAfterCloseFails": `PushAfterCloseFails(v T) { Close() Push(v) catch { case Closed: return true } false }`,
	"scope ActionsRunLastFirst":       `ActionsRunLastFirst() { let mut order = "" let f = func() { OnExit() { order = order + "a" } OnExit() { order = order + "b" } } f() order == "ba" }`,
	"task StopIsIdempotent":           `StopIsIdempotent() { let t = Go() { LiveScheduler.Sleep(1h) 0 } t.Stop() t.Stop() true }`,
	"task WaitReturnsTheBlock":        `WaitReturnsTheBlock(n int) { let t = Go() { n } t.Wait() == n }`,
	"timeout DeadlineFirstExpires":    `DeadlineFirstExpires() { with context { Scheduler: LiveScheduler } { After(1ms) { LiveScheduler.Sleep(1h) false } catch { case Expired: true } } }`,
	"timeout QuickBodyWins":           `QuickBodyWins(n int) { with context { Scheduler: LiveScheduler } { try After(1h) { n } == n } }`,
	"trace InnerSpanShadows":          `InnerSpanShadows(outer string, inner string) { with Span(outer) { with Span(inner) { Current() == Some[string]{Value: inner} } } }`,
	"trace SpanEndRestores":           `SpanEndRestores(name string) { let before = Current() with Span(name) { Current() } Current() == before }`,
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
			if !regexp.MustCompile(`(?m)^ok ` + name + `( \(\d+ cases\))?$`).MatchString(stdout) {
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

// TestStdLawsRun runs the probe's tests; lawledger.Run holds that the
// probe restates exactly the ledger's laws whose runner is this test, each
// with the row's statement, and that each passed.
func TestStdLawsRun(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "stdlaws", stdLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdlaws"], "--seed", "1")
	if code != 0 {
		t.Errorf("the std law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdLawsStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdLawsProbe, stdout))
}

// stdLawsZeroCost is a program that calls every std package with a law in
// this unit. Its Go is pinned in testdata/stdlaws/zero_cost.go.golden,
// written at the unit's base, before the laws: a law adds no Go to a
// build that imports its package (types.md, Laws 11).
const stdLawsZeroCost = `import queue
import task
import timeout
import scope
import trace
import log

func run() fails queue.Closed | timeout.Expired needs Console | Scheduler {
	let q = queue.New[int](1)
	try q.Push(1)
	q.Close()
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

func main() fails queue.Closed | timeout.Expired needs Console {
	with context { Scheduler: LiveScheduler } { try run() }
}
`

// TestStdLawsAddNoGo: the program above lowers to the Go it lowered to
// before the std laws were written, byte for byte, and its Go names no law.
// ERGO_STDLAWS_GOLDEN=write writes the golden file instead (run at the
// base only).
func TestStdLawsAddNoGo(t *testing.T) {
	got := mustLower(t, "stdlawszero", stdLawsZeroCost).mainGo()
	golden := filepath.Join("testdata", "stdlaws", "zero_cost.go.golden")
	if os.Getenv("ERGO_STDLAWS_GOLDEN") == "write" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the Go of a program that imports the std packages with laws changed:\n%s", lineDiff(string(want), got))
	}
	for _, law := range []string{"FirstInFirstOut", "CloseKeepsValues", "CloseIsIdempotent", "PushAfterCloseFails", "WaitReturnsTheBlock", "StopIsIdempotent", "QuickBodyWins", "DeadlineFirstExpires", "ActionsRunLastFirst", "InnerSpanShadows", "SpanEndRestores", "InnerAnnotationShadows", "AnnotationEndRestores", "Int63NeverNegative"} {
		if strings.Contains(got, law) {
			t.Errorf("the Go names the law %s", law)
		}
	}
}

// lineDiff is the first differing line of want and got, with its number.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + ":\n-" + a + "\n+" + b
		}
	}
	return ""
}
