package lower

import (
	"regexp"
	"strings"
	"testing"
)

// TestGenericInstantiation: generic functions and types lower to Go
// generics, one declaration each whatever the number of instances (no
// specialised copies, so instantiation work is finite: Go's GC-shape
// compilation, types.md Generics 5); recursive and mutually recursive
// generic calls; several type instances of one function and one type; one
// higher-order function instantiated with several rows (pure, needing
// Console, failing), which the uniform function-value shape makes one Go
// function; same-named 196m functions on distinct records as Go methods,
// which also satisfy an operation type directly; and source names that
// collide with generated or Go-reserved names, which the namer separates
// by identity.
func TestGenericInstantiation(t *testing.T) {
	built := runBehaviors(t, genericCases())
	checkGenericDecls(t, built)
}

// genericCases are TestGenericInstantiation's programs.
func genericCases() []behavior {
	return []behavior{{
		name: "recursion",
		src: `func count[T](xs []T, n int) int {
	if n == 0 {
		return 0
	}
	return 1 + count[T](xs, n - 1)
}

func even[T](x T, n int) bool {
	if n == 0 {
		return true
	}
	return odd[T](x, n - 1)
}

func odd[T](x T, n int) bool {
	if n == 0 {
		return false
	}
	return even[T](x, n - 1)
}

type Pair[K comparable, V] { Key K, Value V }

type Just[T] { Value T }
type Nothing {}
type Maybe[T] = Just[T] | Nothing

func orElse[T](m Maybe[T], d T) T {
	match m {
	case Just as j:
		return j.Value
	case Nothing:
		return d
	}
}

func wrap[T](ok bool, v T) Maybe[T] {
	if ok {
		return Just[T]{Value: v}
	}
	return Nothing
}

func main() needs Console {
	if count([]int{1}, 3) == 3 && count([]string{"a"}, 5) == 5 {
		Console.Println("counted")
	}
	if even(1, 10) && odd("x", 7) && !even(true, 3) {
		Console.Println("parity")
	}
	let p = Pair[string, int]{Key: "k", Value: 4}
	let q = Pair[int, string]{Key: 4, Value: "v"}
	if p.Value == q.Key {
		Console.Println(p.Key + q.Value)
	}
	Console.Println(orElse(wrap(true, "just"), "none") + " " + orElse(wrap(false, "just"), "none"))
	if orElse(wrap(true, 7), 0) == 7 {
		Console.Println("int instance")
	}
}
`,
		stdout: "counted\nparity\nkv\njust none\nint instance\n",
	}, {
		name: "rows",
		src: `type Bad { At string }

func apply[A, E, R](f func(A) A fails E needs R, x A) A fails E needs R {
	return try f(x)
}

func double(n int) int { return n * 2 }

// pure binds no environment: apply gets none, since R is empty here.
func pure(n int) int { return apply(double, n) }

func main() needs Console {
	let a = apply(func(n int) int { return n + 1 }, 1)
	let b = apply(double, a)
	let c = apply(func(s string) string needs Console {
		Console.Println("inside " + s)
		return s + "!"
	}, "hi")
	let d = apply(func(n int) int fails Bad {
		if n > 3 {
			return Bad{At: "big"}
		}
		return n
	}, b) catch {
	case Bad as e:
		Console.Println("failed " + e.At)
		0
	}
	if b == 4 && d == 0 && pure(5) == 10 {
		Console.Println(c)
	}
}
`,
		stdout: "inside hi\nfailed big\nhi!\n",
	}, {
		name: "samename",
		src: `type Shape { Area() int }

type Circle { R int }
type Square { S int }

func Area(c Circle) int { return 3 * c.R * c.R }
func Area(s Square) int { return s.S * s.S }

func total(a Shape, b Shape) int { return a.Area() + b.Area() }

func main() needs Console {
	let c = Circle{R: 2}
	let s = Square{S: 3}
	if c.Area() == 12 && s.Area() == 9 && total(c, s) == 21 {
		Console.Println("areas")
	}
}
`,
		stdout: "areas\n",
	}, {
		name: "names",
		src: `type Env { V int }
type Counter { Value int }

func Value(c Counter) int { return c.Value + 100 }

func ergo(n int) int { return n + 1 }
func time(n int) int { return n + 2 }
func buildW(n int) int { return n + 3 }

type Num { Get() int }
type Fixed {}

func Get(_ Fixed) int { return 5 }

let W = context {
	Num: Fixed,
}

func main_1() int { return 9 }

func main() needs Console {
	let env = Env{V: 1}
	let v = ergo(env.V)
	let err = time(v)
	let t = buildW(err)
	let b = with W { Num.Get() + t }
	let old = Counter{Value: 1}
	if b == 12 && old.Value == 1 && Value(old) == 101 && main_1() == 9 {
		Console.Println("names")
	}
}
`,
		stdout: "names\n",
	}}
}

// checkGenericDecls checks the emitted Go declarations.
func checkGenericDecls(t *testing.T, built map[string]*lowered) {
	if o := built["recursion"]; o != nil {
		goSrc := o.mainGo()
		for _, decl := range []string{`func count\[`, `func even\[`, `func odd\[`, `type Pair\[`, `type Maybe\[`, `func orElse\[`} {
			if n := len(regexp.MustCompile(`(?m)^`+decl).FindAllString(goSrc, -1)); n != 1 {
				t.Errorf("%s is declared %d times in Go, want once (one generic declaration for every instance)", decl, n)
			}
		}
	}
	if o := built["rows"]; o != nil {
		if n := strings.Count(o.mainGo(), "func apply["); n != 1 {
			t.Errorf("apply is declared %d times, want once for its three row instances", n)
		}
	}
	if o := built["samename"]; o != nil {
		goSrc := o.mainGo()
		// Each Area is pure, so its Go method takes no environment (pure.go,
		// D-S5-10).
		for _, m := range []string{"func (c Circle) Area() int", "func (s Square) Area() int"} {
			if !strings.Contains(goSrc, m) {
				t.Errorf("Go lacks the method %q:\n%s", m, goSrc)
			}
		}
		// An operation takes the caller's task first (rows.md, Rows 7), so
		// each record reaches Shape through an adapter; a pure method needs
		// no environment of that task, so the adapter discards the task
		// (stage D3b, D2; D-S5-10).
		for _, m := range []string{"func (a *opsCircleAsShape) Area(_ *rt.Task) int {\n\treturn (a.v).Area()\n}", "func (a *opsSquareAsShape) Area(_ *rt.Task) int {\n\treturn (a.v).Area()\n}"} {
			if !strings.Contains(goSrc, m) {
				t.Errorf("Go lacks the adapter method %q:\n%s", m, goSrc)
			}
		}
	}
	if o := built["names"]; o != nil {
		goSrc := o.mainGo()
		// The field Value keeps its name; the function Value becomes a Go
		// function, since Go has no field and method of one name.
		if !strings.Contains(goSrc, "Value int") || strings.Contains(goSrc, ") Value() int") {
			t.Errorf("the field and the function Value collide:\n%s", goSrc)
		}
	}
}
