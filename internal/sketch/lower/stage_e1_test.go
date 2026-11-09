package lower

import (
	"testing"
)

// Stage E1 (D-E1, C5, C12): a declared constraint lowers to a Go
// constraint that admits the operators the checker admitted. ordered and
// number are generated type-set interfaces, never any, so < and + on an
// ordered T and -, * and / on a number T compile; comparable is Go's.
// The instances cover a basic type, a defined type over float64, string
// and duration (time.Duration, an int64 underneath), and the prelude's
// Range, whose T is ordered.
func TestErgoRunE1Constraints(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Celsius float64

func Max[T ordered](a, b T) T {
	if a > b { a } else { b }
}

func Double[T number](x T) T {
	x + x
}

func Neg[T number](x T) T {
	-x * x / x
}

func Cat[T ordered](a, b T) T {
	a + b
}

func Same[T comparable](a, b T) bool {
	a == b
}

func Span[T ordered](r Range[T]) T {
	r.End
}

func main() needs Console {
	let r = Range[string]{Start: "a", End: "z"}
	let d = Max(2s, 3s)
	Console.Println("\(Max(1, 2)) \(Double(2.5)) \(Neg(3)) \(Cat("a", "b")) \(Same(1, 1)) \(Span(r)) \(Max(Celsius(1.5), Celsius(0.5)) == Celsius(1.5)) \(d == 3s)")
	for i in 0..2 {
		Console.Println("\(i)")
	}
}
`, "2 5 -3 ab true z true true\n0\n1\n")
}

// Stage E1 (D-E1, C7): an explicit instance used as a value lowers from
// Info.Instances, never from a re-derived instantiation. The cases cover a
// program generic passed as an argument, held in a let, called in
// parentheses and with a result type the arguments do not fix; and a
// literal that wraps the std generic queue.New, held and called later (an
// extern is call-only, P3: queue.New[int] itself is no value).
func TestErgoRunE1InstanceValues(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import queue

func Max[T ordered](a, b T) T {
	if a > b { a } else { b }
}

func First[T](xs []T) T {
	xs[0]
}

func Zero[T]() []T {
	[]T{}
}

func apply(f func(int, int) int) int {
	f(3, 4)
}

func main() needs Console {
	let big = apply(Max[int])
	let first = First[string]
	let zero = Zero[int]
	let mk = func(n int) queue.Queue[int] { queue.New[int](n) }
	let q = mk(1)
	q.Close()
	Console.Println("\(big) \(first([]string{"x"})) \(len(zero())) \((Max[string])("a", "b"))")
}
`, "4 x 0 b\n")
}

// The accept program for stage E1 runs: declared constraints, an instance
// used as a value, a record Map key and an inferred closure parameter.
func TestErgoRunE1AcceptGenerics(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Point { X int, Y int }

func Max[T ordered](a, b T) T {
	if a > b { a } else { b }
}

func Same[T comparable](a, b T) bool {
	a == b
}

func Twice[T](x T, f func(T) T) T {
	f(f(x))
}

func apply(f func(int, int) int) int {
	f(3, 4)
}

func main() needs Console {
	let m = Map[Point, string]{Point{X: 1, Y: 2}: "p"}
	let found = match m.Get(Point{X: 1, Y: 2}) {
	case Some{v}:
		v
	case None:
		"none"
	}
	let big = apply(Max[int])
	let word = Max("a", "b")
	let same = Same(Point{X: 1, Y: 2}, Point{X: 1, Y: 2})
	let twice = Twice(3, func(x) { x * 2 })
	Console.Println("\(big) \(word) \(found) \(same) \(twice)")
}
`, "4 b p true 12\n")
}

// A type parameter a method set constrains lowers to Go any with a witness
// (D-E1; stage E1 orchestrator answer 2): operations on a value and through
// a pointer, a 196m function and a generic one, a caller's witness passed
// on, an operation type's value, fallible and no-result operations, a
// constraint that names its own parameter, a method value, an instance used
// as a value, and an operations conversion inside a generic function (C8).
func TestErgoRunE1Witnesses(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Valid {
	Check() bool,
}

type Both {
	Check() bool,
	Name() string,
}

type Scaler {
	Scale(n int) int fails Bad,
	Touch(),
}

type Bad { Why string }

type Lesser[K] {
	Less(other K) bool,
}

type Card { Number string }

type Num { V int }

type Box[T] { V T }

type Holder[T Valid] { Item T }

func Check(c Card) bool {
	c.Number != ""
}

func Check[T](b Box[T]) bool {
	true
}

func Name(c Card) string {
	"card " + c.Number
}

func Scale(n Num, k int) int fails Bad {
	if k < 0 {
		return Bad{Why: "negative"}
	}
	n.V * k
}

func Touch(n Num) {
}

func Less(a Num, b Num) bool {
	a.V < b.V
}

func All[T Valid](xs []T) bool {
	for x in xs {
		if !x.Check() {
			return false
		}
	}
	true
}

func Named[T Both](x T) string {
	if All([]T{x}) { x.Name() } else { "invalid" }
}

func AsValid[T Valid](x T) Valid {
	x
}

func One[T Valid](x *T) bool {
	x.Check()
}

func ScaleAll[T Scaler](xs []T, k int) int fails Bad {
	let mut sum = 0
	for x in xs {
		x.Touch()
		let n = try x.Scale(k)
		sum = sum + n
	}
	sum
}

func Min[T Lesser[T]](a, b T) T {
	if a.Less(b) { a } else { b }
}

func Checker[T Valid](x T) func() bool {
	x.Check
}

func Ok[T Valid](h Holder[T]) bool {
	h.Item.Check()
}

func Big[T ordered](b Box[T], x T) bool {
	b.V > x
}

func Pair[A Valid, B Both](a A, b B) string {
	if a.Check() { b.Name() } else { "none" }
}

func AsValidBox[T](b Box[T]) Valid {
	b
}

func pick(f func([]Card) bool) bool {
	f([]Card{Card{Number: "9"}})
}

func main() needs Console {
	let mut c = Card{Number: "7"}
	let mut d = Card{Number: ""}
	let v = AsValid(c)
	let s = ScaleAll([]Num{Num{V: 2}, Num{V: 3}}, 10) catch {
	case Bad:
		0
	}
	let bad = ScaleAll([]Num{Num{V: 2}}, -1) catch {
	case Bad:
		-1
	}
	let m = Min(Num{V: 5}, Num{V: 4})
	let f = Checker(c)
	let all = All[Card]
	let vs = []Valid{c}
	Console.Println("\(v.Check()) \(Named(c)) \(s) \(bad) \(m.V) \(f()) \(Ok(Holder[Card]{Item: c})) \(Big(Box[int]{V: 3}, 2)) \(pick(all))")
	Console.Println("\(All([]Card{c, d})) \(All(vs)) \(All([]Box[int]{Box[int]{V: 1}})) \(One(&c)) \(One(&d))")
	Console.Println("\(Pair(c, c)) \(Pair(d, c)) \(AsValidBox(Box[string]{V: "x"}).Check())")
}
`, "true card 7 50 -1 4 true true true true\nfalse true true true false\ncard 7 none true\n")
}
