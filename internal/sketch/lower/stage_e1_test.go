package lower

import "testing"

// Stage E1 (D-E1, C5, C12): a declared constraint lowers to a Go
// constraint that admits the operators the checker admitted. ordered and
// number are generated type-set interfaces, never any, so < and + on an
// ordered T and -, * and / on a number T compile; comparable is Go's.
// The instances cover a basic type, a defined type over float64, string
// and duration (time.Duration, an int64 underneath), and the prelude's
// Range, whose T is ordered.
func TestErgoRunE1Constraints(t *testing.T) {
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
// parentheses and with a result type the arguments do not fix; and a std
// generic (queue.New) held as a value and called later.
func TestErgoRunE1InstanceValues(t *testing.T) {
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
	let mk = queue.New[int]
	let q = mk(1)
	q.Close()
	Console.Println("\(big) \(first([]string{"x"})) \(len(zero())) \((Max[string])("a", "b"))")
}
`, "4 x 0 b\n")
}

// The accept program for stage E1 runs: declared constraints, an instance
// used as a value, a record Map key and an inferred closure parameter.
func TestErgoRunE1AcceptGenerics(t *testing.T) {
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
