package lower

import "testing"

// Stage C round 5 review M1: an operations conversion from every kind of
// source to every kind of operation type runs: a concrete record, a
// pointer, an instantiated generic record of one and of two type
// parameters and pointers to those, to instances of a one- and a
// two-parameter operation type, and the concrete ones also to a plain
// operation type. (A generic record declares no plain operation type its
// instance alone would meet: G[T]'s M returns T, not int, so EXC1's clause
// cannot name J.) The reviewer's two witnesses come first. A pointer
// source is a borrow, which converts only at a let (types.md, Borrows 6),
// so its value is called there.
func TestErgoRunOperationsConversionDomain(t *testing.T) {
	needsBuild(t)
	t.Run("reviewer-generic-source", func(t *testing.T) {
		wantRun(t, `type G[T] { V T } implements A[T]
type A[T] { M() T }
func M[T](g G[T]) T { g.V }
func use(a A[int]) int { a.M() }
func main() needs Console {
	if use(G[int]{V: 9}) == 9 { Console.Println("generic source") }
}
`, "generic source\n")
	})
	t.Run("reviewer-generic-pointer-source", func(t *testing.T) {
		wantRun(t, `type G[T] { V T } implements A[T]
type A[T] { M() T }
func M[T](g G[T]) T { g.V }
func main() needs Console {
	let mut g = G[int]{V: 9}
	let a A[int] = &g
	if a.M() == 9 { Console.Println("generic pointer source") }
}
`, "generic pointer source\n")
	})
	t.Run("domain", func(t *testing.T) {
		wantRun(t, `type J { mut M() int }
type A[T] { mut M() T }
type B[T, U] {
	mut M() T,
	mut K(u U) U,
}

type S {} implements J, A[int], B[int, string]
type P {} implements J, A[int], B[int, string]
type G[T] { V T } implements A[T], B[T, string]
type H[T, U] { V T, W U } implements A[T], B[T, U]

func M(_ S) int { 1 }
func K(_ S, u string) string { u }
func M(_ mut P) int { 2 }
func K(_ mut P, u string) string { u }
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
	let pj J = &p
	let pa A[int] = &p
	let pb B[int, string] = &p
	if pj.M() == 2 && pa.M() == 2 && pb.K("k") == "k" {
		Console.Println("pointer source")
	}
	if useA(g) == 3 && useB(g) == "k" {
		Console.Println("generic source")
	}
	let ga A[int] = &g
	let gb B[int, string] = &g
	if ga.M() == 3 && gb.K("k") == "k" {
		Console.Println("generic pointer source")
	}
	if useA(h) == 4 && useB(h) == "w" {
		Console.Println("two-parameter source")
	}
	let ha A[int] = &h
	let hb B[int, string] = &h
	if ha.M() == 4 && hb.K("w") == "w" {
		Console.Println("two-parameter pointer source")
	}
}
`, "concrete source\npointer source\ngeneric source\ngeneric pointer source\ntwo-parameter source\ntwo-parameter pointer source\n")
	})
}
