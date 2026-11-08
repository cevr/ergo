package lower

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Locals named like package types (L5; types.md, Scope 1; decision O16):
// a let, for, parameter or catch binder may take a package type's name, as
// in Go. In its scope the name is the local in value and pattern position,
// and the type in type position.

// TestErgoRunLocalShadowRepros are the three examples-wave repros (f7, f7b,
// f7c), which were S108.
func TestErgoRunLocalShadowRepros(t *testing.T) {
	wantRun(t, `type point { x int }

func f() int {
	let point = point{x: 1}
	point.x
}

func main() needs Console {
	Console.Println("\(f())")
}
`, "1\n")
	// The examples-wave note expected 1; "a\nb\n" has two non-empty lines,
	// as Go's strings.Split counts them.
	wantRun(t, `import strings

type line { text string }

func count(text string) int {
	let mut n = 0
	for line in strings.Split(text, "\n") {
		if line != "" {
			n = n + 1
		}
	}
	n
}

func main() needs Console {
	Console.Println("\(count("a\nb\n"))")
}
`, "2\n")
	wantRun(t, `type point { x int }

func f() int {
	let point = point{x: 7}
	let q = point
	q.x
}

func g() bool {
	let point = point{x: 7}
	point == point{x: 7}
}

func main() needs Console {
	Console.Println("\(f()) \(g())")
}
`, "7 true\n")
}

// shadowDecls are the package types a local takes the name of: a record
// (rec), a union variant (va), a defined type (num), and the error type a
// catch binds (oops). Each has val, a function on the type.
const shadowDecls = `type rec { n int }
type alt = va{ n int } | vb{}
type num int
type oops { n int }

func val(x rec) int { x.n }
func val(x va) int { x.n }
func val(x num) int { int(x) }
func val(x oops) int { x.n }

func fail(n int) int fails oops {
	return oops{n: n}
}

`

// shadowType is a type a local is named after.
type shadowType struct {
	name string
	mk   func(n string) string // a value of the type from an int expression
	one  string                // a value of the type, written where the type is known
}

var shadowTypes = []shadowType{
	{"rec", func(n string) string { return "rec{n: " + n + "}" }, "rec{n: 1}"},
	{"va", func(n string) string { return "va{n: " + n + "}" }, "va{n: 1}"},
	{"num", func(n string) string { return "num(" + n + ")" }, "1"},
}

var (
	shadowBinders = []string{"let", "for", "param", "catch"}
	shadowUses    = []string{"value", "field", "method", "pattern", "type"}
	shadowWraps   = []string{"plain", "literal", "block"}
)

// shadowUse writes a use of the local $ of type v (rec, va, num or oops)
// whose value is the local's n, or n + 1 for the type position use, which
// also writes the type named L. The lines end with the value.
func shadowUse(use, v string, l shadowType) []string {
	switch use {
	case "value":
		return []string{"let q = $", "q.val()"}
	case "field":
		if v == "num" {
			return []string{"int($)"}
		}
		return []string{"$.n"}
	case "method":
		return []string{"$.val()"}
	case "pattern":
		if v == "num" {
			return []string{"match $ {", "case k:", "\tint(k)", "}"}
		}
		return []string{"match $ {", "case " + v + "{n: k}:", "\tk", "}"}
	case "type":
		return []string{"let w " + l.name + " = " + l.one, "w.val() + $.val()"}
	}
	panic(use)
}

// shadowWrap puts the use's lines in a function literal or a block.
func shadowWrap(wrap string, lines []string) []string {
	ind := func(ls []string) []string {
		out := make([]string, len(ls))
		for i, s := range ls {
			out[i] = "\t" + s
		}
		return out
	}
	switch wrap {
	case "literal":
		return append(append([]string{"let g = func() int {"}, ind(lines)...), "}", "g()")
	case "block":
		return append(append([]string{"if n >= 0 {"}, ind(lines)...), "} else {", "\t0", "}")
	}
	return lines
}

// genShadowProgram writes one function per binder × type × use, each with
// a wrap the seed rotates, and a main that prints each function's value
// for an n r picks. § stands for the local's name, and a "// local T"
// comment before each function names the type; want holds the values main
// prints. The property fills § with the type's name and with a fresh name
// (the oracle).
func genShadowProgram(r *rand.Rand, seed uint64) (src string, want []int) {
	var b, calls strings.Builder
	b.WriteString(shadowDecls)
	k := 0
	for _, binder := range shadowBinders {
		for _, l := range shadowTypes {
			for _, use := range shadowUses {
				n := 1 + r.IntN(50)
				// Over three seeds every function takes every wrap.
				wrap := shadowWraps[(k+int(seed))%len(shadowWraps)]
				v := l.name
				if binder == "catch" {
					v = "oops"
				}
				lines := shadowWrap(wrap, shadowUse(use, v, l))
				// The last expression's first line: a for body returns it.
				last := 0
				for i, s := range lines {
					if !strings.HasPrefix(s, "\t") && !strings.HasPrefix(s, "}") && !strings.HasPrefix(s, "case ") {
						last = i
					}
				}
				ind := func(depth int) {
					tabs := strings.Repeat("\t", depth)
					for i, s := range lines {
						if i == last && binder == "for" {
							s = "return " + s
						}
						b.WriteString(tabs + strings.ReplaceAll(s, "$", "§") + "\n")
					}
				}
				fmt.Fprintf(&b, "// local %s\n", l.name)
				switch binder {
				case "let":
					fmt.Fprintf(&b, "func f%d(n int) int {\n\tlet § = %s\n", k, l.mk("n"))
					ind(1)
					b.WriteString("}\n\n")
				case "for":
					fmt.Fprintf(&b, "func f%d(n int) int {\n\tfor § in []%s{%s} {\n", k, l.name, l.mk("n"))
					ind(2)
					b.WriteString("\t}\n\t0\n}\n\n")
				case "param":
					fmt.Fprintf(&b, "func h%d(§ %s, n int) int {\n", k, l.name)
					ind(1)
					fmt.Fprintf(&b, "}\n\nfunc f%d(n int) int {\n\th%d(%s, n)\n}\n\n", k, k, l.mk("n"))
				case "catch":
					fmt.Fprintf(&b, "func f%d(n int) int {\n\tfail(n) catch {\n\tcase oops as §:\n", k)
					ind(2)
					b.WriteString("\t}\n}\n\n")
				}
				fmt.Fprintf(&calls, "\tConsole.Println(\"\\(f%d(%d))\")\n", k, n)
				if use == "type" {
					n++
				}
				want = append(want, n)
				k++
			}
		}
	}
	b.WriteString("func main() needs Console {\n")
	b.WriteString(calls.String())
	b.WriteString("}\n")
	return b.String(), want
}

// shadowNamed fills the local's placeholder: each function names its local
// after the type its "// local" comment names; fresh names it zz instead
// (the oracle).
func shadowNamed(src string, fresh bool) string {
	if fresh {
		return strings.ReplaceAll(src, "§", "zz")
	}
	var out strings.Builder
	for _, fn := range strings.SplitAfter(src, "\n}\n") {
		name := ""
		if i := strings.Index(fn, "// local "); i >= 0 {
			rest := fn[i+len("// local "):]
			name = rest[:strings.IndexByte(rest, '\n')]
		}
		out.WriteString(strings.ReplaceAll(fn, "§", name))
	}
	return out.String()
}

// TestErgoRunLocalShadowProperty (L5 acceptance): for every binder kind
// (let, for, parameter, catch binding) × every type kind a local is named
// after (record, union variant, defined type) × every use (a value read, a
// field, a method call, a pattern over it, and the type written in a type
// position beside it), in a plain body, a function literal or a block,
// the program with the local named like the type checks clean and prints
// what the program with the local renamed prints (the oracle) and what
// the model computes. Over the three seeds every function takes every
// wrap; the seed picks the values, and a failing seed prints the source.
func TestErgoRunLocalShadowProperty(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x4c35))
		gen, want := genShadowProgram(r, seed)
		shadow, oracle := shadowNamed(gen, false), shadowNamed(gen, true)
		if strings.Contains(shadow, "§") {
			t.Fatalf("seed %d: a function's local has no name\n%s", seed, shadow)
		}
		for _, p := range []struct{ name, src string }{{"shadowing", shadow}, {"renamed", oracle}} {
			if _, ds := check.CheckSource(p.src); len(ds) > 0 {
				t.Fatalf("seed %d: the %s program: %v\n%s", seed, p.name, ds, p.src)
			}
		}
		var model strings.Builder
		for _, n := range want {
			fmt.Fprintf(&model, "%d\n", n)
		}
		a, errA := ergoRun(t, shadow)
		b, errB := ergoRun(t, oracle)
		if errA != nil || errB != nil || a != b || a != model.String() {
			t.Fatalf("seed %d: shadowing (%v) and renamed (%v) programs differ or miss the model\n--- shadowing output\n%s\n--- renamed output\n%s\n--- model\n%s\n--- shadowing source\n%s", seed, errA, errB, a, b, model.String(), shadow)
		}
	}
}

// TestLocalShadowVerdicts: a use that is wrong for the local is wrong in
// the same way for the renamed local: the local named like a type in a
// match pattern binds a new name, which D-D2 refuses as it refuses the
// renamed one; a conversion through the name calls the local. Each pair
// has the same diagnostic codes and lines.
func TestLocalShadowVerdicts(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"pattern-name", "\tlet § = rec{n: n}\n\tmatch n {\n\tcase §:\n\t\t1\n\t}\n"},
		{"for-pattern-name", "\tfor § in []rec{rec{n: n}} {\n\t\tlet (§, m) = (1, 2)\n\t\treturn m\n\t}\n\t0\n"},
		{"call", "\tlet § = num(n)\n\tint(§(2))\n"},
		{"unread", "\tlet § = rec{n: n}\n\t0\n"},
	} {
		name := "rec"
		if strings.Contains(c.body, "num(") {
			name = "num"
		}
		src := shadowDecls + "func f(n int) int {\n" + c.body + "}\n"
		a, b := verdictLines(strings.ReplaceAll(src, "§", name)), verdictLines(strings.ReplaceAll(src, "§", "zz"))
		if len(a) == 0 || !slices.Equal(a, b) {
			t.Errorf("%s: named like the type %v, renamed %v\n%s", c.name, a, b, src)
		}
	}
}

func verdictLines(src string) []string {
	_, ds := check.CheckSource(src)
	var out []string
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s@%d", d.Code, d.Line))
	}
	slices.Sort(out)
	return out
}
