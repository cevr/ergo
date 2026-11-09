package lower

import (
	"testing"
)

const nestedWitnessDecls = `type Valid { Check() bool, }
type Card { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func All[T Valid](x T) bool { x.Check() }
`

// Passing *T to another constrained function adapts the receiver and
// reuses the enclosing T witness (E1 r2 M2).
func TestErgoRunE1ForwardedPointerWitness(t *testing.T) {
	needsBuild(t)
	wantRun(t, nestedWitnessDecls+`func FromPointer[T Valid](x *T) bool { All(x) }
func main() needs Console {
	let mut c = Card{N: 1}
	Console.Println("\(FromPointer(&c))")
}`, "true\n")
}
