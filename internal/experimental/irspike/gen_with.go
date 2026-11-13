package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// withFuncs are GenerateWith's functions: each gets an IR body in every
// seed (G6).
var withFuncs = []string{"show", "named", "inline", "nested", "multi", "local", "each", "adapted", "leak"}

// GenerateWith is a program of withs (G6): a named context, an inline and
// a local context, a with of two operands, nested withs, withs in value,
// statement and result position and inside a loop; providers that are
// records whose functions provide the capability (an operations
// conversion), one of them a seeded record literal; a with
// that leaves the outer environment as it was. main prints every
// function's result for seeded operands.
func GenerateWith(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x51afd7ed558ccd))
	var b strings.Builder
	base := r.IntN(50)
	word := func() string { return []string{"a", "bc", "def", "", "zz"}[r.IntN(5)] }
	b.WriteString("type Store {\n\tGet(k int) string,\n}\n\n")
	b.WriteString("type Mem { Base int }\n\nfunc Get(m Mem, k int) string {\n\t\"mem \\(m.Base + k)\"\n}\n\n")
	b.WriteString("type Fixed { Text string }\n\nfunc Get(f Fixed, k int) string {\n\t\"\\(f.Text)\\(k)\"\n}\n\n")
	fmt.Fprintf(&b, "let Live = context {\n\tStore: Mem{Base: %d},\n}\n\n", base)
	b.WriteString("func show(k int) string needs Store {\n\tStore.Get(k)\n}\n\n")
	// A with as the function's result.
	b.WriteString("func named(k int) string needs Console {\n\twith Live { show(k) }\n}\n\n")
	// A with in a value position, an inline context.
	fmt.Fprintf(&b, "func inline(t string, k int) string needs Console {\n\tlet got = with context { Store: Fixed{Text: t + %q} } {\n\t\tlet s = show(k)\n\t\ts + \"!\"\n\t}\n\tgot + \"?\"\n}\n\n", word())
	// Nested withs: the inner context overrides the outer's Store.
	fmt.Fprintf(&b, "func nested(t string, k int) string needs Console {\n\twith Live {\n\t\tlet a = show(k)\n\t\twith context { Store: Fixed{Text: t} } {\n\t\t\tlet s = show(k + %d)\n\t\t\ta + s\n\t\t}\n\t}\n}\n\n", 1+r.IntN(3))
	// Two operands: the later binds over the earlier.
	ops := []string{"Live", "context { Store: Fixed{Text: t} }"}
	if r.IntN(2) == 0 {
		ops[0], ops[1] = ops[1], ops[0]
	}
	fmt.Fprintf(&b, "func multi(t string, k int) string needs Console {\n\twith %s, %s { show(k) }\n}\n\n", ops[0], ops[1])
	// A local context, named by two withs.
	b.WriteString("func local(t string, k int) string needs Console {\n\tlet c = context { Store: Fixed{Text: t} }\n\tlet x = with c { show(k) }\n\twith c {\n\t\tlet s = show(k * 2)\n\t\tx + s\n\t}\n}\n\n")
	// Withs in statement position inside a loop, reading the outer
	// environment's Console.
	fmt.Fprintf(&b, "func each(n int) needs Console {\n\tlet mut i = 0\n\tfor i < n {\n\t\twith Live { Console.Println(show(i)) }\n\t\ti += %d\n\t}\n}\n\n", 1+r.IntN(2))
	// A with leaves the environment outside it as it was: the outer Store
	// after it is the caller's.
	b.WriteString("func leak(t string, k int) string needs Store | Console {\n\tlet a = with context { Store: Fixed{Text: t} } { show(k) }\n\tlet s = show(k)\n\ta + s\n}\n\n")
	// A provider value of a local, converted to the capability.
	b.WriteString("func adapted(k int, m Mem) string needs Console {\n\twith context { Store: m } { show(k) }\n}\n\n")
	b.WriteString("func main() needs Console {\n")
	for i := 0; i < 4; i++ {
		k := r.IntN(20) - 5
		t := word()
		fmt.Fprintf(&b, "\tConsole.Println(\"%d \\(named(%d)) \\(inline(%q, %d)) \\(nested(%q, %d)) \\(multi(%q, %d)) \\(local(%q, %d)) \\(adapted(%d, Mem{Base: %d})) \\(with Live { leak(%q, %d) })\")\n", i, k, t, k, t, k, t, k, t, k, k, r.IntN(9), t, k)
	}
	fmt.Fprintf(&b, "\teach(%d)\n", r.IntN(5))
	b.WriteString("}\n")
	return b.String()
}
