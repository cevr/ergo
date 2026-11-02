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
