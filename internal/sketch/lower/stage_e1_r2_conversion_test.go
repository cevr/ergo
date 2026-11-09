package lower

import "testing"

// The Conversion fact supplies the instance and witnesses of each
// generic table function, including a conversion in a generic body.
func TestErgoRunE1ConversionWitness(t *testing.T) {
	needsBuild(t)
	wantRun(t, nestedWitnessDecls+`func AsValid[T Valid](b Box[T]) Valid { b }
func main() needs Console {
	let c = Card{N: 1}
	let vs = []Valid{Box[Card]{V: c}}
	let v = AsValid(Box[Card]{V: c})
	Console.Println("\(vs[0].Check()) \(v.Check())")
}`, "true true\n")
}
