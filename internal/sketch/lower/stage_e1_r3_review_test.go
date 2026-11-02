package lower

import "testing"

// Preserve the reviewer's admitted closure programs verbatim.
func TestErgoRunE1ReviewClosures(t *testing.T) {
	t.Run("p05-closure-method-value.ergo", func(t *testing.T) {
		wantRun(t, `type Valid { Check() bool, }
type Card { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func All[T Valid](x T) bool { x.Check() }
func Mk[T Valid](x T) func() bool { func() bool { All(Box[T]{V: x}) } }
func MV[T Valid](x T) func() bool { let b = Box[Box[T]]{V: Box[T]{V: x}}; let m = x.Check; func() bool { m() && b.Check() && Checker(b)() } }
func Checker[T Valid](x T) func() bool { x.Check }
func Apply(f func() bool) bool { f() }
func Inst() bool { let f = All[Box[Box[Card]]]; f(Box[Box[Card]]{V: Box[Card]{V: Card{N: 3}}}) }
func Deep[T Valid](x T) bool { let g = func(y T) bool { Apply(Mk(y)) && Apply(MV(y)) }; g(x) }
func main() needs Console {
	Console.Println("\(Apply(Mk(Box[Card]{V: Card{N: 1}}))) \(Apply(MV(Card{N: 1}))) \(Inst()) \(Deep(Box[Box[Card]]{V: Box[Card]{V: Card{N: 0}}}))")
}
`, "true true true false\n")
	})
	t.Run("p06-closure-min.ergo", func(t *testing.T) {
		wantRun(t, `type Valid { Check() bool, }
type Card { N int }
func Check(c Card) bool { c.N > 0 }
func All[T Valid](x T) bool { x.Check() }
func Mk[T Valid](x T) func() bool { func() bool { All(x) } }
func main() needs Console { Console.Println("\(Mk(Card{N: 1})())") }
`, "true\n")
	})
	t.Run("p07-closure-direct.ergo", func(t *testing.T) {
		wantRun(t, `type Valid { Check() bool, }
type Card { N int }
func Check(c Card) bool { c.N > 0 }
func Mk[T Valid](x T) func() bool { func() bool { x.Check() } }
func main() needs Console { Console.Println("\(Mk(Card{N: 1})())") }
`, "true\n")
	})
	t.Run("p16-conv-closure.ergo", func(t *testing.T) {
		wantRun(t, `type Valid { Check() bool, }
type Card { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func Later[T Valid](x T) func() Valid { func() Valid { Box[T]{V: x} } }
func main() needs Console { Console.Println("\(Later(Card{N: 1})().Check())") }
`, "true\n")
	})
}
