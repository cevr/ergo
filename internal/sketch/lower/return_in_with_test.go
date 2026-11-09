package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestErgoRunReturnInWith (L3 acceptance): the d-10 repro returns 3 for
// []int{-1, 3}, and its provider closes before the function's exit action.
func TestErgoRunReturnInWith(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import scope

type Store { Get() int }
type Mem { N int }
func Get(m Mem) int { m.N }
func Close(m Mem) needs Console { Console.Println("close \(m.N)") }

func first(xs []int) int needs Console {
	scope.OnExit() { Console.Println("exit") }
	with context { Store: Mem{N: 0} } {
		for x in xs {
			if x > 0 {
				return x + Store.Get()
			}
		}
		Store.Get()
	}
}

func main() needs Console {
	Console.Println("\(first([]int{-1, 3}))")
}
`, "close 0\nexit\n3\n")
}

// TestErgoRunReturnInWithSlots: a with that returns stands in operand
// slots (a call argument, a binary operand, a composite element, an
// interpolation, an if condition, a match subject); the operands before
// it are evaluated first, and its provider closes once on both exits.
func TestErgoRunReturnInWithSlots(t *testing.T) {
	needsBuild(t)
	slots := []string{
		"integer(WITH)",
		"1 + WITH",
		"[]int{tick(), WITH}[1]",
		"len(\"\\(WITH)\")",
		"if WITH > 0 { 1 } else { 2 }",
		"match WITH { case 0: 3; case _: 4 }",
	}
	var b strings.Builder
	b.WriteString(`type C { Use() int }
type R { N int }
func Use(r R) int { r.N }
func Close(r R) needs Console { Console.Println("close \(r.N)") }
func integer(n int) int { n }
func tick() int needs Console { Console.Println("tick"); 0 }
`)
	var want strings.Builder
	for i, slot := range slots {
		with := fmt.Sprintf("with context { C: R{N: %d} } { if k == 0 { return 100 + C.Use() }; C.Use() }", i)
		fmt.Fprintf(&b, "func s%d(k int) int needs Console {\n\t%s\n}\n", i, strings.ReplaceAll(slot, "WITH", with))
		for k := range 2 {
			if strings.Contains(slot, "tick") {
				want.WriteString("tick\n")
			}
			fmt.Fprintf(&want, "close %d\n", i)
			v := 100 + i
			if k == 1 {
				v = map[int]int{0: i, 1: 1 + i, 2: i, 3: len(fmt.Sprint(i)), 4: map[bool]int{true: 1, false: 2}[i > 0], 5: map[bool]int{true: 3, false: 4}[i == 0]}[i]
			}
			fmt.Fprintf(&want, "s%d(%d) = %d\n", i, k, v)
		}
	}
	b.WriteString("func main() needs Console {\n")
	for i := range slots {
		for k := range 2 {
			fmt.Fprintf(&b, "\tConsole.Println(\"s%d(%d) = \\(s%d(%d))\")\n", i, k, i, k)
		}
	}
	b.WriteString("}\n")
	wantRun(t, b.String(), want.String())
}

// rwReturn is a return point in a with block: the function returns when
// its argument k is id, by one of four forms.
type rwReturn struct {
	id, form int
	useCap   bool // the returned value reads the block's capability
}

// rwLevel is one with of a nest; level i binds the capability C<i>.
type rwLevel struct {
	second     bool // the with also binds D<i>, built after C<i>
	exitInside bool // the block registers an exit action
	before     []rwReturn
	after      []rwReturn // only when the next with is bound by let
	letInner   bool       // the next with is `let v = with …`, then v + 1
}

type rwFunc struct {
	name    string
	topExit bool
	levels  []rwLevel
	ids     int // returns use ids 0..ids-1; k = ids returns by the tail
}

func genRWFunc(r *rand.Rand, name string) *rwFunc {
	f := &rwFunc{name: name, topExit: r.IntN(2) == 0}
	depth := 1 + r.IntN(3)
	ret := func() rwReturn {
		rp := rwReturn{id: f.ids, form: r.IntN(4), useCap: r.IntN(2) == 0}
		f.ids++
		return rp
	}
	for i := range depth {
		l := rwLevel{second: r.IntN(3) == 0, exitInside: r.IntN(2) == 0}
		for range r.IntN(3) {
			l.before = append(l.before, ret())
		}
		if i+1 < depth {
			l.letInner = r.IntN(2) == 0
			if l.letInner {
				for range r.IntN(2) {
					l.after = append(l.after, ret())
				}
			}
		}
		f.levels = append(f.levels, l)
	}
	return f
}

// cover records the shapes f exercises.
func (f *rwFunc) cover(c map[string]bool) {
	c["exit top"] = c["exit top"] || f.topExit
	c["depth 3"] = c["depth 3"] || len(f.levels) == 3
	for i, l := range f.levels {
		c["second"] = c["second"] || l.second
		c["exit inside"] = c["exit inside"] || l.exitInside
		c["after"] = c["after"] || len(l.after) > 0
		c["let"] = c["let"] || l.letInner
		c["tail"] = c["tail"] || i+1 < len(f.levels) && !l.letInner
		for _, rp := range append(append([]rwReturn{}, l.before...), l.after...) {
			c[fmt.Sprintf("form %d", rp.form)] = true
			c["cap"] = c["cap"] || rp.useCap
		}
	}
}

func (f *rwFunc) n(i int) int { return 10*len(f.name) + i } // C<i>'s provider value
func (f *rwFunc) m(i int) int { return 500 + f.n(i) }       // D<i>'s provider value

// source writes the function.
func (f *rwFunc) source() string {
	var b strings.Builder
	w := func(depth int, format string, args ...any) {
		b.WriteString(strings.Repeat("\t", depth) + fmt.Sprintf(format, args...) + "\n")
	}
	w(0, "func %s(k int) int needs Console {", f.name)
	if f.topExit {
		w(1, "scope.OnExit() { Console.Println(\"%s exit top\") }", f.name)
	}
	ret := func(d, i int, rp rwReturn) {
		v := fmt.Sprintf("%d", 1000+rp.id)
		if rp.useCap {
			v += fmt.Sprintf(" + C%d.Use()", i)
		}
		switch rp.form {
		case 0:
			w(d, "if k == %d { return %s }", rp.id, v)
		case 1:
			w(d, "for j in 0..3 { if k == %d && j == 1 { return %s } }", rp.id, v)
		case 2:
			w(d, "let _ = risky(k - %d) catch { case Fault: return %s }", rp.id, v)
		case 3:
			w(d, "match k {")
			w(d, "case %d:", rp.id)
			w(d+1, "return %s", v)
			w(d, "case _:")
			w(d, "}")
		}
	}
	var level func(i, d int, prefix string)
	level = func(i, d int, prefix string) {
		l := f.levels[i]
		entries := fmt.Sprintf("C%d: R%d{N: %d}", i, i, f.n(i))
		if l.second {
			entries += fmt.Sprintf(", D%d: S%d{N: %d}", i, i, f.m(i))
		}
		w(d, "%swith context { %s } {", prefix, entries)
		w(d+1, "Console.Println(\"%s use %d \\(C%d.Use())\")", f.name, i, i)
		if l.second {
			w(d+1, "Console.Println(\"%s use d%d \\(D%d.Use())\")", f.name, i, i)
		}
		if l.exitInside {
			w(d+1, "scope.OnExit() { Console.Println(\"%s exit %d\") }", f.name, i)
		}
		for _, rp := range l.before {
			ret(d+1, i, rp)
		}
		switch {
		case i+1 == len(f.levels):
			w(d+1, "C%d.Use()", i)
		case l.letInner:
			level(i+1, d+1, fmt.Sprintf("let v%d = ", i))
			for _, rp := range l.after {
				ret(d+1, i, rp)
			}
			w(d+1, "v%d + 1", i)
		default:
			level(i+1, d+1, "")
		}
		w(d, "}")
	}
	level(0, 1, "")
	w(0, "}")
	return b.String()
}

// model is the reference: the lines the function prints for k and its
// value. A return computes its value in its block, then closes every open
// with, the innermost first and in each with its providers in reverse
// build order, then runs the exit actions registered so far, the last
// first. A block's normal end closes its own with.
func (f *rwFunc) model(k int) ([]string, int) {
	var out, exits []string
	closeLevel := func(i int) {
		if f.levels[i].second {
			out = append(out, fmt.Sprintf("close %d", f.m(i)))
		}
		out = append(out, fmt.Sprintf("close %d", f.n(i)))
	}
	finish := func() {
		for j := len(exits) - 1; j >= 0; j-- {
			out = append(out, exits[j])
		}
	}
	if f.topExit {
		exits = append(exits, f.name+" exit top")
	}
	returned := false
	var level func(i int) int
	level = func(i int) int {
		l := f.levels[i]
		out = append(out, fmt.Sprintf("%s use %d %d", f.name, i, f.n(i)))
		if l.second {
			out = append(out, fmt.Sprintf("%s use d%d %d", f.name, i, f.m(i)))
		}
		if l.exitInside {
			exits = append(exits, fmt.Sprintf("%s exit %d", f.name, i))
		}
		hit := func(rps []rwReturn) (int, bool) {
			for _, rp := range rps {
				if rp.id == k {
					v := 1000 + rp.id
					if rp.useCap {
						v += f.n(i)
					}
					for j := i; j >= 0; j-- {
						closeLevel(j)
					}
					returned = true
					return v, true
				}
			}
			return 0, false
		}
		if v, ok := hit(l.before); ok {
			return v
		}
		var v int
		switch {
		case i+1 == len(f.levels):
			v = f.n(i)
		default:
			v = level(i + 1)
			if returned {
				return v
			}
			if l.letInner {
				if rv, ok := hit(l.after); ok {
					return rv
				}
				v++
			}
		}
		closeLevel(i)
		return v
	}
	v := level(0)
	finish()
	return out, v
}

// TestErgoRunReturnInWithModel (L3 acceptance): generated nests of with,
// each owning a provider whose Close logs, with exit actions and returns
// at random depths, in an if, a loop, a catch arm and a match arm, before
// and after a nested with bound by let. For every argument the program's
// log and value equal the reference model: each provider is released
// exactly once, the innermost first, before the function's exit actions,
// which run last first. A failing seed prints the Ergo source.
func TestErgoRunReturnInWithModel(t *testing.T) {
	needsBuild(t)
	cover := map[string]bool{}
	for _, seed := range []uint64{1, 2, 3} {
		r := rand.New(rand.NewPCG(seed, 0x4c33))
		var b strings.Builder
		b.WriteString("import scope\n\ntype Fault {}\n\nfunc risky(n int) int fails Fault {\n\tif n == 0 {\n\t\treturn Fault\n\t}\n\tn\n}\n\n")
		for i := range 3 {
			fmt.Fprintf(&b, "type C%d { Use() int }\ntype R%d { N int }\nfunc Use(r R%d) int { r.N }\nfunc Close(r R%d) needs Console { Console.Println(\"close \\(r.N)\") }\n", i, i, i, i)
			fmt.Fprintf(&b, "type D%d { Use() int }\ntype S%d { N int }\nfunc Use(s S%d) int { s.N }\nfunc Close(s S%d) needs Console { Console.Println(\"close \\(s.N)\") }\n\n", i, i, i, i)
		}
		var fs []*rwFunc
		var want []string
		for i := range 8 {
			f := genRWFunc(r, "f"+strings.Repeat("x", i))
			fs = append(fs, f)
			f.cover(cover)
			b.WriteString(f.source() + "\n")
		}
		b.WriteString("func main() needs Console {\n")
		for _, f := range fs {
			for k := 0; k <= f.ids; k++ {
				fmt.Fprintf(&b, "\tConsole.Println(\"%s(%d) = \\(%s(%d))\")\n", f.name, k, f.name, k)
				lines, v := f.model(k)
				want = append(want, lines...)
				want = append(want, fmt.Sprintf("%s(%d) = %d", f.name, k, v))
			}
		}
		b.WriteString("}\n")
		src := b.String()
		out, err := ergoRun(t, src)
		if wantOut := strings.Join(want, "\n") + "\n"; err != nil || out != wantOut {
			t.Fatalf("seed %d: ergo run: %v\n--- source\n%s\n--- got\n%s\n--- want\n%s", seed, err, src, out, wantOut)
		}
	}
	for _, shape := range []string{"form 0", "form 1", "form 2", "form 3", "depth 3", "after", "let", "tail", "second", "exit inside", "exit top", "cap"} {
		if !cover[shape] {
			t.Errorf("no generated case has the shape %s", shape)
		}
	}
}
