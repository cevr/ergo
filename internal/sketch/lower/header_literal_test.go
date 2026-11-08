package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// lw-header-literal (G-ex-c-2): a composite literal in an if, for or
// match header lowers to Go that parses, and gives the value the same
// expression gives when its literal is bound by a let first.

// TestErgoRunHeaderLiteral is the examples-wave repro c2: a generic
// variant literal in parentheses on the right of != in an if header. The
// checker accepted it; go/format dropped the parentheses, so go build
// failed with "unexpected { at end of statement".
func TestErgoRunHeaderLiteral(t *testing.T) {
	wantRun(t, `func differs(m Map[string, string], k string, v string) bool {
	if m.Get(k) != (Some[string]{Value: v}) {
		return true
	}
	false
}

func main() needs Console {
	let mut m Map[string, string]
	m["a"] = "1"
	Console.Println("\(differs(m, "a", "1")) \(differs(m, "a", "2"))")
}
`, "false true\n")
	// Repro c2e (stackvm): the same header in an if in value position, the
	// value of a let.
	wantRun(t, `func label(m Map[int, bool], n int) string {
	let l = if m.Get(n) == (Some[bool]{Value: true}) { "L" } else { "-" }
	l
}

func main() needs Console {
	let mut m Map[int, bool]
	m[1] = true
	Console.Println("\(label(m, 1))\(label(m, 2))")
}
`, "L-\n")
}

var headerSeed = flag.Uint64("header.seed", 0, "run TestHeaderLiteralAgrees on this seed alone")

// headerSeeds are the fixed seeds; each writes one program of every case.
var headerSeeds = []uint64{1, 2, 3}

// headerLit is a literal kind: the literal's Ergo text (A is the generated
// constant), whether a header needs it in parentheses (a TypeName form),
// and the boolean condition over it, the literal on the left or right.
type headerLit struct {
	name  string
	lit   string
	paren bool
	cond  func(lit string, left bool) string
}

func sides(op string, other string) func(string, bool) string {
	return func(lit string, left bool) string {
		if left {
			return lit + " " + op + " " + other
		}
		return other + " " + op + " " + lit
	}
}

var headerLits = []headerLit{
	{"record", "P{X: A}", true, sides("==", "p")},
	{"variant", "Some[int]{Value: A}", true, sides("!=", "o")},
	{"generic", "G[int]{V: A}", true, sides("==", "q")},
	{"slice", "[]int{A, 9}", false, func(lit string, left bool) string { return sides("==", "p.X")(lit+"[0]", left) }},
	{"map", `Map[string, int]{"k": A}`, false, func(lit string, left bool) string { return sides("==", "p.X")("len("+lit+")", left) }},
	{"call", "G[int]{V: A}", false, func(lit string, left bool) string { return sides("==", "p.X")("g("+lit+")", left) }},
	{"binary", "G[int]{V: A}", true, func(lit string, left bool) string {
		if left {
			return lit + ".V + p.X == 4"
		}
		return "4 == p.X + " + lit + ".V"
	}},
}

// headerKinds place a condition in a statement header; each yields 1 when
// the condition holds and 0 when it does not.
var headerKinds = []struct {
	name string
	body func(cond string) string
}{
	{"if", func(c string) string { return "\tif " + c + " { 1 } else { 0 }\n" }},
	{"else-if", func(c string) string { return "\tif p.X == 1000 { 2 } else if " + c + " { 1 } else { 0 }\n" }},
	// The condition is the whole header: a nested binary would give the
	// literal parentheses of its own (binary writes them).
	{"for", func(c string) string {
		return "\tfor " + c + " {\n\t\treturn 1\n\t}\n\t0\n"
	}},
	{"match", func(c string) string {
		return "\tmatch " + c + " {\n\tcase true:\n\t\t1\n\tcase false:\n\t\t0\n\t}\n"
	}},
}

type headerCase struct {
	id       string
	fn, ref  string // the header function and its let-first reference
	call     string // the arguments main passes
	describe string
}

// headerCases writes every header kind × literal kind × side, and a match
// whose subject is the variant literal itself (a Go type switch), with the
// constants and arguments drawn from seed.
func headerCases(seed uint64) []headerCase {
	r := rand.New(rand.NewPCG(seed, 0x6864))
	n := func() string { return fmt.Sprint(r.IntN(4)) }
	args := func() string {
		o := "None"
		if r.IntN(3) > 0 {
			o = "Some[int]{Value: " + n() + "}"
		}
		return "P{X: " + n() + "}, " + o + ", G[int]{V: " + n() + "}"
	}
	const sig = "(p P, o Option[int], q G[int]) int {\n"
	var out []headerCase
	for _, k := range headerKinds {
		for _, l := range headerLits {
			for _, left := range []bool{true, false} {
				id := fmt.Sprintf("s%dc%d", seed, len(out))
				lit := strings.ReplaceAll(l.lit, "A", n())
				inHeader := lit
				if l.paren {
					inHeader = "(" + lit + ")"
				}
				out = append(out, headerCase{
					id:       id,
					fn:       "func " + id + sig + k.body(l.cond(inHeader, left)) + "}\n",
					ref:      "func r" + id + sig + "\tlet lit = " + lit + "\n\tlet b = " + l.cond("lit", left) + "\n" + k.body("b") + "}\n",
					call:     args(),
					describe: fmt.Sprintf("%s header, %s literal, left %v", k.name, l.name, left),
				})
			}
		}
	}
	for range 2 {
		id := fmt.Sprintf("s%dc%d", seed, len(out))
		lit := "Some[int]{Value: " + n() + "}"
		arms := " {\n\tcase Some[int] as s:\n\t\tif s.Value == p.X { 1 } else { 0 }\n\t}\n"
		out = append(out, headerCase{
			id:       id,
			fn:       "func " + id + sig + "\tmatch (" + lit + ")" + arms + "}\n",
			ref:      "func r" + id + sig + "\tlet lit = " + lit + "\n\tmatch lit" + arms + "}\n",
			call:     args(),
			describe: "match subject, variant literal",
		})
	}
	return out
}

// TestHeaderLiteralAgrees is the generated property of lw-header-literal:
// every case checks, lowers to Go that parses and builds, and its header
// function prints what its let-first reference prints. A failing line
// names the seed, the Ergo source of both functions and both outputs.
// Replay: go test -run '^TestHeaderLiteralAgrees$' ./internal/sketch/lower -args -header.seed=N
func TestHeaderLiteralAgrees(t *testing.T) {
	seeds := headerSeeds
	if *headerSeed != 0 {
		seeds = []uint64{*headerSeed}
	}
	const decls = "type P { X int }\n\ntype G[T] { V T }\n\nfunc g(v G[int]) int { v.V }\n\n"
	var programs []*lowered
	cases := map[uint64][]headerCase{}
	for _, seed := range seeds {
		cs := headerCases(seed)
		cases[seed] = cs
		var src strings.Builder
		src.WriteString(decls)
		for _, c := range cs {
			src.WriteString(c.fn + "\n" + c.ref + "\n")
		}
		src.WriteString("func main() needs Console {\n")
		for _, c := range cs {
			fmt.Fprintf(&src, "\tConsole.Println(\"%s \\(%s(%s)) \\(r%s(%s))\")\n", c.id, c.id, c.call, c.id, c.call)
		}
		src.WriteString("}\n")
		programs = append(programs, mustLower(t, fmt.Sprintf("header%d", seed), src.String()))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the header programs do not build (seeds %v)", seeds)
	}
	total := 0
	for i, seed := range seeds {
		p := programs[i]
		stdout, stderr, code := runExe(t, exes[p.name])
		if code != 0 {
			t.Fatalf("seed %d: exit %d\n%s", seed, code, stderr)
		}
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(lines) != len(cases[seed]) {
			t.Fatalf("seed %d: %d lines for %d cases\n%s", seed, len(lines), len(cases[seed]), stdout)
		}
		for j, c := range cases[seed] {
			f := strings.Fields(lines[j])
			if len(f) != 3 || f[0] != c.id || f[1] != f[2] {
				t.Errorf("seed %d, %s (%s): line %q; want the header value equal to the reference's.\nReplay: go test -run '^TestHeaderLiteralAgrees$' ./internal/sketch/lower -args -header.seed=%d\n%s\n%sarguments: %s",
					seed, c.id, c.describe, lines[j], seed, c.fn, c.ref, c.call)
			}
			total++
		}
	}
	t.Logf("seeds %v: %d header cases agree", seeds, total)
}

// TestTypeNameLiteral pins the text test that decides which header values
// are parenthesized.
func TestTypeNameLiteral(t *testing.T) {
	for s, want := range map[string]bool{
		"P{X: int(1)}":                   true,
		"Some[string]{Value: v}":         true,
		"pkg.T[int, string]{A: `}`}":     true,
		`T{S: "}{"}`:                     true,
		"[]int{1}":                       false,
		"map[string]int{}":               false,
		"struct{}{}":                     false,
		"func() {}":                      false,
		"P{X: 1}.X":                      false,
		"f(P{X: 1})":                     false,
		"(P{X: 1})":                      false,
		"v_1":                            false,
		"P{X: 1} == P{X: 2}":             false,
		"G[int]{V: 1}.Method(G[int]{})":  false,
		"T{A: '}'}":                      true,
		"Tuple2[int, int]{F0: 1, F1: 2}": true,
	} {
		if got := typeNameLiteral(s); got != want {
			t.Errorf("typeNameLiteral(%q) = %v, want %v", s, got, want)
		}
	}
}
