package lower

import "testing"

// Stage D3a (check-v1): the prelude's Append, Get and Delete reached by the
// dot form on a slice or a Map (D-D6, D-D8), and a dot call to a generic
// function whose receiver binds its type parameters (D-D8, the TypeArgs
// fact). The refusals are the checker's (internal/sketch/check
// stage_d3a_test.go); every admitted form runs here through the public CLI.

// TestErgoRunPreludeDotCalls: Append adds at the end, from a literal, a
// chain and a generic element type; Get returns Some with the value or
// None for a missing key, over a generic Map too; Delete returns the Map
// without the key (that the argument is unchanged is TestValueRuleAgrees'
// and probe P7delete's); a program's
// own func Get on a local type keeps its name (decision 9).
func TestErgoRunPreludeDotCalls(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Store { N int }

func Get(s Store, k string) int {
	s.N + len(k)
}

func report(name string, ok bool) needs Console {
	if ok {
		Console.Println(name)
	} else {
		Console.Println("FAIL " + name)
	}
}

func push[T](xs []T, x T) []T {
	xs.Append(x)
}

func lookup[K comparable, V](m Map[K, V], k K, d V) V {
	match m.Get(k) {
	case Some{v}:
		v
	case None:
		d
	}
}

func main() needs Console {
	let xs = []int{1, 2}.Append(3)
	report("append", len(xs) == 3 && xs[2] == 3)
	let ys = xs.Append(4).Append(5)
	report("append-chain", len(ys) == 5 && ys[4] == 5)
	let ss = push([]string{}, "a")
	report("append-generic", len(ss) == 1 && ss[0] == "a")
	let mut m = Map[string, int]{"a": 5, "b": 2}
	let hit = match m.Get("a") {
	case Some{v}:
		v
	case None:
		0
	}
	report("get-hit", hit == 5)
	let miss = match m.Get("z") {
	case Some{v}:
		v
	case None:
		-1
	}
	report("get-miss", miss == -1)
	report("get-generic", lookup(m, "b", 0) == 2 && lookup(m, "q", 7) == 7)
	m = m.Delete("a")
	report("delete", len(m) == 1 && lookup(m, "a", 0) == 0)
	report("own-get", Store{N: 1}.Get("ab") == 3)
}
`, "append\nappend-chain\nappend-generic\nget-hit\nget-miss\nget-generic\ndelete\nown-get\n")
}

// TestErgoRunGenericDotCall: p.Swap() and p.Map2(f) call generic functions
// whose receiver binds some or all of their type parameters; the lowered
// call writes every type argument (formerly refused naming D-D8).
func TestErgoRunGenericDotCall(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Pair[A, B] { First A, Second B }

func Swap[A, B](p Pair[A, B]) Pair[B, A] {
	Pair[B, A]{First: p.Second, Second: p.First}
}

func Map2[A, B, C](p Pair[A, B], f func(a A) C) Pair[C, B] {
	Pair[C, B]{First: f(p.First), Second: p.Second}
}

func swapAll[A, B](p Pair[A, B]) Pair[A, B] {
	p.Swap().Swap()
}

type G[T] { v T }

func pick[T, U](g G[T], u U) U {
	u
}

func main() needs Console {
	Console.Println(G[int]{v: 1}.pick("picked"))
	let p = Pair[int, string]{First: 4, Second: "x"}
	let q = p.Swap()
	Console.Println(q.First)
	let r = p.Map2(func(a int) string { "n" })
	Console.Println(r.First + r.Second)
	Console.Println(swapAll(p).Second)
}
`, "picked\nx\nnx\nx\n")
}

// TestErgoRunImportAliasForClash: an import alias that resolves a clash
// with a package-level name (D-D7) names the package; the package-level
// timeout keeps its own name.
func TestErgoRunImportAliasForClash(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import t timeout

func timeout() string {
	"local timeout"
}

func expire() int fails t.Expired {
	return t.Expired
}

func main() needs Console {
	Console.Println(timeout())
	let n = expire() catch { case t.Expired: 7 }
	if n == 7 {
		Console.Println("aliased")
	}
}
`, "local timeout\naliased\n")
}
