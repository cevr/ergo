package lower

import "testing"

// Stage C round 3 review M1: a catch whose value is discarded, over a
// fallible expression that has a value, builds and runs; its caught value
// is consumed, its failure still reaches the arms. The five measured
// shapes, a pattern catch and an empty arm, through the public CLI.
func TestErgoRunDiscardedCatchOverValue(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type E {}
type X {}
type R { B bool }
func g(c bool) int fails E { if c { return E }; 7 }
func gx(c bool) int fails E | X { if c { return E }; 8 }
func r(c bool) int fails R { if c { return R{B: true} }; 9 }
func note(s string) int needs Console { Console.Println(s); 0 }
func word(s string) string needs Console { Console.Println(s); s }

func voidTail(b bool) needs Console { g(b) catch { case E: note("void tail") } }
func ifArm(b bool) needs Console {
	if b { g(b) catch { case E: note("if arm") } } else { note("if else") }
}
func mixedArms(b bool) needs Console {
	g(b) catch { case E: if b { note("int arm") } else { word("string arm") } }
}
func middle(b bool) int needs Console {
	g(b) catch { case E: note("middle") }
	3
}
func tried(b bool) fails X needs Console { try gx(b) catch { case E: note("tried") } }
func branch(b bool) needs Console { (if b { g(b) } else { 3 }) catch { case E: note("branch") } }
func pattern(b bool) needs Console { r(b) catch { case R{B: true}: note("pattern"); case R: note("other") } }
func empty(b bool) needs Console {
	g(b) catch {
	case E:
	}
	note("empty")
}

func main() needs Console {
	voidTail(true)
	voidTail(false)
	ifArm(true)
	ifArm(false)
	mixedArms(true)
	mixedArms(false)
	if middle(true) + middle(false) == 6 { Console.Println("middle 6") }
	tried(true) catch { case X: Console.Println("X") }
	tried(false) catch { case X: Console.Println("X") }
	branch(true)
	branch(false)
	pattern(true)
	pattern(false)
	empty(true)
	empty(false)
}
`, "void tail\nif arm\nif else\nint arm\nmiddle\nmiddle 6\ntried\nbranch\npattern\nempty\nempty\n")
}

// Found by TestCheckCorpusBuilds: a union of two instances of one generic
// record, V[A] | V[B], declares its marker method once, on V, and a match
// over it tells the instances apart.
func TestErgoRunUnionOfOneGenericRecord(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A {}
type B {}
type V[T] {}
type W[T, Q] {}
type P = V[A] | V[B]
type G[T] = W[T, A] | W[T, []T]

func h(p P) int {
	match p {
	case V[A]: 1
	case V[B]: 2
	}
}
func k(g G[B]) int {
	match g {
	case W[B, A]: 3
	case W[B, []B]: 4
	}
}

func main() needs Console {
	if h(V[A]) == 1 && h(V[B]) == 2 && k(W[B, A]) == 3 && k(W[B, []B]) == 4 {
		Console.Println("instances")
	}
}
`, "instances\n")
}
