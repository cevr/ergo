package lower

import (
	"fmt"
	"strings"
	"testing"
)

const nestedWitnessDecls = `type Valid { Check() bool, }
type Card { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func All[T Valid](x T) bool { x.Check() }
`

// A finite chain has no arbitrary witness depth limit (E1 r2 M1).
func TestErgoRunE1FiniteWitnessChains(t *testing.T) {
	needsBuild(t)
	for _, depth := range []int{1, 8, 10, 50} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			typ, value := "Card", "Card{N: 1}"
			for range depth {
				typ = "Box[" + typ + "]"
				value = typ + "{V: " + value + "}"
			}
			src := nestedWitnessDecls + `func main() needs Console { Console.Println("\(All(VALUE))") }`
			wantRun(t, strings.ReplaceAll(src, "VALUE", value), "true\n")
		})
	}
}

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
