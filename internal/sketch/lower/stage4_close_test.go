package lower

import "testing"

// Stage 4 (S4c item 7): C29. Each checked Close of an owning with's
// provider releases (resources.md, Context ownership; Q-S4-1): an
// operation of the provider's operation type, a generic Close, and a
// Close that takes a pointer to a provider that is a value. The releases
// run when the with's block ends, last acquired first.

// TestCloseFormsRelease runs the three forms, nested, and the F close leg
// of the acceptance (a generic Close that needs Console).
func TestCloseFormsRelease(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import queue

type A { Use() string }

type Releaser { Use() string, Close() }
type R { Q queue.Queue[string] }
func Use(_ R) string { "use R" }
func Close(r R) {
	r.Q.Push("close R") catch {
	case queue.Closed:
	}
}
func make(q queue.Queue[string]) Releaser { R{Q: q} }

type G[T] { Value T }
func Use[T](_ G[T]) string { "use G" }
func Close[T](g G[T]) needs Console { Console.Println("close G") }

type P {}
func Use(_ P) string { "use P" }
func Close(_ *P) needs Console { Console.Println("close P") }

func main() needs Console {
	let q = queue.New[string](4)
	with context { A: make(q) } { Console.Println(A.Use()) }
	let closed = q.Pop() catch {
	case queue.Closed:
		"none"
	}
	Console.Println(closed)
	with context { A: G[int]{Value: 1} } { Console.Println(A.Use()) }
	with context { A: P } {
		Console.Println(A.Use())
		with context { A: G[string]{Value: "s"} } { Console.Println(A.Use()) }
	}
	Console.Println("after")
}
`, "use R\nclose R\nuse G\nclose G\nuse P\nuse G\nclose G\nclose P\nafter\n")
}

// TestCloseGenericFunctionRelease: a generic Close over a type that is not
// a record is a Go function, not a method, so its release passes the type
// arguments its parameter binds against the provider: Close[int].
func TestCloseGenericFunctionRelease(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A { Use() string }

type List[T] []T
func Use[T](l List[T]) string { "use List" }
func Close[T](l List[T]) needs Console { Console.Println("close List \(len(l))") }

func main() needs Console {
	with context { A: List[int]([]int{1, 2}) } { Console.Println(A.Use()) }
	Console.Println("after")
}
`, "use List\nclose List 2\nafter\n")
}

// TestCloseOverriddenNotRun: a provider that a replace overrides is never
// built, so its Close never runs; the replacing provider's Close does.
func TestCloseOverriddenNotRun(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A { Use() string }

type G[T] { Value T }
func Use[T](_ G[T]) string { "use G" }
func Close[T](g G[T]) needs Console { Console.Println("close G") }

type P {}
func Use(_ P) string { "use P" }
func Close(_ *P) needs Console { Console.Println("close P") }

let Base = context { A: P }
let Swapped = context {
	Base,
	replace A: G[int]{Value: 1},
}

func main() needs Console {
	with Swapped { Console.Println(A.Use()) }
	Console.Println("after")
}
`, "use G\nclose G\nafter\n")
}
