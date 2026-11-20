package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// equalTestBodies is the number of tests GenerateEqualTests writes: each
// gets an IR body in every seed (GP-DEFINED, the operator family).
const equalTestBodies = 4

// GenerateEqualTests is a program of tests whose asserts compare values of
// types that are not basic (GP-DEFINED, "operator on a non-basic type"):
// == and != on slices of ints and of strings, on a record and on a union,
// and a comparison with an if as its operand bound to a local. The seed
// decides the values and which asserts hold, so a run reports passes and
// failures, each with its operands shown.
func GenerateEqualTests(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x6a09e667f3bcc909))
	var b strings.Builder
	pick := func(right, wrong string) string {
		if r.IntN(3) == 0 {
			return wrong
		}
		return right
	}
	ints := func(n, off int) string {
		var xs []string
		for i := 0; i < n; i++ {
			xs = append(xs, fmt.Sprint(i*2+off))
		}
		return "[]int{" + strings.Join(xs, ", ") + "}"
	}
	b.WriteString("type P { X int, Y int }\n\n")
	b.WriteString("type Mode = Hi{N int} | Lo{}\n\n")
	b.WriteString("func evens(n int) []int {\n\tlet mut xs = []int{}\n\tfor i in 0..n {\n\t\txs = xs.Append(i * 2)\n\t}\n\txs\n}\n\n")
	b.WriteString("func words(n int) []string {\n\tif n > 2 {\n\t\treturn []string{\"a\", \"b\"}\n\t}\n\t[]string{\"a\"}\n}\n\n")
	b.WriteString("func at(n int) P {\n\tP{X: n, Y: n * 2}\n}\n\n")
	b.WriteString("func mode(n int) Mode {\n\tif n > 2 {\n\t\treturn Hi{N: n}\n\t}\n\tLo\n}\n\n")
	n := r.IntN(5)
	fmt.Fprintf(&b, "test Slices {\n\tassert evens(%d) == %s\n\tassert words(%d) != []string{%s}\n}\n\n",
		n, pick(ints(n, 0), ints(n+1, 0)), r.IntN(5), pick(`"z"`, `"a"`))
	x := r.IntN(6)
	fmt.Fprintf(&b, "test Records {\n\tassert at(%d) == P{X: %d, Y: %s}\n\tassert at(%d) != at(%s)\n}\n\n",
		x, x, pick(fmt.Sprint(x*2), fmt.Sprint(x*2+1)), x, pick(fmt.Sprint(x+1), fmt.Sprint(x)))
	m := r.IntN(6)
	fmt.Fprintf(&b, "test Unions {\n\tassert mode(%d) == mode(%s)\n\tassert mode(%d) != mode(%s)\n}\n\n",
		m, pick(fmt.Sprint(m), fmt.Sprint(m+1)), m, pick(fmt.Sprint(m+3), fmt.Sprint(m)))
	c := r.IntN(6)
	fmt.Fprintf(&b, "test Branches {\n\tlet same = (if %d > 2 { evens(3) } else { evens(2) }) == %s\n\tassert same\n}\n\n",
		c, pick(map[bool]string{true: ints(3, 0), false: ints(2, 0)}[c > 2], ints(4, 0)))
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(len(evens(2)))\")\n}\n")
	return b.String()
}
