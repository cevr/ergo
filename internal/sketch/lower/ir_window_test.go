package lower

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestIRWindowWritesFromItsCalls is the window pass on the IR (types.md,
// Laws 14 Windows; ir_window.go), read from LowerIR's IR in process (the
// promised fact is which calls, with which arguments, a body makes, so no
// Go is built). Every law is admitted (admitAll: the engine, apart from
// the evidence). Agreement of the rewritten program's output with the
// reference lowering is TestRewriteAgrees's (full tier, its IR leg).
//
//   - Joins: `c.Add(1); c.Add(2)` is one Add of the computed constant 3.
//   - Swap: `c.Inc(); c.Add(x)` is `c.Add(x); c.Inc()`: the moved Add
//     reads x, the second call's own operand.
//   - Two: `c.Inc(); d.Inc()` on two locals is no window: no Twice.
//   - Shape: a law whose right side, Halve, takes the provider by value
//     while the window's Incs take it by pointer cannot be written from
//     the window's call nodes, so the window keeps its two Incs (without
//     the refusal, Go would get Halve(&c)).
//
// Mutants: lf6w-windows-off (Joins keeps two Adds), lf6w-constant-dropped
// (Joins's Add reads 1), lf6w-passthrough-first-call (Swap's Add reads
// no x), lf6w-window-any-receiver (Two calls Twice), lf6w-shape-unchecked
// (Shape calls Halve).
func TestIRWindowWritesFromItsCalls(t *testing.T) {
	defaultLoweringOnly(t)
	saved := inlineLowering
	inlineLowering = "call" // the operations stay calls, so the IR shows them
	defer func() { inlineLowering = saved }()
	src := `type T {
	mut Add(n int),
	mut Inc(),
	mut Twice(),
	Halve(),
	Read() int,

	law J(a int, b int) { Add(a); Add(b) } == { Add(a + b) },
	law S(a int) { Inc(); Add(a) } == { Add(a); Inc() },
	law W() { Inc(); Inc() } == { Twice() },
}

type P { n int } implements T

func Add(p mut P, n int) { p.n = p.n + n }
func Inc(p mut P) { p.n = p.n + 1 }
func Twice(p mut P) { p.n = p.n + 2 }
func Halve(p P) { }
func Read(p P) int { p.n }

type U {
	mut Inc(),
	Halve(),
	Read() int,

	law H() { Inc(); Inc() } == { Halve() },
}

type Q { n int } implements U

func Inc(q mut Q) { q.n = q.n + 1 }
func Halve(q Q) { }
func Read(q Q) int { q.n }

func Joins() int {
	let mut c = P{n: 0}
	c.Add(1)
	c.Add(2)
	c.Read()
}

func Swap(x int) int {
	let mut c = P{n: 0}
	c.Inc()
	c.Add(x)
	c.Read()
}

func Two() int {
	let mut c = P{n: 0}
	let mut d = P{n: 0}
	c.Inc()
	d.Inc()
	c.Read() + d.Read()
}

func Shape() int {
	let mut c = Q{n: 0}
	c.Inc()
	c.Inc()
	c.Read()
}

func main() {
	let _ = Joins()
	let _ = Swap(4)
	let _ = Two()
	let _ = Shape()
}
`
	u := factsUnit(t, src)
	u.Rewrite = admitAll{}
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	calls := irCallTexts(t, files)
	for _, fn := range []string{"Joins", "Swap", "Two", "Shape"} {
		if _, ok := calls[fn]; !ok {
			t.Fatalf("%s has no IR body (fallbacks %v)", fn, rep.Fallback)
		}
	}
	for _, c := range []struct{ fn, want string }{
		{"Joins", "Add(3) Read()"},
		{"Swap", "Add(local0) Inc() Read()"}, // local 0 is the parameter x
		{"Two", "Inc() Inc() Read() Read()"},
		{"Shape", "Inc() Inc() Read()"},
	} {
		if got := strings.Join(calls[c.fn], " "); got != c.want {
			t.Errorf("%s calls %s, want %s", c.fn, got, c.want)
		}
	}
	if t.Failed() {
		for _, f := range files {
			if f.Path == IRFileName {
				t.Logf("IR:\n%s", f.Data)
			}
		}
	}
}

// irCallTexts are the calls each IR function makes, by its name, in the
// order the body holds them, each with its arguments past the receiver
// (the call's X): a constant's value, localN for a read of local N, else
// the operand's kind.
func irCallTexts(t *testing.T, files []File) map[string][]string {
	t.Helper()
	var ir irFile
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := map[string][]string{}
	for _, f := range ir.Funcs {
		name := f.Name
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		var expr func(e *irExpr)
		var stmts func(list []*irStmt)
		expr = func(e *irExpr) {
			if e == nil {
				return
			}
			if e.K == "call" || e.K == "pcall" || e.K == "mcall" {
				var args []string
				for _, a := range e.Args {
					switch {
					case a.K == "const":
						args = append(args, a.V)
					case a.K == "local":
						args = append(args, "local"+strconv.Itoa(a.I))
					default:
						args = append(args, a.K)
					}
				}
				out[name] = append(out[name], e.Name+"("+strings.Join(args, ", ")+")")
			}
			expr(e.X)
			expr(e.Y)
			for _, a := range e.Args {
				expr(a)
			}
			stmts(e.Body)
		}
		stmts = func(list []*irStmt) {
			for _, s := range list {
				expr(s.X)
				expr(s.Y)
				stmts(s.Then)
				stmts(s.Else)
				stmts(s.Body)
			}
		}
		out[name] = append(out[name][:0:0], out[name]...)
		stmts(f.Body)
	}
	return out
}
