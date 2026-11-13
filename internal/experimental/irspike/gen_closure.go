package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// closureFuncs are GenerateClosure's functions: each gets an IR body in
// every seed (G6).
var closureFuncs = []string{"apply", "applyT", "pair", "twice", "adder", "useAdder", "local", "loud", "each", "checked", "guarded", "caught", "chain"}

// GenerateClosure is a program of function values (G6): function literals
// bound to a let, passed as an argument and returned; a trailing block as
// a call's last argument; calls of function values in value and statement
// position, with and without a value; a literal that needs a capability
// through its own environment; a slice of function values; a literal
// that can fail, its failure passed on by try and taken by catch; a
// literal that captures the maker's parameters and locals. main prints
// every function's result for seeded operands.
func GenerateClosure(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	k := 1 + r.IntN(9)
	op := []string{"+", "-", "*"}[r.IntN(3)]
	b.WriteString("type Bad { N int }\n\n")
	b.WriteString("func apply(f func(int) int, x int) int {\n\tlet r = f(x)\n\tr\n}\n\n")
	b.WriteString("func applyT(x int, f func(int) int) int {\n\tlet a = f(x)\n\tlet b = f(a)\n\ta + b\n}\n\n")
	b.WriteString("func pair(f func(int, int) int, x int, y int) int {\n\tlet r = f(x, y)\n\tr\n}\n\n")
	b.WriteString("func twice(f func() int) int {\n\tlet a = f()\n\tlet b = f()\n\ta * 10 + b\n}\n\n")
	b.WriteString("func adder(k int) func(int) int {\n\tfunc(a int) int { a + k }\n}\n\n")
	fmt.Fprintf(&b, "func useAdder(x int) int {\n\tlet f = adder(x %s %d)\n\tlet a = f(x)\n\tlet g = twice() { x * %d }\n\ta + g\n}\n\n", op, k, k)
	fmt.Fprintf(&b, "func local(x int, y int) int {\n\tlet scale = %d\n\tlet f = func(a int) int {\n\t\tlet t = a * scale\n\t\tif t > y {\n\t\t\treturn t - y\n\t\t}\n\t\tt + y\n\t}\n\tlet u = apply(f, x)\n\tlet v = applyT(y, f)\n\tlet w = pair(func(a int, c int) int { a * 10 - c }, u, x)\n\tu + v + w\n}\n\n", 1+r.IntN(4))
	b.WriteString("func loud(n int) needs Console {\n\tlet say = func(s string) needs Console { Console.Println(\"say \\(s) \\(n)\") }\n\tsay(\"a\")\n\tif n > 2 {\n\t\tsay(\"b\")\n\t}\n}\n\n")
	fmt.Fprintf(&b, "func each(x int) int {\n\tlet fs = []func(int) int{func(a int) int { a + 1 }, adder(%d), func(a int) int { a %s x }}\n\tlet mut s = 0\n\tfor f in fs {\n\t\tlet v = f(s + x)\n\t\ts = v\n\t}\n\ts\n}\n\n", k, op)
	fmt.Fprintf(&b, "func checked(x int) int fails Bad {\n\tlet f = func(a int) int fails Bad {\n\t\tif a %% %d == 0 {\n\t\t\treturn Bad{N: a}\n\t\t}\n\t\ta * 3\n\t}\n\tlet v = try f(x)\n\tlet w = try f(v + 1)\n\tw - v\n}\n\n", 2+r.IntN(3))
	b.WriteString("func guarded(x int) fails Bad {\n\tlet g = func(a int) fails Bad {\n\t\tif a > 5 {\n\t\t\treturn Bad{N: a}\n\t\t}\n\t}\n\ttry g(x)\n\ttry g(x - 3)\n}\n\n")
	b.WriteString("func caught(x int) int {\n\tlet v = checked(x) catch {\n\tcase Bad as e:\n\t\t0 - e.N\n\t}\n\tlet mut w = 0\n\tguarded(x) catch {\n\tcase Bad as e:\n\t\tw = e.N * 100\n\t}\n\tv + w\n}\n\n")
	fmt.Fprintf(&b, "func chain(x int) int {\n\tlet f = adder(x)\n\tlet g = func(a int) int {\n\t\tlet h = adder(a)\n\t\tlet t = h(%d)\n\t\tf(t)\n\t}\n\tlet v = g(x)\n\tv\n}\n\n", k)
	b.WriteString("func main() needs Console {\n")
	for i := 0; i < 5; i++ {
		x, y := r.IntN(15)-4, r.IntN(12)
		fmt.Fprintf(&b, "\tConsole.Println(\"%d \\(useAdder(%d)) \\(local(%d, %d)) \\(each(%d)) \\(caught(%d)) \\(chain(%d))\")\n", i, x, x, y, x, x, x)
		fmt.Fprintf(&b, "\tloud(%d)\n", y)
	}
	b.WriteString("}\n")
	return b.String()
}
