package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// lawBodies is the number of law bodies GenerateLaws writes: each gets an
// IR body in every seed (G6). Pair's two laws count once per provider's
// instance, a copy each (mono.go lawMonoFor).
const lawBodies = 12

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
	b.WriteString("type Bad { N int }\n\n")
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
	// A generic operation type's member laws (G6: law of a generic
	// operation type), run by two providers of different instances:
	// Ints (Pair[int]) clamps, so Idem holds; Strs (Pair[string]) appends,
	// so Idem is refuted with its site. Each runs a copy of each law at
	// its instance (mono.go lawMonoFor).
	b.WriteString("type Pair[T comparable] {\n\tPut(v T) T,\n\tTake(v T) T fails Bad,\n\tSame(a T, c T) bool,\n\n\tlaw Idem(v T) {\n\t\tlet w = Put(v)\n\t\tSame(Put(w), w)\n\t},\n\n\tlaw Taken(v T) {\n\t\tlet w = Take(v) catch {\n\t\tcase Bad:\n\t\t\treturn true\n\t\t}\n\t\tSame(w, v)\n\t},\n}\n\n")
	fmt.Fprintf(&b, "type Ints {} implements Pair[int]\n\nfunc Put(_ Ints, v int) int {\n\tif v > %d {\n\t\treturn %d\n\t}\n\tv\n}\n\n", 3+r.IntN(5), 3+r.IntN(5))
	fmt.Fprintf(&b, "func Take(_ Ints, v int) int fails Bad {\n\tif v %% %d == 0 {\n\t\treturn Bad{N: v}\n\t}\n\tv\n}\n\n", 2+r.IntN(4))
	b.WriteString("func Same(_ Ints, a int, c int) bool {\n\ta == c\n}\n\n")
	b.WriteString("type Strs {} implements Pair[string]\n\nfunc Put(_ Strs, v string) string {\n\tv + \"!\"\n}\n\n")
	fmt.Fprintf(&b, "func Take(_ Strs, v string) string fails Bad {\n\tif len(v) %% %d == 0 {\n\t\treturn Bad{N: len(v)}\n\t}\n\tv\n}\n\n", 2+r.IntN(3))
	b.WriteString("func Same(_ Strs, a string, c string) bool {\n\ta == c\n}\n\n")
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(scale(2))\")\n}\n")
	return b.String()
}
