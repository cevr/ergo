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
	needsBuild(t)
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

// A literal of a defined type over a slice or Map type, a generic one and
// one over another defined type included, is that slice or Map literal
// with the defined type's Go name, as Go writes it (`Ints{1, 2}`); an
// elided element literal takes its defined element type. The checker
// admitted these and lowering refused them (S109 "is not a record
// literal") before the D3a rewrite.
func TestErgoRunDefinedCollectionLiterals(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Ints []int
type More Ints
type Counts Map[string, int]
type GS[T] []T
type Lookup[K comparable, V] Map[K, V]

func Size(xs Ints) int { len(xs) }
func sum(xs []int) int {
	let mut n = 0
	for x in xs { n = n + x }
	n
}

func main() needs Console {
	let xs = Ints{1, 2}
	let more = More{3, 4, 5}
	let counts = Counts{"a": 1, "b": 2}
	let gs = GS[string]{"x"}
	let lookup = Lookup[string, Ints]{"k": {7, 8}}
	let nested = []Ints{{1}, {2, 3}}
	let byName = Map[string, Ints]{"n": {4}}
	if xs.Size() == 2 && sum(xs) == 3 && len(more) == 3 && len(counts) == 2 && len(gs) == 1 {
		Console.Println("literals")
	}
	let raw Map[string, Ints] = lookup
	let k = raw.Get("k")
	let n = byName.Get("n")
	match (k, n) {
	case (Some as s, Some as m):
		if s.Value.Size() == 2 && nested[1].Size() == 2 && m.Value[0] == 4 { Console.Println("elided") }
	case _:
		Console.Println("FAIL")
	}
}
`, "literals\nelided\n")
}
