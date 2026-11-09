package lower

import (
	"testing"
)

// Stage C round 2 review witnesses, run through the public CLI.

// m4: a with, try or catch whose value is discarded discards its call
// tails (one statement-position rule, rows.md Totality 3), and the lowered
// program runs each of them: call tails of two types, a fallible tail
// under a statement catch, and a with in a void tail.
func TestErgoRunDiscardedWith(t *testing.T) {
	needsBuild(t)
	wantRun(t, `let W = context {}
type E {}
func g(b bool) int fails E { if b { return E }; 7 }
func each(f func()) { f() }
func i() int needs Console { Console.Println("i"); 1 }
func s() string needs Console { Console.Println("s"); "s" }
func pi() int { 1 }
func ps() string { "s" }
func f(b bool) needs Console {
	with W { if b { i() } else { s() } }
	with W { Console.Println("in with") }
	with W { g(b) } catch { case E: s() }
	with W { if b { g(b) } else { s() } } catch { case E: Console.Println("caught E") }
	each() { with W { if b { pi() } else { ps() } } }
}
func main() needs Console {
	f(true)
	f(false)
	Console.Println("ok")
}
`, "i\nin with\ns\ncaught E\ns\nin with\ns\nok\n")
}
