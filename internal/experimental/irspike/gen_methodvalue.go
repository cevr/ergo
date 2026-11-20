package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// A method value and its eta twin, each taken before a write to the
// receiver (CV1; types.md, Methods 2 and Function values 2 and 3). The
// method value x.f binds x's value where it is evaluated; its twin
// func(k) { x.f(k) }, a non-escaping literal, reads x when it runs, so it
// sees a later write. GenerateMethodValue writes cases of each write a
// program can make after the take, with the result each must give (the
// model), as a main that prints them and a test that asserts the model's
// line.

// mvWrite is a write to the receiver after the take.
type mvWrite int

const (
	mvReassign mvWrite = iota // a = Acc{N: n + d}
	mvBorrow                  // p := &h.A; …; p.N = n + d
	mvLater                   // applyAfter(f, bumpBy(&a, d), m): a later argument writes it
	mvKept                    // keep(a.Add), then a = …: an escaping method value (no eta twin: S108)
	mvWrites
)

// mvForm is how the value is taken: the method value or its eta twin.
type mvForm int

const (
	mvValue mvForm = iota
	mvEta
)

// mvCase is one generated function: its write, its form, and n, d and m.
type mvCase struct {
	write   mvWrite
	form    mvForm
	n, d, m int
	known   bool // passed to apply, a known-callback variant (the plain closure), not bound by a let
}

// want is the model: the method value reads n, the eta twin n + d.
func (c mvCase) want() int {
	if c.form == mvValue {
		return c.n + c.m
	}
	return c.n + c.d + c.m
}

// taken is the value as written at the take, over the receiver x.
func (c mvCase) taken(x string) string {
	if c.form == mvValue {
		return x + ".Add"
	}
	return "func(k int) int { " + x + ".Add(k) }"
}

// body is the case's function body over its parameters n, d and m: the
// caller passes them, so no build-time fold computes the case (line).
func (c mvCase) body() string {
	var b strings.Builder
	call := func(f string) string {
		if c.known {
			return fmt.Sprintf("apply(%s, m)", f)
		}
		return fmt.Sprintf("%s(m)", f)
	}
	switch c.write {
	case mvReassign:
		fmt.Fprintf(&b, "\tlet mut a = Acc{N: n}\n\tlet f = %s\n\ta = Acc{N: n + d}\n\t%s\n", c.taken("a"), call("f"))
	case mvBorrow:
		fmt.Fprintf(&b, "\tlet mut h = Holder{A: Acc{N: n}}\n\tlet p = &h.A\n\tlet f = %s\n\tp.N = n + d\n\t%s\n", c.taken("p"), call("f"))
	case mvLater:
		fmt.Fprintf(&b, "\tlet mut a = Acc{N: n}\n\tapplyAfter(%s, bumpBy(&a, d), m)\n", c.taken("a"))
	case mvKept:
		fmt.Fprintf(&b, "\tlet mut a = Acc{N: n}\n\tlet f = keep(%s)\n\ta = Acc{N: n + d}\n\t%s\n", c.taken("a"), call("f"))
	}
	return b.String()
}

// GenerateMethodValue is a program of method values and eta twins taken
// before each kind of write, and the model's line: main prints the cases'
// results, and the test MethodValuesAgree asserts the model's line, so
// the evaluator checks it too.
func GenerateMethodValue(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
	var cases []mvCase
	for w := mvWrite(0); w < mvWrites; w++ {
		for f := mvValue; f <= mvEta; f++ {
			if w == mvKept && f == mvEta {
				continue // an escaping literal that sees a later write is refused (S108)
			}
			c := mvCase{write: w, form: f, n: r.IntN(50) - 10, d: 1 + r.IntN(20), m: r.IntN(30) - 5}
			c.known = w != mvLater && r.IntN(2) == 0
			cases = append(cases, c)
		}
	}
	r.Shuffle(len(cases), func(i, j int) { cases[i], cases[j] = cases[j], cases[i] })
	var b strings.Builder
	b.WriteString("type Acc { N int }\n\nfunc Add(a Acc, b int) int { a.N + b }\n\ntype Holder { A Acc }\n\n")
	b.WriteString("func apply(f func(b int) int, x int) int { f(x) }\n\n")
	b.WriteString("func applyAfter(f func(b int) int, k int, x int) int { f(x) + k }\n\n")
	b.WriteString("func keep(f own func(b int) int) func(b int) int { f }\n\n")
	b.WriteString("func bumpBy(a mut Acc, d int) int {\n\ta.N = a.N + d\n\t0\n}\n\n")
	var parts, want []string
	for i, c := range cases {
		fmt.Fprintf(&b, "func c%d(n int, d int, m int) int {\n%s}\n\n", i, c.body())
		parts = append(parts, fmt.Sprintf("\\(c%d(%d + z, %d + z, %d + z))", i, c.n, c.d, c.m))
		want = append(want, fmt.Sprint(c.want()))
	}
	line := strings.Join(want, " ")
	// line's z is zero; main passes it as a mutable local, which the IR's
	// build-time fold does not read (ir_constfold.go: a call that reads a
	// local stays), so each case runs its IR body. With constants the fold
	// computed every case in the evaluator, and no IR body ran (CV1
	// fork-answer.md: cv-mv-ir-late-binding then looked equivalent).
	fmt.Fprintf(&b, "func line(z int) string { \"%s\" }\n\n", strings.Join(parts, " "))
	fmt.Fprintf(&b, "test MethodValuesAgree {\n\tassert line(0) == \"%s\"\n}\n\n", line)
	b.WriteString("func main() needs Console {\n\tlet mut z = 1\n\tz = z - 1\n\tConsole.Println(line(z))\n}\n")
	return b.String(), line + "\n"
}
