package lower

import "testing"

// Stage D3a rewrite (check-v1): every assignment the checker's one
// assignability relation admits runs, with the conversion its clause
// recorded (types.md, Assignability): clause 2 in and out of an unnamed
// slice, Map, tuple and function type, at a let, an argument, a field, an
// element and a comparison; clause 3 from a defined basic, record and slice
// type into an operation type at a let, a multi-name let (each element,
// local and package-level), a
// tuple literal taken apart, a field, an element and a result; clause 4 at
// a comparison with a variant; clause 5 from a named function type into a
// wider unnamed one. The refusals are the checker's (internal/sketch/check
// stage_d3a_assign_test.go).
func TestErgoRunAssignability(t *testing.T) {
	wantRun(t, `type Fault {}
type Store { Get() int }
type Count int
type Mem { N int }
type Mem2 Mem
type Ints []int
type Counts Map[string, int]
type Pair (int, string)
type F func(n int) int
type G func() int
type Box { s Store }
type Sized { Size() int }

func Get(c Count) int { int(c) }
func Get(m Mem2) int { m.N }
func Size(xs Ints) int { len(xs) }
func read(s Store) int { s.Get() }
func size(s Sized) int { s.Size() }
func plus(n int) int { n + 1 }
func total(xs []int) int { len(xs) }
func two() (Count, Count) { (Count(1), Count(2)) }
func stores() (Store, Store) { let a, b Store = two(); (a, b) }
func give() Store { Count(9) }
func seven() int { 7 }
func widen(g G) func() int fails Fault { g }

let pa, pb Store = two()

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	} else {
		Console.Println("FAIL " + name)
	}
}

func main() needs Console {
	let s Store = Count(3)
	let m Store = Mem2{N: 4}
	let a, b Store = two()
	let p, q Store = (Count(7), s)
	let (x, y) = stores()
	let box = Box{s: Count(5)}
	let all = []Store{Count(1), Mem2{N: 2}}
	let ys Ints = []int{1, 2}
	report("operations", read(s) == 3 && read(m) == 4 && read(give()) == 9 && size(ys) == 2)
	report("multi-let", read(a) + read(b) == 3 && read(p) + read(q) == 10 && read(x) + read(y) == 3 && read(pa) + read(pb) == 3)
	report("field-element", read(box.s) == 5 && read(all[1]) == 2)
	let named Ints = []int{1}
	let raw []int = named
	let xs = raw.Append(2)
	let counts Counts = Map[string, int]{"a": 4}
	let rm Map[string, int] = counts
	let pair Pair = (3, "tuple")
	let pp (int, string) = pair
	let back Pair = pp
	let fn F = plus
	let rawfn func(n int) int = fn
	let nested = [][]int{named}
	report("underlying", xs[1] == 2 && len(rm) == 1 && rawfn(5) == 6 && total(named) == 1 && len(nested[0]) == 1)
	let o Option[int] = Some{1}
	report("compare", back == pair && pair == pp && o != None)
	let w = widen(seven)
	let r = w() catch {
	case Fault:
		0
	}
	report("signature", r == 7)
}
`, "operations\nmulti-let\nfield-element\nunderlying\ncompare\nsignature\n")
}
