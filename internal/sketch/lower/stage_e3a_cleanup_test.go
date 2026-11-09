package lower

import (
	"fmt"
	"strings"
	"testing"
)

// Stage E3a M3: a test case's verdict is the exit that survives its
// unwinding, never the bare fact that an assert failed on the way. A failed
// assert, alone or beside typed failures, fails the case and the run goes
// on; any other exit, a defect an exit action raises after the assert began
// the unwind included, prints the defect FAIL line and stops the run with
// the runtime's report of every reason (exit 2).
//
// TestCaseOutcomeProperty is the property: body outcome × exit-action
// outcome × the carrier that runs the exit action, through a with scope,
// a frame's exit action and child tasks. Each program has a second test,
// Later, which runs only when T's outcome lets the run go on.

// caseBodies are the outcomes of the test's body: the statement that makes
// it, and the stderr text its reason leaves in a runtime report.
var caseBodies = []struct {
	name, stmt, reported string
}{
	{"pass", "assert one() == 1", ""},
	{"assert", "assert one() == 2", "assert failed at main.ergo:"},
	{"failure", "let v = try risky(7)\nassert v == 0", "Bang{Code:7}"},
	{"defect", "assert index([]int{1, 2}, 5) == 0", "index out of range [5] with length 2\n"},
}

// caseCleanups are the outcomes of the exit action. An assert in it stands
// only in a block of the test (a provider's Close is a function, where an
// assert is S108), so the with carriers skip it.
var caseCleanups = []struct {
	name, stmt, reported string
	inTest               bool // the statement must stand in the test's own code
}{
	{"clean", "let _ = index([]int{1}, 0)", "", false},
	{"defect", "let _ = index([]int{}, 0)", "index out of range [0] with length 0 (in an exit action)", false},
	{"assert", "assert one() == 3", "assert failed at main.ergo:", true},
}

// The exit action a carrier runs.
const (
	exitNone  = iota // no exit action: the body's panic reaches the case bare
	exitClose        // Mem's Close, which the with that built Mem runs
	exitBlock        // a scope.OnExit block of the test's own code
)

// caseCarriers place the body and the exit action. BODY and CLEANUP are the
// statements; WAIT is the Wait of the child task, with try when its block
// can fail.
var caseCarriers = []struct {
	name, test string
	exit       int
}{
	{"bare", "BODY", exitNone},
	{"with", "with context { Store: Mem } {\n\tassert Store.Get() == 1\n\tBODY\n}", exitClose},
	{"exit-action", "scope.OnExit() {\n\tCLEANUP\n}\nBODY", exitBlock},
	{"task-with", "let t = task.Go() {\n\twith context { Store: Mem } {\n\t\tassert Store.Get() == 1\n\t\tBODY\n\t}\n\t0\n}\nWAIT", exitClose},
	{"task-exit-action", "let t = task.Go() {\n\tscope.OnExit() {\n\t\tCLEANUP\n\t}\n\tBODY\n\t0\n}\nWAIT", exitBlock},
	{"exit-action-around-task", "scope.OnExit() {\n\tCLEANUP\n}\nlet t = task.Go() {\n\tBODY\n\t0\n}\nWAIT", exitBlock},
}

func indent(s, tab string) string {
	return strings.ReplaceAll(s, "\n", "\n"+tab)
}

// caseProgram writes the program of one point of the property.
func caseProgram(carrier, body, cleanup string, exit int) string {
	test := carrier
	// Each placeholder stands at its own indentation; the statements it
	// replaces continue at the same one.
	for _, ph := range []struct{ key, stmt string }{{"BODY", body}, {"CLEANUP", cleanup}} {
		for _, tab := range []string{"\t\t", "\t", ""} {
			test = strings.ReplaceAll(test, tab+ph.key, tab+indent(ph.stmt, tab))
		}
	}
	wait := "let _ = t.Wait()"
	if strings.Contains(body, "try ") {
		wait = "let _ = try t.Wait()"
	}
	test = strings.ReplaceAll(test, "WAIT", wait)
	var imports []string
	if strings.Contains(test, "scope.OnExit") {
		imports = append(imports, "import scope\n")
	}
	if strings.Contains(test, "task.Go") {
		imports = append(imports, "import task\n")
	}
	closeBody := "let _ = index([]int{1}, 0)"
	if exit == exitClose {
		closeBody = cleanup
	}
	return strings.Join(imports, "") + `
type Store { Get() int, }
type Mem {}
type Bang { Code int }

func Get(_ Mem) int { 1 }
func Close(_ Mem) { ` + closeBody + ` }
func one() int { 1 }
func index(xs []int, i int) int { xs[i] }
func risky(n int) int fails Bang {
	if n > 0 {
		return Bang{Code: n}
	}
	n
}

test T {
	` + indent(test, "\t") + `
}

test Later { assert true }

func main() needs Console {
	Console.Println("run")
}
`
}

// TestCaseOutcomeProperty builds Go because it runs the emitted test harness
// under go test: the harness's verdicts and the values it prints are the
// output; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestCaseOutcomeProperty(t *testing.T) {
	needsBuild(t)
	type point struct {
		name          string
		body, cleanup int
		prog          *lowered
	}
	var points []point
	var builds []*lowered
	for _, c := range caseCarriers {
		for bi, b := range caseBodies {
			for ci, cl := range caseCleanups {
				label := cl.name
				switch {
				case c.exit == exitNone && ci > 0, c.exit == exitClose && cl.inTest:
					continue
				case c.exit == exitNone:
					label = "none" // nothing runs after the body
				}
				name := fmt.Sprintf("case_%s_%s_%s", strings.ReplaceAll(c.name, "-", "_"), b.name, label)
				p := lowerTestBuild(t, name, caseProgram(c.test, b.stmt, cl.stmt, c.exit))
				points = append(points, point{name, bi, ci, p})
				builds = append(builds, p)
			}
		}
	}
	// Every carrier meets every body and every cleanup it can hold: the
	// bare carrier × 4 bodies, 2 with carriers × 4 × 2 and 3 exit-action
	// carriers × 4 × 3.
	if want := len(caseBodies) + 2*len(caseBodies)*2 + 3*len(caseBodies)*len(caseCleanups); len(points) != want {
		t.Fatalf("%d points, want %d", len(points), want)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, pt := range points {
		b, cl := caseBodies[pt.body], caseCleanups[pt.cleanup]
		stdout, stderr, code := runExe(t, exes[pt.name])
		got := lines(stdout)
		fail := func(why string) {
			t.Errorf("%s: %s\nexit %d\nstdout:\n%s\nstderr:\n%s\nsource:\n%s", pt.name, why, code, stdout, firstLines(stderr, 20), pt.prog.src)
		}
		defect := b.name == "defect" || cl.name == "defect"
		switch {
		case defect:
			// A defect stops the run: T's FAIL line, no Later, exit 2,
			// and the runtime reports every reason, the defects and what
			// unwound beside them.
			if code != 2 || strings.Join(got, "\n") != "FAIL T: a defect, which stops the run" {
				fail("a defect must print T's defect FAIL line, stop the run before Later and exit 2")
			}
			for _, want := range []string{b.reported, cl.reported} {
				if want != "" && !strings.Contains(stderr, want) {
					fail(fmt.Sprintf("the runtime's report lost the reason %q", want))
				}
			}
			// Each index defect keeps the stack at its panic, so its report
			// names the Ergo frame that faulted first, the bare carrier's
			// too, whose panic the case itself records.
			faults, framed := 0, 0
			errLines := lines(stderr)
			for i, ln := range errLines {
				if strings.HasPrefix(ln, "panic: runtime error: index out of range") {
					faults++
					if i+1 < len(errLines) && strings.HasPrefix(errLines[i+1], "\tindex at "+testSourcePath+":") {
						framed++
					}
				}
			}
			if want := strings.Count(b.name+" "+cl.name, "defect"); faults != want || framed != want {
				fail(fmt.Sprintf("the report holds %d index defects, %d of them led by their faulting frame; want %d", faults, framed, want))
			}
		case b.name == "pass" && cl.name == "clean":
			if code != 0 || strings.Join(got, "\n") != "ok T\nok Later\nPASS (2 tests)" {
				fail("a passing case must pass and the run go on")
			}
		default:
			// A failed assert, an escaped typed failure or both fail the
			// case; the run goes on to Later and exits 1.
			want := "FAIL T: a failure escaped the test at main.ergo:"
			escaped := true
			if b.name == "assert" || cl.name == "assert" {
				want, escaped = "FAIL T: assert failed at main.ergo:", false
			}
			if escaped && len(got) > 0 && !strings.HasSuffix(got[0], ": Bang{Code:7}") {
				fail("an escaped failure's FAIL line must end with its text, Bang{Code:7}")
			}
			if code != 1 || len(got) != 3 || !strings.HasPrefix(got[0], want) || got[1] != "ok Later" || got[2] != "FAIL (1 of 2 tests)" {
				fail(fmt.Sprintf("a failed case must print %q…, run Later and exit 1", want))
			}
			// The FAIL line names the first assert that failed: the body's,
			// which ran before the exit action.
			switch {
			case b.name == "assert" && !strings.HasSuffix(got[0], ": one() == 2: left 1, right 2"):
				fail("the FAIL line must name the body's assert")
			case b.name != "assert" && cl.name == "assert" && !strings.HasSuffix(got[0], ": one() == 3: left 1, right 3"):
				fail("the FAIL line must name the exit action's assert")
			}
			if strings.Contains(stderr, "panic: ") {
				fail("a failed case is not a defect")
			}
		}
	}
}
