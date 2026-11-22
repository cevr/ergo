package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// testBodies is the number of tests GenerateTests writes: each gets an IR
// body in every seed (G6).
const testBodies = 12

// GenerateTests is a program of test declarations (G6): asserts whose
// failure shows its operands (a comparison of ints, of strings, of a
// multi-line string, which adds a line diff, and of bools), asserts that
// show only their source text (a call, a comparison of two constants), a
// test whose try passes a failure on, an assert that a try starts, a property test of a seed, a
// test that calls a function value, and asserts of == on records, a union
// and its variant, and slices. The seed decides which asserts hold,
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
	b.WriteString("import scope\n\ntype Bad { N int }\n\n")
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
	b.WriteString("func raw() string {\n\t\"\\xff\"\n}\n\n")
	b.WriteString("test Constants {\n\tassert 1 < 2\n\tassert scale(0) == 0\n\tlet r = raw()\n\tassert len(r) == 1\n}\n\n")
	fmt.Fprintf(&b, "test Passed {\n\tlet v = try check(%d)\n\tassert v > 0\n}\n\n", r.IntN(7))
	// A try that starts a shown assert covers the comparison: a failure
	// fails the test, else the operands show (FLIP F7 GP).
	y := r.IntN(7)
	fmt.Fprintf(&b, "test Tried {\n\tassert try check(%d) == %s\n}\n\n", y, pick(fmt.Sprint(y+1), fmt.Sprint(y+2)))
	fmt.Fprintf(&b, "test Caught {\n\tlet v = check(%d) catch {\n\tcase Bad as e:\n\t\t0 - e.N\n\t}\n\tassert v %s 0\n}\n\n", r.IntN(7), pick(">", "<"))
	fmt.Fprintf(&b, "test Holds(seed int) {\n\tlet n = seed %% 50\n\tassert scale(n) %s n * %d\n}\n\n", pick("==", "!="), k)
	fmt.Fprintf(&b, "test Values {\n\tlet f = func(a int) int { a + %d }\n\tlet mut s = 0\n\tfor i in []int{1, 2, 3} {\n\t\tlet v = f(i)\n\t\ts = s + v\n\t}\n\tassert s == %s\n}\n\n", k, pick(fmt.Sprint(6+3*k), fmt.Sprint(7+3*k)))
	// == on records, a union and its variant, and slices: the equality
	// the IR writes for a type that is not basic (FLIP F7 CS), its
	// failure showing the operands.
	z := r.IntN(7)
	b.WriteString("type Won { Moves int }\n\ntype Lost { At string }\n\ntype End = Won | Lost\n\ntype Pt { X int, Y int }\n\n")
	b.WriteString("func ending(n int) End {\n\tif n > 3 {\n\t\treturn Won{Moves: n}\n\t}\n\tLost{At: \"gate\"}\n}\n\n")
	// A dropped call of a generic function whose row is empty here: its
	// Go form returns the value and a nil error, both bound and dropped
	// (lru's memo test).
	b.WriteString("func first[E](f func() int fails E) int fails E {\n\ttry f()\n}\n\n")
	fmt.Fprintf(&b, "test Records {\n\tlet _ = first(func() int { 1 })\n\tassert ending(%d) == Won{Moves: %s}\n\tassert Pt{X: %d, Y: 1} == Pt{X: %d, Y: %s}\n\tassert []int{%d, 1} != []int{%d, %s}\n}\n\n", z, pick(fmt.Sprint(z), fmt.Sprint(z+1)), z, z, pick("1", "2"), z, z, pick("2", "1"))
	// A test that opens a frame scope (GP-FRAMES): its exit action runs
	// when the test ends, after a try that may fail it, and raises a
	// defect at a seeded index, so an action that does not run, or a
	// failure the frame's End loses, changes the report.
	fmt.Fprintf(&b, "test Exits {\n\tscope.OnExit() {\n\t\tlet ys = []int{1, 2}\n\t\tlet _ = ys[%d]\n\t}\n\tlet v = try check(%d)\n\tassert v > 0\n}\n\n", r.IntN(3), r.IntN(7))
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(scale(2))\")\n}\n")
	return b.String()
}
