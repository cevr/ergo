package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The fixed arrays' seeded property (196y; types.md, Arrays). Each seed
// writes a program over [3]int, an array of plain elements, and [2][]int,
// an array whose elements hold a slice, so the value rule copies it. The
// program binds, copies, writes and reads them through each path an array
// takes: a let and a let mut bind of a literal, a binding, a function's
// result and a default; an element write, a whole reassignment and a write
// of a slice element inside an element; an argument its callee copies and
// writes; a record field written through the record; a slice of an array,
// and of a literal, written after, and an array written after a
// slice of it is taken; a loop that writes the array it visits; == between
// arrays, Go's and generated; and Map writes into the elements of a
// default [2]Map[int, int]. The generator is the reference interpreter: it runs each
// statement on a Go model with value semantics (every bind copies) and
// writes the line the program must print.
//
// Replay: go test -run '^TestArrayValuesAgree$' ./internal/sketch/lower -args -array.seed=N

var arraySeed = flag.Uint64("array.seed", 0, "run TestArrayValuesAgree on this seed alone")

// akind is a generated array's type.
type akind int

const (
	aInts   akind = iota // [3]int
	aSlices              // [2][]int
)

var akindType = []string{"[3]int", "[2][]int"}
var akindDigest = []string{"da", "das"}

// aval is a model array: ints for [3]int, slices for [2][]int (each
// element a slice of two ints).
type aval struct {
	ints   [3]int
	slices [2][]int
}

func (v aval) copy() aval {
	out := v
	for i, s := range v.slices {
		out.slices[i] = append([]int(nil), s...)
	}
	return out
}

func (v aval) digest(k akind) int {
	if k == aInts {
		return v.ints[0]*100 + v.ints[1]*10 + v.ints[2]
	}
	d := 0
	for i, s := range v.slices {
		for j, x := range s {
			d = d*7 + x*(i*2+j+1)
		}
	}
	return d
}

type abind struct {
	name    string
	kind    akind
	val     aval
	written bool
}

type arrayGen struct {
	r       *rand.Rand
	b       strings.Builder
	want    strings.Builder
	binds   []*abind
	n       int
	reached map[string]bool
	maps    [2]map[int]int // the model of mm, a default [2]Map[int, int]
}

const arrayPrelude = `type Holder { A [3]int, N int }

func da(a [3]int) int { a[0] * 100 + a[1] * 10 + a[2] }

func das(a [2][]int) int {
	let mut d = 0
	for i, s in a {
		for j, x in s {
			d = d * 7 + x * (i * 2 + j + 1)
		}
	}
	d
}

func ds(s []int) int {
	let mut d = 0
	for x in s {
		d = d * 10 + x
	}
	d
}

func dmm(a [2]Map[int, int]) int {
	let mut d = 0
	for i, m in a {
		for k in 0..3 {
			match m.Get(k) {
			case Some[int] as v:
				d += (v.Value + 1) * (i * 3 + k + 1) * (i + 1)
			case None:
			}
		}
		d = d * 10 + len(m)
	}
	d
}

func idA(a [3]int) [3]int { a }

func idAS(a [2][]int) [2][]int { a }

func bumpA(a [3]int) int {
	let mut b = a
	b[1] = b[1] + 5
	da(b)
}

func bumpAS(a [2][]int) int {
	let mut b = a
	b[0][1] = 9
	das(b)
}

`

func (g *arrayGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *arrayGen) print(x string, d int) {
	g.line(fmt.Sprintf("Console.Println(\"\\(%s)\")", x))
	fmt.Fprintf(&g.want, "%d\n", d)
}

func (g *arrayGen) digest(b *abind) {
	g.print(akindDigest[b.kind]+"("+b.name+")", b.val.digest(b.kind))
}

// literal is a literal of kind k with random content, and its model.
func (g *arrayGen) literal(k akind) (string, aval) {
	var v aval
	if k == aInts {
		var parts []string
		for i := range v.ints {
			v.ints[i] = g.r.IntN(9)
			parts = append(parts, fmt.Sprint(v.ints[i]))
		}
		return "[3]int{" + strings.Join(parts, ", ") + "}", v
	}
	var parts []string
	for i := range v.slices {
		a, b := g.r.IntN(9), g.r.IntN(9)
		v.slices[i] = []int{a, b}
		parts = append(parts, fmt.Sprintf("[]int{%d, %d}", a, b))
	}
	return "[2][]int{" + strings.Join(parts, ", ") + "}", v
}

func (g *arrayGen) pick(k akind) *abind {
	var c []*abind
	for _, b := range g.binds {
		if b.kind == k {
			c = append(c, b)
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[g.r.IntN(len(c))]
}

// bind adds a binding of kind k from a source.
func (g *arrayGen) bind(k akind) {
	text, val, path := "", aval{}, ""
	switch src := g.pick(k); {
	case src != nil && g.r.IntN(3) == 0:
		text, val, path = src.name, src.val.copy(), "source binding"
	case src != nil && g.r.IntN(2) == 0:
		text, val, path = []string{"idA(", "idAS("}[k]+src.name+")", src.val.copy(), "source result"
	case k == aInts && g.r.IntN(4) == 0:
		text, path = "[3]int{}", "source default"
	default:
		text, val = g.literal(k)
		path = "source literal"
	}
	g.reached[path] = true
	g.n++
	b := &abind{name: fmt.Sprintf("v%d", g.n), kind: k, val: val}
	g.binds = append(g.binds, b)
	g.line("\x00" + b.name + "\x00 " + b.name + " " + akindType[k] + " = " + text)
}

func (g *arrayGen) stmt() {
	k := akind(g.r.IntN(2))
	b := g.pick(k)
	if b == nil || g.r.IntN(5) == 0 {
		g.bind(k)
		return
	}
	switch g.r.IntN(10) {
	case 0: // an element write
		b.written = true
		g.reached["write element"] = true
		if k == aInts {
			i, x := g.r.IntN(3), g.r.IntN(9)
			b.val.ints[i] = x
			g.line(fmt.Sprintf("%s[%d] = %d", b.name, i, x))
		} else {
			i, j, x := g.r.IntN(2), g.r.IntN(2), g.r.IntN(9)
			b.val.slices[i][j] = x
			g.line(fmt.Sprintf("%s[%d][%d] = %d", b.name, i, j, x))
		}
	case 1: // a whole element replaced
		b.written = true
		g.reached["write whole element"] = true
		if k == aInts {
			i := g.r.IntN(3)
			b.val.ints[i] = b.val.ints[(i+1)%3]
			g.line(fmt.Sprintf("%s[%d] = %s[%d]", b.name, i, b.name, (i+1)%3))
		} else {
			i := g.r.IntN(2)
			b.val.slices[i] = append([]int(nil), b.val.slices[1-i]...)
			g.line(fmt.Sprintf("%s[%d] = %s[%d]", b.name, i, b.name, 1-i))
		}
	case 2: // a whole reassignment from another binding
		if o := g.pick(k); o != nil && o != b {
			b.written = true
			g.reached["reassign"] = true
			b.val = o.val.copy()
			g.line(b.name + " = " + o.name)
		}
	case 3: // an argument its callee copies and writes
		g.reached["argument"] = true
		v := b.val.copy()
		if k == aInts {
			v.ints[1] += 5
			g.print("bumpA("+b.name+")", v.digest(k))
		} else {
			v.slices[0][1] = 9
			g.print("bumpAS("+b.name+")", v.digest(k))
		}
	case 4: // a record field written through the record
		if k == aInts {
			g.reached["field"] = true
			g.n++
			h := fmt.Sprintf("h%d", g.n)
			i, x := g.r.IntN(3), g.r.IntN(9)
			g.line(fmt.Sprintf("let mut %s = Holder{A: %s, N: 1}", h, b.name))
			g.line(fmt.Sprintf("%s.A[%d] = %d", h, i, x))
			v := b.val.copy()
			v.ints[i] = x
			g.print("da("+h+".A)", v.digest(k))
		}
	case 5: // a slice of the array, written after
		g.reached["slice"] = true
		g.n++
		s := fmt.Sprintf("s%d", g.n)
		if k == aInts && g.r.IntN(2) == 0 {
			// The array written after its slice is taken.
			g.reached["slice then write"] = true
			b.written = true
			old := b.val.ints[1]*10 + b.val.ints[2]
			b.val.ints[1] = 7
			g.line(fmt.Sprintf("let %s = %s[1:3]", s, b.name))
			g.line(b.name + "[1] = 7")
			g.print("ds("+s+")", old)
		} else if k == aInts {
			src := b.name
			if g.r.IntN(2) == 0 {
				// A slice of a literal, which Go cannot address.
				g.reached["slice of a literal"] = true
				src = fmt.Sprintf("[3]int{%d, %d, %d}", b.val.ints[0], b.val.ints[1], b.val.ints[2])
			}
			g.line(fmt.Sprintf("let mut %s = %s[1:3]", s, src))
			g.line(s + "[0] = 8")
			g.print("ds("+s+")", 8*10+b.val.ints[2])
		} else {
			g.line(fmt.Sprintf("let mut %s = %s[0:1]", s, b.name))
			g.line(s + "[0][0] = 8")
			g.print("ds("+s+"[0])", 8*10+b.val.slices[0][1])
		}
	case 6: // a loop that writes the array it visits
		if k == aInts {
			b.written = true
			g.reached["loop"] = true
			start := b.val.ints
			for i, x := range start {
				b.val.ints[i] = x + b.val.ints[(i+1)%3]
			}
			g.line(fmt.Sprintf("for i, x in %s {\n\t\t%s[i] = x + %s[(i + 1) %% 3]\n\t}", b.name, b.name, b.name))
		}
	case 7: // == between two arrays: Go's for [3]int, generated for [2][]int
		if o := g.pick(k); o != nil {
			eq := 0
			if k == aInts {
				g.reached["equal"] = true
				if b.val.ints == o.val.ints {
					eq = 1
				}
			} else {
				g.reached["equal slices"] = true
				if slices.Equal(b.val.slices[0], o.val.slices[0]) && slices.Equal(b.val.slices[1], o.val.slices[1]) {
					eq = 1
				}
			}
			g.print("if "+b.name+" == "+o.name+" { 1 } else { 0 }", eq)
		}
	case 8: // a write into a Map element of the default [2]Map[int, int]
		g.reached["default maps"] = true
		i, key, x := g.r.IntN(2), g.r.IntN(3), g.r.IntN(9)
		g.maps[i][key] = x
		g.line(fmt.Sprintf("mm[%d][%d] = %d", i, key, x))
		g.print("dmm(mm)", g.mapsDigest())
	}
	g.digest(b)
}

// mapsDigest is dmm of the model of mm.
func (g *arrayGen) mapsDigest() int {
	d := 0
	for i, m := range g.maps {
		for k := range 3 {
			if v, ok := m[k]; ok {
				d += (v + 1) * (i*3 + k + 1) * (i + 1)
			}
		}
		d = d*10 + len(m)
	}
	return d
}

func arrayProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &arrayGen{r: rand.New(rand.NewPCG(seed, 0x617272)), reached: reached}
	g.maps = [2]map[int]int{{}, {}}
	g.line("let mut mm [2]Map[int, int]")
	for range 80 {
		g.stmt()
	}
	for _, b := range g.binds {
		g.digest(b)
	}
	g.print("dmm(mm)", g.mapsDigest())
	body := g.b.String()
	for _, b := range g.binds {
		kw := "let"
		if b.written {
			kw = "let mut"
		}
		body = strings.ReplaceAll(body, "\x00"+b.name+"\x00", kw)
	}
	return arrayPrelude + "func main() needs Console {\n" + body + "}\n", g.want.String()
}

// TestArrayValuesAgree is the seeded property (see above), under the value
// rule and the copying lowering "copy". It builds Go because the property
// is about what the arrays hold when the program runs: the copies Go makes
// of an array and the copies the value rule adds are only seen by running
// the program.
func TestArrayValuesAgree(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4}
	if *arraySeed != 0 {
		seeds = []uint64{*arraySeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
		name      string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := arrayProgram(seed, reached)
		for _, mode := range []string{"", "copy"} {
			name := fmt.Sprintf("arr%d%s", seed, mode)
			var modes []string
			if mode != "" {
				modes = []string{mode}
			}
			programs = append(programs, lowerValueMode(t, name, src, true, modes...))
			runs = append(runs, run{seed, src, want, name})
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the array programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the array model differ\nReplay: go test -run '^TestArrayValuesAgree$' ./internal/sketch/lower -args -array.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	if *arraySeed == 0 {
		for _, path := range arrayPaths {
			if !reached[path] {
				t.Errorf("the fixed seeds never reach %q: %v", path, reached)
			}
		}
	}
}

// arrayPaths are the paths the fixed seeds must reach.
var arrayPaths = []string{
	"source binding", "source result", "source default", "source literal",
	"write element", "write whole element", "reassign", "argument", "field", "slice", "loop", "equal",
	"equal slices", "default maps", "slice then write", "slice of a literal",
}
