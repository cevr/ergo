package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// testBodies is the number of tests GenerateTests writes: each gets an IR
// body in every seed (G6).
const testBodies = 9

// GenerateTests is a program of test declarations (G6): asserts whose
// failure shows its operands (a comparison of ints, of strings, of a
// multi-line string, which adds a line diff, and of bools), asserts that
// show only their source text (a call, a comparison of two constants), a
// test whose try passes a failure on, a property test of a seed, and a
// test that calls a function value. The seed decides which asserts hold,
// so a run reports passes and failures with their texts.
func GenerateTests(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	k := 1 + r.IntN(6)
	pick := func(right, wrong string) string {
		if r.IntN(3) == 0 {
			return wrong
		}
		return right
	}
	b.WriteString("type Bad { N int }\n\n")
	fmt.Fprintf(&b, "func scale(n int) int {\n\tn * %d\n}\n\n", k)
	b.WriteString("func even(n int) bool {\n\tn % 2 == 0\n}\n\n")
	b.WriteString("func name(n int) string {\n\tif n > 3 {\n\t\treturn \"big\"\n\t}\n\t\"small\"\n}\n\n")
	b.WriteString("func lines(n int) string {\n\tif n > 1 {\n\t\treturn \"a\\nb\\nc\"\n\t}\n\t\"a\\nx\\nc\"\n}\n\n")
	b.WriteString("func check(n int) int fails Bad {\n\tif n > 4 {\n\t\treturn Bad{N: n}\n\t}\n\tn + 1\n}\n\n")
	x := r.IntN(8)
	fmt.Fprintf(&b, "test Ints {\n\tlet a = scale(%d)\n\tassert a == %s\n\tassert scale(a) >= %d\n}\n\n", x, pick(fmt.Sprint(x*k), fmt.Sprint(x*k+1)), r.IntN(40))
	fmt.Fprintf(&b, "test Strings {\n\tassert name(%d) == %s\n\tassert %q != name(%d)\n}\n\n", x, pick(fmt.Sprintf("%q", map[bool]string{true: "big", false: "small"}[x > 3]), `"medium"`), pick("medium", "big"), 5)
	fmt.Fprintf(&b, "test Lines {\n\tassert lines(%d) == \"a\\nb\\nc\"\n}\n\n", r.IntN(3))
	fmt.Fprintf(&b, "test Bools {\n\tassert even(%d) == %s\n\tassert even(%d)\n}\n\n", x, pick(fmt.Sprint(x%2 == 0), fmt.Sprint(x%2 != 0)), 2*r.IntN(3)+r.IntN(2))
	b.WriteString("test Constants {\n\tassert 1 < 2\n\tassert scale(0) == 0\n}\n\n")
	fmt.Fprintf(&b, "test Passed {\n\tlet v = try check(%d)\n\tassert v > 0\n}\n\n", r.IntN(7))
	fmt.Fprintf(&b, "test Caught {\n\tlet v = check(%d) catch {\n\tcase Bad as e:\n\t\t0 - e.N\n\t}\n\tassert v %s 0\n}\n\n", r.IntN(7), pick(">", "<"))
	fmt.Fprintf(&b, "test Holds(seed int) {\n\tlet n = seed %% 50\n\tassert scale(n) %s n * %d\n}\n\n", pick("==", "!="), k)
	fmt.Fprintf(&b, "test Values {\n\tlet f = func(a int) int { a + %d }\n\tlet mut s = 0\n\tfor i in []int{1, 2, 3} {\n\t\tlet v = f(i)\n\t\ts = s + v\n\t}\n\tassert s == %s\n}\n\n", k, pick(fmt.Sprint(6+3*k), fmt.Sprint(7+3*k)))
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(scale(2))\")\n}\n")
	return b.String()
}
