package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// lawBodies is the number of laws GenerateLaws writes: each gets an IR
// body in every seed (G6).
const lawBodies = 9

// GenerateLaws is a program of laws (G6): a law whose tail is a
// comparison, one that returns early from an if, one whose tail is an if
// expression, one whose tail is a match, one that returns from a catch,
// and one whose tail is tried. The seed decides which hold, so a run
// reports held laws and refuted ones with their sites.
func GenerateLaws(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	k := 2 + r.IntN(5)
	cmp := func(hold, refute string) string {
		if r.IntN(3) == 0 {
			return refute
		}
		return hold
	}
	b.WriteString("import scope\n\ntype Bad { N int }\n\n")
	b.WriteString("type Shape = Dot | Box\n\ntype Dot {}\n\ntype Box { W int }\n\n")
	fmt.Fprintf(&b, "func scale(n int) int {\n\tn * %d\n}\n\n", k)
	b.WriteString("func shapeOf(n int) Shape {\n\tif n % 2 == 0 {\n\t\treturn Dot\n\t}\n\tBox{W: n}\n}\n\n")
	b.WriteString("func check(n int) int fails Bad {\n\tif n % 7 == 3 {\n\t\treturn Bad{N: n}\n\t}\n\tn\n}\n\n")
	// An operation type whose member laws call its operations bare
	// (CallLawOp), run against its provider Mem, which claims it (a law
	// of an operation type with no provider does not run): the seed
	// decides whether the provider keeps them.
	fmt.Fprintf(&b, "type Store {\n\tGet(n int) int,\n\tFind(n int) int fails Bad,\n\n\tlaw Kept(n int) {\n\t\tlet a = Get(n)\n\t\tlet c = Get(a)\n\t\tc %s a + %d\n\t},\n\n\tlaw Found(n int) {\n\t\tlet v = Find(n) catch {\n\t\tcase Bad as e:\n\t\t\treturn e.N == n\n\t\t}\n\t\tlet g = Get(n)\n\t\tv %s g\n\t},\n}\n\n", cmp(">=", "<"), r.IntN(2), cmp("==", "!="))
	b.WriteString("type Mem {} implements Store\n\n")
	fmt.Fprintf(&b, "func Get(_ Mem, n int) int {\n\tn + %d\n}\n\n", 1+r.IntN(3))
	fmt.Fprintf(&b, "func Find(_ Mem, n int) int fails Bad {\n\tif n %% %d == 0 {\n\t\treturn Bad{N: n}\n\t}\n\tGet(Mem, n)\n}\n\n", 2+r.IntN(4))
	fmt.Fprintf(&b, "law Scales(n int) {\n\tscale(n) %s n * %d\n}\n\n", cmp("==", "!="), k)
	fmt.Fprintf(&b, "law Early(n int) {\n\tif n < 0 {\n\t\treturn %s\n\t}\n\tscale(n) >= n\n}\n\n", cmp("true", "n > 0"))
	fmt.Fprintf(&b, "law Branches(n int) {\n\tif n %% 2 == 0 {\n\t\tscale(n) %% 2 == 0\n\t} else {\n\t\tscale(n) %s n\n\t}\n}\n\n", cmp("!= 1 +", "=="))
	fmt.Fprintf(&b, "law Shapes(n int) {\n\tmatch shapeOf(n) {\n\tcase Dot:\n\t\tn %% 2 == 0\n\tcase Box as x:\n\t\tx.W %s n\n\t}\n}\n\n", cmp("==", "<"))
	fmt.Fprintf(&b, "law Caught(n int) {\n\tlet v = check(n) catch {\n\tcase Bad:\n\t\treturn %s\n\t}\n\tv == n\n}\n\n", cmp("n % 7 == 3", "n < 0"))
	fmt.Fprintf(&b, "law Tried(n int) {\n\tlet v = try check(n %% %d)\n\tv >= 0\n}\n\n", 3+r.IntN(5))
	// A law that opens a frame scope (GP-FRAMES): its exit action runs at
	// each case's end and, at a seeded modulus, raises a defect for some n.
	fmt.Fprintf(&b, "law Exits(n int) {\n\tscope.OnExit() {\n\t\tlet ys = []int{1, 2, 3}\n\t\tlet _ = ys[(n %% %[1]d + %[1]d) %% %[1]d]\n\t}\n\tscale(n) %% %[2]d == 0\n}\n\n", 3+r.IntN(2), k)
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(scale(2))\")\n}\n")
	return b.String()
}
