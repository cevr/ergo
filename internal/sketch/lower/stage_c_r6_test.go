package lower

import "testing"

// Stage C round 5 review M1: an operations conversion from every kind of
// source to every kind of operation type runs: a concrete record, a
// pointer, an instantiated generic record of one and of two type
// parameters and pointers to those, to a plain operation type and to
// instances of a one- and a two-parameter one. The reviewer's two
// witnesses come first.
func TestErgoRunOperationsConversionDomain(t *testing.T) {
	needsBuild(t)
	t.Run("reviewer-generic-source", func(t *testing.T) {
		wantRun(t, `type G[T] { V T }
type A[T] { M() T }
func M[T](g G[T]) T { g.V }
func use(a A[int]) int { a.M() }
func main() needs Console {
	if use(G[int]{V: 9}) == 9 { Console.Println("generic source") }
}
`, "generic source\n")
	})
	t.Run("reviewer-generic-pointer-source", func(t *testing.T) {
		wantRun(t, `type G[T] { V T }
type A[T] { M() T }
func M[T](g G[T]) T { g.V }
func use(a A[int]) int { a.M() }
func main() needs Console {
	let mut g = G[int]{V: 9}
	if use(&g) == 9 { Console.Println("generic pointer source") }
}
`, "generic pointer source\n")
	})
	t.Run("domain", func(t *testing.T) {
		wantRun(t, `type J { M() int }
type A[T] { M() T }
type B[T, U] {
	M() T,
	K(u U) U,
}

type S {}
type P {}
type G[T] { V T }
type H[T, U] { V T, W U }

func M(_ S) int { 1 }
func K(_ S, u string) string { u }
func M(_ *P) int { 2 }
func K(_ *P, u string) string { u }
func M[T](g G[T]) T { g.V }
func K[T](g G[T], u string) string { u }
func M[T, U](h H[T, U]) T { h.V }
func K[T, U](h H[T, U], u U) U { h.W }

func useJ(j J) int { j.M() }
func useA(a A[int]) int { a.M() }
func useB(b B[int, string]) string { b.K("k") }

func main() needs Console {
	let s = S
	let mut p = P
	let mut g = G[int]{V: 3}
	let mut h = H[int, string]{V: 4, W: "w"}
	if useJ(s) == 1 && useA(s) == 1 && useB(s) == "k" {
		Console.Println("concrete source")
	}
	if useJ(&p) == 2 && useA(&p) == 2 && useB(&p) == "k" {
		Console.Println("pointer source")
	}
	if useJ(g) == 3 && useA(g) == 3 && useB(g) == "k" {
		Console.Println("generic source")
	}
	if useJ(&g) == 3 && useA(&g) == 3 && useB(&g) == "k" {
		Console.Println("generic pointer source")
	}
	if useJ(h) == 4 && useA(h) == 4 && useB(h) == "w" {
		Console.Println("two-parameter source")
	}
	if useJ(&h) == 4 && useA(&h) == 4 && useB(&h) == "w" {
		Console.Println("two-parameter pointer source")
	}
}
`, "concrete source\npointer source\ngeneric source\ngeneric pointer source\ntwo-parameter source\ntwo-parameter pointer source\n")
	})
}

// D-C5 (stage C round 5 review): a function literal that declares both
// its failure row and its service row runs, under a with and a catch.
// The reviewer's witness, kept in the run matrix.
func TestErgoRunLiteralRows(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Bad {}
type Svc { Get() int }
type P {}
func Get(_ P) int { 40 }
let W = context { Svc: P }
func fail() int fails Bad { Bad }
func invoke() int fails Bad needs Svc {
	let f = func() int fails Bad needs Svc { if Svc.Get() == 40 { fail() } else { 0 } }
	try f()
}
func main() needs Console {
	let n = with W { invoke() } catch { case Bad: 7 }
	if n == 7 { Console.Println("declared literal rows") }
}
`, "declared literal rows\n")
}
