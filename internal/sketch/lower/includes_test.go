package lower

import "testing"

// TestIncludedOperationsLower is the smallest repro of an EXC1 bug found
// by EXH1's chat repair: the Go interface of an operation type listed only
// the operations its body wrote, not those a member `..Closer` includes,
// so the release of a tuple part of that type called a Close the
// interface did not have, and the Go did not type-check. Mutant
// exh1-includes-dropped (the include loop of typeDecl removed).
func TestIncludedOperationsLower(t *testing.T) {
	typecheckGo(t, []*lowered{mustLower(t, "included-closer", `type Conn { ..Closer, Read() int }

type S { n int } implements Conn
func Read(s S) int { s.n }
func Close(s S) {}

func pair(c Conn) (Conn, int) takes c { (c, 1) }

func main() needs Console {
	let c, n = pair(S{n: 2})
	Console.Println("\(c.Read() + n)")
}
`)})
}
