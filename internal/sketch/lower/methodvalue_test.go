package lower

import (
	"os"
	"strings"
	"testing"
)

// methodValueProbe is eval's method-value probe (types.md, Methods 2;
// CV1), read once for the program table.
var methodValueProbe = func() string {
	src, err := os.ReadFile("../eval/testdata/methodvalue.ergo")
	if err != nil {
		panic(err)
	}
	return string(src)
}()

// TestMethodValueRuns runs the method-value probe's seeded tests built: a
// method value is the closure that calls x.f, with x's value copied where
// x.f is evaluated (calls.go methodValue196), so a later write to x, to a
// field of x or through a borrow of x does not reach it. It catches
// cv-method-value-late-binding (the receiver not spilled, so the closure
// reads the variable at the call) in the package ledger; eval's
// TestMethodValueAgrees checks the same probe at check time.
//
// It builds Go because the tests run in the built test binary, where the
// closure's capture is Go's.
func TestMethodValueRuns(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "methodvalue"), "--seed", "1")
	if code != 0 {
		t.Errorf("the method-value probe failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, name := range []string{"MethodValueIsTheClosure", "MethodValueBindsAtTheTake", "MethodValueOfAnInstance", "MethodValueBindsBeforeLaterArguments"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("the probe did not report %s:\n%s", name, stdout)
		}
	}
}

// TestMethodValueTakesTheIR: a method value is built by the IR, with no
// fallback to the text path (ir_methodvalue.go), both where a known-
// callback variant takes it (the plain closure: a known site) and where a
// function keeps it (the uniform closure). It catches cv-mv-ir-fallback
// (the IR stops at a method value) and cv-mv-known-site-off (a pure
// method value is no known callback, so the call keeps the uniform ABI,
// the receipt's 1.71x of hand Go) in the package ledger.
func TestMethodValueTakesTheIR(t *testing.T) {
	const src = `type Acc { N int }

func Add(a Acc, b int) int { a.N + b }

func apply(f func(b int) int, x int) int { f(x) }

func keep(f own func(b int) int) func(b int) int { f }

func main() needs Console {
	let mut a = Acc{N: 1}
	let k = keep(a.Add)
	a = Acc{N: 10}
	let s = apply(a.Add, 2)
	Console.Println("\(k(2)) \(s)")
}
`
	saved := statsHook
	defer func() { statsHook = saved }()
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	u := factsUnit(t, src)
	u.NoFallback = true
	if _, _, err := LowerIR(u); err != nil {
		t.Fatalf("a method value leaves the IR: %v", err)
	}
	if stats.Sites != 1 {
		t.Errorf("apply(a.Add, 2) is %d known sites, want 1: a pure method value is a known callback (pureMethodValue)", stats.Sites)
	}
}
