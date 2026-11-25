package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func checkOne(t *testing.T, src string) []check.Diagnostic {
	t.Helper()
	_, diags := check.CheckSource(src)
	return diags
}

func wantClean(t *testing.T, name, src string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if _, err := syntax.Parse(src); err != nil {
			t.Fatalf("parse failed: %v\nsrc:\n%s", err, src)
		}
		if diags := checkOne(t, src); len(diags) > 0 {
			t.Fatalf("want clean check, got:\n%v\nsrc:\n%s", diags, src)
		}
	})
}

func wantParseErr(t *testing.T, name, src string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		_, err := syntax.Parse(src)
		if err == nil {
			t.Fatalf("want parse error (S001), got clean parse\nsrc:\n%s", src)
		}
	})
}

func wantDiag(t *testing.T, name, src, code, substr string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if _, err := syntax.Parse(src); err != nil {
			t.Fatalf("want check diagnostic %s, got parse error: %v\nsrc:\n%s", code, err, src)
		}
		diags := checkOne(t, src)
		for _, d := range diags {
			if (code == "" || d.Code == code) && (substr == "" || strings.Contains(d.Message, substr)) {
				return
			}
		}
		t.Fatalf("want diagnostic %s containing %q, got:\n%v\nsrc:\n%s", code, substr, diags, src)
	})
}

func TestDieCheckerRules(t *testing.T) {
	// Rule 1: Statement, not value (contextual `die`, never a reserved keyword).
	wantParseErr(t, "value-let-rhs", `
type Broken {}
func f() int defects {
	let x = die Broken{}
	x
}
`)
	wantParseErr(t, "value-call-arg", `
type Broken {}
func send(x int) {}
func f() defects {
	send(die Broken{})
}
`)
	wantParseErr(t, "value-assign-rhs", `
type Broken {}
func f() defects {
	let mut x = 0
	x = die Broken{}
	_ = x
}
`)
	wantClean(t, "contextual-die-names", `
type Box { die int }
func die(n int) int { n + 1 }
func use(b Box) int {
	let dieVal = 3
	let xs = []int{1, 2}
	let _ = xs[0]
	let f = func() int {
		let mut die = 3
		die = 4
		die
	}
	b.die + die(1) + f() + dieVal
}
`)
	wantClean(t, "contextual-die-let-and-index", `
func use() int {
	let mut die = []int{1, 2}
	die[0] = 5
	die[0]
}
`)
	wantClean(t, "die-statement-terminates", `
type Broken {}
func f(broken bool, value int) int defects {
	if broken {
		die Broken
	}
	value
}
`)

	// Rule 2: Payload is owned snapshot-safe data.
	wantClean(t, "payload-nominal-record-and-union", `
type Detail { Code int, Note string }
type Kind = Detail | Other
type Other { Flag bool }
func f(n int) int defects {
	let k Kind = Detail{Code: n, Note: "bad"}
	die k
}
`)
	wantClean(t, "payload-eval-under-fails", `
type Transport { Code int }
type Lost { Code int }
func read() int fails Transport { return Transport{Code: 7} }
func f() int fails Transport defects {
	die Lost{Code: try read()}
}
`)
	wantDiag(t, "payload-rejects-resource-handle", `
import queue
type Lost { Q queue.Queue[int] }
func f(q queue.Queue[int]) int defects {
	die Lost{Q: q}
}
`, "S108", "Lost.Q")
	wantDiag(t, "payload-rejects-borrowed-ref", `
func f(x mut int) int defects {
	die x
}
`, "S108", "borrowed reference")
	wantDiag(t, "payload-rejects-closure", `
type Bad { Fn func(int) int }
func f() int defects {
	die Bad{Fn: func(x int) int { x }}
}
`, "S108", "Bad.Fn")
	wantDiag(t, "payload-rejects-capability", `
func f(c Console) int defects {
	die c
}
`, "S108", "capability")
	wantDiag(t, "payload-rejects-go-any", `
type Bad { V go.Any }
func f(v go.Any) int defects {
	die Bad{V: v}
}
`, "S108", "go.Any")
	wantDiag(t, "payload-rejects-custom-codec", `
type Custom { name string }
func ParseCustom(s string) Custom { Custom{name: s} }
func Wire(c Custom) string { c.name }
func f(c Custom) int defects {
	die c
}
`, "S108", "custom wire codec")

	// Rule 3: Reachability and coverage first.
	wantDiag(t, "dead-arm-with-die-rejected", `
type Broken {}
func f(b bool) int defects {
	match b {
	case true:
		1
	case false:
		0
	case _:
		die Broken{}
	}
}
`, "S108", "already covered")
	wantDiag(t, "missing-variant-with-die-rejected", `
type A {}
type B {}
type C {}
type U = A | B | C
type Broken {}
func f(u U) int defects {
	match u {
	case A:
		1
	case B:
		die Broken{}
	}
}
`, "S103", "C")
	wantClean(t, "reachable-arm-with-die-clean", `
type Simulated {}
func f(b bool) int defects {
	match b {
	case true:
		1
	case false:
		die Simulated
	}
}
`)

	// Rule 4: Explicit effect, no erasure.
	wantDiag(t, "assign-defects-to-plain-rejected", `
type Simulated { Attempt int }
func child(n int) int defects {
	die Simulated{Attempt: n}
}
func bad() {
	let f func(int) int = child
	_ = f(1)
}
`, "S108", "defects")
	wantDiag(t, "generic-forwarding-cannot-erase", `
type Simulated { Attempt int }
func child(n int) int defects {
	die Simulated{Attempt: n}
}
func forward[T](f func(int) T) T { f(1) }
func bad() int {
	forward(child)
}
`, "S108", "")
	wantDiag(t, "generic-identity-keeps-defects", `
type Simulated { Attempt int }
func child(n int) int defects {
	die Simulated{Attempt: n}
}
func id[F](f F) F { f }
func bad() int {
	let g = id(child)
	g(1)
}
`, "S108", "defects")
	wantDiag(t, "interface-impl-defects-rejected", `
type Simulated {}
type Worker { Run(n int) int }
type Bad {} implements Worker
func Run(_ Bad, n int) int defects {
	die Simulated{}
}
`, "S106", "defects")
	wantClean(t, "plain-fits-marked-and-marked-fits-marked", `
type Simulated { Attempt int }
func plain(n int) int { n + 1 }
func child(n int) int defects {
	die Simulated{Attempt: n}
}
func use() int defects {
	let f func(int) int defects = child
	let g func(int) int defects = plain
	f(1) + g(2)
}
`)

	// Rule 5: No local checked-failure promotion in catch arms.
	wantDiag(t, "catch-arm-direct-die-rejected", `
type Transport { Code int }
type Lost { Code int }
func read() int fails Transport { return Transport{Code: 1} }
func bad() int defects {
	read() catch {
	case Transport as e:
		die Lost{Code: e.Code}
	}
}
`, "S108", "catch arm")
	wantDiag(t, "catch-arm-nested-block-die-rejected", `
type Transport { Code int }
type Lost { Code int }
func read() int fails Transport { return Transport{Code: 1} }
func bad(flag bool) int defects {
	read() catch {
	case Transport as e:
		if flag {
			die Lost{Code: e.Code}
		}
		0
	}
}
`, "S108", "catch arm")
	wantDiag(t, "catch-arm-calls-defects-helper-rejected", `
type Transport { Code int }
type Lost { Code int }
func read() int fails Transport { return Transport{Code: 1} }
func boom(code int) int defects { die Lost{Code: code} }
func bad() int defects {
	read() catch {
	case Transport as e:
		boom(e.Code)
	}
}
`, "S108", "catch arm")
	wantClean(t, "catch-arm-returns-typed-failure-or-dormant-callback", `
type Transport { Code int }
type Lost { Code int }
func read() int fails Transport { return Transport{Code: 1} }
func ok1() int fails Transport {
	read() catch {
	case Transport as e:
		return e
	}
}
func ok2() func() int defects {
	let _ = read() catch {
	case Transport as e:
		let cb = func() int defects { die Lost{Code: e.Code} }
		let _ = cb
		0
	}
	func() int defects { 1 }
}
`)

	// Rule 6: No same-task authority at root, global initializer, extern go, or pure/replay boundary.
	wantDiag(t, "main-cannot-die-directly", `
type Lost {}
func main() {
	die Lost{}
}
`, "S108", "main")
	wantDiag(t, "main-cannot-declare-defects", `
type Lost {}
func main() defects {
	die Lost{}
}
`, "S113", "main cannot declare defects")
	wantDiag(t, "global-init-cannot-call-defects", `
type Lost {}
func boom() int defects { die Lost{} }
let x = boom()
`, "S108", "initializer")
	wantDiag(t, "extern-go-cannot-declare-defects", `
extern go strings.ToUpper func bad(s string) string defects
`, "S108", "extern go")
	wantDiag(t, "machine-step-cannot-declare-defects", `
type S = Idle
type Idle {}
type E = Ping
type Ping {}
type Simulated {}
func step(s S, e E) Decision[S, int] defects {
	die Simulated{}
}
type M machine[S, E] {
	initial: Idle,
	step: step,
}
`, "S108", "step cannot declare defects")
	wantDiag(t, "replay-callback-cannot-take-defects", `
type Simulated {}
func child(n int) int defects { die Simulated{} }
func replay(f func(int) int) int { f(1) }
func bad() int {
	replay(child)
}
`, "S108", "defects")
	wantClean(t, "main-observes-child-via-task-go-and-exitof", `
import task
type Simulated { Attempt int }
func child(n int) int defects {
	die Simulated{Attempt: n}
}
func main() {
	let t = task.Go() { child(3) }
	let _ = match task.ExitOf(t) {
	case task.Defected as d:
		match task.ReasonAs[Simulated](d) {
		case Some{Value: r}:
			r.Attempt
		case None:
			-1
		}
	case _:
		0
	}
}
`)
}
