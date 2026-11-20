package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// seqFuncs are GenerateSeq's functions with a Seq loop: each gets an IR
// body in every seed when the IR lowers Seq loops (PORT-FORIN).
var seqFuncs = []string{"total", "firstOver", "failing", "nested", "words", "traced"}

// GenerateSeq writes the Seq-loop program of seed (PORT-FORIN): for-in
// loops over Seq values (an upTo producer and a Seq[string] of words) whose
// bodies break, continue, return, fail and print (a polling frame), nested
// Seq loops, and in odd seeds a Seq that ignores yield's false under a
// loop that breaks: Go's range-over-func panics there, on the text path
// and the IR alike. main prints each function's result for a few bounds.
func GenerateSeq(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x2f6c1d9a7b3e5541))
	n := func(k int) int { return r.IntN(k) }
	var b strings.Builder
	b.WriteString("type E1 { Code int }\n\n")
	b.WriteString("func upTo(n int) Seq[int] {\n\tSeq[int](func(yield func(v int) bool) {\n\t\tfor i in 0..n {\n\t\t\tif !yield(i) {\n\t\t\t\treturn\n\t\t\t}\n\t\t}\n\t})\n}\n\n")
	b.WriteString("func stubborn(n int) Seq[int] {\n\tSeq[int](func(yield func(v int) bool) {\n\t\tfor i in 0..n {\n\t\t\tlet _ = yield(i)\n\t\t}\n\t})\n}\n\n")
	fmt.Fprintf(&b, "func wordsOf(xs []string) Seq[string] {\n\tSeq[string](func(yield func(v string) bool) {\n\t\tfor x in xs {\n\t\t\tif !yield(x + \"%s\") {\n\t\t\t\treturn\n\t\t\t}\n\t\t}\n\t})\n}\n\n", []string{"", "!", "-"}[n(3)])
	fmt.Fprintf(&b, "func total(n int) int {\n\tlet mut t = 0\n\tfor v in upTo(n) {\n\t\tif v > %d {\n\t\t\tbreak\n\t\t}\n\t\tif v %% %d == 0 {\n\t\t\tcontinue\n\t\t}\n\t\tt = t + v * %d\n\t}\n\tt\n}\n\n", 3+n(8), 2+n(3), 1+n(4))
	fmt.Fprintf(&b, "func firstOver(n int) int {\n\tfor v in upTo(n) {\n\t\tif v * v > %d {\n\t\t\treturn v\n\t\t}\n\t}\n\t-1\n}\n\n", 5+n(40))
	fmt.Fprintf(&b, "func failing(n int) int fails E1 {\n\tlet mut t = 0\n\tfor v in upTo(n) {\n\t\tif v == %d {\n\t\t\treturn E1{Code: v + t}\n\t\t}\n\t\tt = t + v\n\t}\n\tt\n}\n\n", 2+n(6))
	fmt.Fprintf(&b, "func nested(n int) int {\n\tlet mut t = 0\n\tfor a in upTo(n) {\n\t\tfor c in upTo(a) {\n\t\t\tif c == %d {\n\t\t\t\tbreak\n\t\t\t}\n\t\t\tt = t + a * c\n\t\t}\n\t\tif t > %d {\n\t\t\tbreak\n\t\t}\n\t}\n\tt\n}\n\n", 1+n(3), 20+n(200))
	fmt.Fprintf(&b, "func words(xs []string) string {\n\tlet mut out = \"\"\n\tfor w in wordsOf(xs) {\n\t\tif w == \"%s\" {\n\t\t\tcontinue\n\t\t}\n\t\tout = out + w + \",\"\n\t}\n\tout\n}\n\n", []string{"b", "c", "zz"}[n(3)])
	fmt.Fprintf(&b, "func traced(n int) int needs Console {\n\tlet mut k = 0\n\tfor v in upTo(n) {\n\t\tConsole.Println(\"step \\(v)\")\n\t\tk = k + v\n\t\tif k > %d {\n\t\t\treturn k\n\t\t}\n\t}\n\tk\n}\n\n", 4+n(20))
	b.WriteString("func main() needs Console {\n")
	for i := 0; i < 4; i++ {
		m := n(12)
		fmt.Fprintf(&b, "\tlet f%d = failing(%d) catch {\n\tcase E1 as e:\n\t\t-e.Code\n\t}\n", i, m)
		fmt.Fprintf(&b, "\tConsole.Println(\"n %d: \\(total(%d)) \\(firstOver(%d)) \\(f%d) \\(nested(%d)) \\(traced(%d))\")\n", m, m, m, i, m, m)
	}
	fmt.Fprintf(&b, "\tConsole.Println(words([]string{\"a\", \"b\", \"c\", \"%s\"}))\n", []string{"d", "zz", "b"}[n(3)])
	if seed%2 == 1 {
		// A Seq that calls yield after it returned false: the loop's break
		// makes Go's range-over-func panic, on both paths.
		fmt.Fprintf(&b, "\tlet mut s = 0\n\tfor v in stubborn(%d) {\n\t\tif v == 1 {\n\t\t\tbreak\n\t\t}\n\t\ts = s + v\n\t}\n\tConsole.Println(\"stubborn \\(s)\")\n", 3+n(4))
	}
	b.WriteString("}\n")
	return b.String()
}
