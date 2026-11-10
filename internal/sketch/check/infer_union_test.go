package check

import "testing"

// TestInferVariantThenUnion is the smallest repro of G-exup-2: a type
// parameter that an earlier argument binds to a variant takes the union a
// later argument names, as machines.Next(Green{…}, d) with d a
// Decision[Light, O] needs. Every argument is then assigned to the
// instance, so a variant bound where the match is exact ([]Green) still
// fails at that argument.
func TestInferVariantThenUnion(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "variant then union", src: `

type Green { N int }
type Red { N int }
type Light = Green | Red
type Box[S] { V S }

func Next[S](s S, b Box[S]) S { return b.V }

func F(b Box[Light]) Light {
	return Next(Green{N: 1}, b)
}

func G(b Box[Light]) Light {
	return Next(b.V, b)
}
`},
		{name: "exact variant stays", src: `

type Green { N int }
type Red { N int }
type Light = Green | Red

func Pick[S](xs []S, s S) S { return s }

func F(xs []Green, l Light) Light {
	return Pick(xs, l)
}
`, want: []selfWant{{"S108", "cannot use []Green as []Light", 10}}},
		{name: "element types match exactly", src: `

type Green { N int }
type Red { N int }
type Light = Green | Red

func Both[S](xs []S, ys []S) int { return len(xs) + len(ys) }

func F(xs []Green, ys []Light) int {
	return Both(xs, ys)
}
`, want: []selfWant{{"S108", "does not match", 10}}},
		{name: "only a union widens", src: `

type Engine { Run() int }
type Fast {}

func Run(_ Fast) int { return 1 }

func Two[S](a S, b S) int { return 0 }

func F(f Fast, e Engine) int {
	return Two(f, e)
}
`, want: []selfWant{{"S108", "does not match", 11}}},
	})
}
