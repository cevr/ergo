package lower

import ()

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
