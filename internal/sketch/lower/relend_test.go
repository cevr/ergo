package lower

import "testing"

// TestRelendLowers: &x of a mut parameter passes its borrow on
// (Info.Relends; types.md, Borrows 1), on a parameter, through a
// function value and in a generic body. Mutant killed (ledger
// modes3-relend-text): the text lowering writes &x as Go's &x, a **T
// that go/types refuses at the call.
func TestRelendLowers(t *testing.T) {
	o := mustLower(t, "relend", `type Tally { n int }

func Bump(t mut Tally) { t.n = t.n + 1 }

func relay(t mut Tally) { Bump(&t) }

func twice[T](x mut T, f func(v mut T)) {
	f(&x)
	f(&x)
}

func main() needs Console {
	let mut t = Tally{n: 0}
	relay(&t)
	twice(&t, func(v mut Tally) { Bump(&v) })
	Console.Println("\(t.n)")
}
`)
	typecheckGo(t, []*lowered{o})
}
