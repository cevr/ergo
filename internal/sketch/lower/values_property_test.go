package lower

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The value rule's seeded property (value-rule; common.md, lowering rule).
// Each seed writes two programs over Maps, slices, a record of both and a
// slice of Maps. They bind, copy, write and read the values through every
// path the rule covers: a let and a let mut bind, a field and an element
// read, an argument whose callee copies and writes it, a function result,
// a record update, a function literal's and a task's capture, a write in a
// non-escaping literal, Append and Delete in place and on a value that
// stays live, a whole reassignment, and a read kept in a loop that writes
// after it. The generator is also the reference interpreter: it runs each
// statement on a model with value semantics (every bind copies) and writes
// the line the program must print.
//
//   - The shared program uses every path. Lowered with the rule, it prints
//     the model's lines.
//   - The unshared program writes only through let mut roots bound from
//     literals and reads them only to print, so no write is shared. Lowered
//     with the rule and with the reference (valueLowering "alias"), both
//     print the model's lines: the rule changes no program that shares
//     nothing.
//
// Purity: next and putM are pure (no environment in their Go) and each
// call prints what the model says, so equal arguments give equal results,
// where the alias lowering gave a growing count.
//
// A failure prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestValueRuleAgrees$' ./internal/sketch/lower -args -value.seed=N

var valueSeed = flag.Uint64("value.seed", 0, "run TestValueRuleAgrees on this seed alone")

// vkind is a generated value's type.
type vkind int

const (
	kMap   vkind = iota // Map[int, int]
	kSlice              // []int
	kBox                // Box{M Map[int, int], S []int}
	kMaps               // []Map[int, int]
)

var kindDigest = []string{"dm", "ds", "db", "dms"}

// mval is a model value: m and s for a Map, a slice or a Box, ms for a
// slice of Maps.
type mval struct {
	m  map[int]int
	s  []int
	ms []map[int]int
}

func (v mval) copy() mval {
	out := mval{m: maps.Clone(v.m), s: slices.Clone(v.s)}
	for _, m := range v.ms {
		out.ms = append(out.ms, maps.Clone(m))
	}
	if v.ms != nil && out.ms == nil {
		out.ms = []map[int]int{}
	}
	return out
}

func dmModel(m map[int]int) int {
	t := len(m) * 1000
	for k, v := range m {
		t += k*7 + v*13
	}
	return t
}

func dsModel(s []int) int {
	t := len(s) * 1000
	for i, v := range s {
		t += (i + 1) * v
	}
	return t
}

func (v mval) digest(k vkind) int {
	switch k {
	case kMap:
		return dmModel(v.m)
	case kSlice:
		return dsModel(v.s)
	case kBox:
		return dmModel(v.m)*3 + dsModel(v.s)
	}
	t := len(v.ms) * 1000
	for i, m := range v.ms {
		t += (i + 1) * dmModel(m)
	}
	return t
}

// vbind is a binding of main.
type vbind struct {
	name    string
	kind    vkind
	mut     bool
	written bool
	val     mval
}

type valueGen struct {
	r       *rand.Rand
	shared  bool
	binds   []*vbind
	b       strings.Builder
	want    strings.Builder
	n       int
	reached map[string]bool
	pending []func() // prints of closures and tasks, run at the end
}

const valuePrelude = `import task

type Box { M Map[int, int], S []int }

let calls = Map[int, int]{}

// next copies the package let and writes the copy: pure.
func next(n int) int {
	let mut c = calls
	c[len(c)] = n
	len(c)
}

// putM copies its argument and writes the copy: pure.
func putM(m Map[int, int], k int) int {
	let mut c = m
	c[k] = k
	dm(c)
}

func putS(s []int, v int) int {
	let mut c = s
	c = c.Append(v)
	if len(c) > 1 {
		c[0] = v
	}
	ds(c)
}

func idM(m Map[int, int]) Map[int, int] { m }

func idB(b Box) Box { b }

func freshM(k int) Map[int, int] {
	let mut m = Map[int, int]{}
	m[k] = k * 2
	m
}

func apply(f func(k int)) {
	f(3)
	f(4)
}

func dm(m Map[int, int]) int {
	let mut t = len(m) * 1000
	for k, v in m {
		t = t + k * 7 + v * 13
	}
	t
}

func ds(s []int) int {
	let mut t = len(s) * 1000
	for i, v in s {
		t = t + (i + 1) * v
	}
	t
}

func db(b Box) int { dm(b.M) * 3 + ds(b.S) }

func dms(ms []Map[int, int]) int {
	let mut t = len(ms) * 1000
	for i, m in ms {
		t = t + (i + 1) * dm(m)
	}
	t
}

`

func (g *valueGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

// print prints the digest of the expression x of kind k, which the model
// says is d.
func (g *valueGen) print(x string, k vkind, d int) {
	g.line(fmt.Sprintf("Console.Println(\"\\(%s(%s))\")", kindDigest[k], x))
	fmt.Fprintf(&g.want, "%d\n", d)
}

func (g *valueGen) fresh() string {
	g.n++
	return fmt.Sprintf("v%d", g.n)
}

// literal is a literal of kind k with random content, and its model.
func (g *valueGen) literal(k vkind) (string, mval) {
	mapLit := func() (string, map[int]int) {
		m := map[int]int{}
		var parts []string
		for range g.r.IntN(3) {
			key, v := g.r.IntN(9), g.r.IntN(9)
			if _, dup := m[key]; dup {
				continue
			}
			m[key] = v
			parts = append(parts, fmt.Sprintf("%d: %d", key, v))
		}
		return "Map[int, int]{" + strings.Join(parts, ", ") + "}", m
	}
	sliceLit := func() (string, []int) {
		s := []int{}
		var parts []string
		for range 1 + g.r.IntN(3) {
			v := g.r.IntN(9)
			s = append(s, v)
			parts = append(parts, fmt.Sprint(v))
		}
		return "[]int{" + strings.Join(parts, ", ") + "}", s
	}
	switch k {
	case kMap:
		x, m := mapLit()
		return x, mval{m: m}
	case kSlice:
		x, s := sliceLit()
		return x, mval{s: s}
	case kBox:
		xm, m := mapLit()
		xs, s := sliceLit()
		return "Box{M: " + xm + ", S: " + xs + "}", mval{m: m, s: s}
	}
	var parts []string
	ms := []map[int]int{}
	for range 1 + g.r.IntN(2) {
		x, m := mapLit()
		parts = append(parts, x)
		ms = append(ms, m)
	}
	return "[]Map[int, int]{" + strings.Join(parts, ", ") + "}", mval{ms: ms}
}

func (g *valueGen) pick(ok func(*vbind) bool) *vbind {
	var c []*vbind
	for _, b := range g.binds {
		if ok(b) {
			c = append(c, b)
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[g.r.IntN(len(c))]
}

// source is an expression of kind k read from the program's bindings and
// its model value (a copy), or a literal.
func (g *valueGen) source(k vkind) (string, mval, string) {
	if !g.shared {
		x, v := g.literal(k)
		return x, v, "literal"
	}
	switch g.r.IntN(9) {
	case 0:
		x, v := g.literal(k)
		return x, v, "literal"
	case 1, 2:
		if b := g.pick(func(b *vbind) bool { return b.kind == k }); b != nil {
			return b.name, b.val.copy(), "binding"
		}
	case 3, 7:
		// A field of a Box, or an element of a slice of Maps.
		switch k {
		case kMap:
			if b := g.pick(func(b *vbind) bool { return b.kind == kBox }); b != nil && g.r.IntN(2) == 0 {
				return b.name + ".M", mval{m: maps.Clone(b.val.m)}, "field"
			}
			if b := g.pick(func(b *vbind) bool { return b.kind == kMaps && len(b.val.ms) > 0 }); b != nil {
				return b.name + "[0]", mval{m: maps.Clone(b.val.ms[0])}, "element"
			}
		case kSlice:
			if b := g.pick(func(b *vbind) bool { return b.kind == kBox }); b != nil {
				return b.name + ".S", mval{s: slices.Clone(b.val.s)}, "field"
			}
		}
	case 4:
		// A function result that shares its argument, or a fresh one.
		switch k {
		case kMap:
			if b := g.pick(func(b *vbind) bool { return b.kind == kMap }); b != nil && g.r.IntN(2) == 0 {
				return "idM(" + b.name + ")", b.val.copy(), "result"
			}
			key := g.r.IntN(9)
			return fmt.Sprintf("freshM(%d)", key), mval{m: map[int]int{key: key * 2}}, "fresh result"
		case kBox:
			if b := g.pick(func(b *vbind) bool { return b.kind == kBox }); b != nil {
				return "idB(" + b.name + ")", b.val.copy(), "result"
			}
		}
	case 5, 8:
		// Append or Delete on a value that stays live.
		switch k {
		case kSlice:
			if b := g.pick(func(b *vbind) bool { return b.kind == kSlice }); b != nil {
				v := g.r.IntN(9)
				out := b.val.copy()
				out.s = append(out.s, v)
				return fmt.Sprintf("%s.Append(%d)", b.name, v), out, "append live"
			}
		case kMap:
			if b := g.pick(func(b *vbind) bool { return b.kind == kMap }); b != nil {
				key := g.r.IntN(9)
				out := b.val.copy()
				delete(out.m, key)
				return fmt.Sprintf("%s.Delete(%d)", b.name, key), out, "delete live"
			}
		case kMaps:
			if b := g.pick(func(b *vbind) bool { return b.kind == kMaps }); b != nil {
				if m := g.pick(func(m *vbind) bool { return m.kind == kMap }); m != nil {
					out := b.val.copy()
					out.ms = append(out.ms, maps.Clone(m.val.m))
					return fmt.Sprintf("%s.Append(%s)", b.name, m.name), out, "append element"
				}
			}
		}
	case 6:
		// A record update that keeps a field of its base.
		if k == kBox {
			if b := g.pick(func(b *vbind) bool { return b.kind == kBox }); b != nil {
				xs, s := g.literal(kSlice)
				return "Box{.." + b.name + ", S: " + xs + "}", mval{m: maps.Clone(b.val.m), s: s.s}, "update"
			}
		}
	}
	x, v := g.literal(k)
	return x, v, "literal"
}

// stmt writes one generated statement and runs it on the model.
func (g *valueGen) stmt() {
	switch g.r.IntN(10) {
	case 0, 1, 2:
		k := vkind(g.r.IntN(4))
		mut := g.r.IntN(2) == 0
		x, v, how := g.source(k)
		name := g.fresh()
		kw := "let"
		if mut {
			// A let mut that nothing writes is S110: the keyword is
			// decided when the program is written (valueProgram).
			kw = "\x00" + name + "\x00"
		}
		g.line(kw + " " + name + " = " + x)
		g.reached["source "+how], g.reached["bind"] = true, true
		b := &vbind{name: name, kind: k, mut: mut, val: v}
		g.binds = append(g.binds, b)
		g.print(name, k, b.val.digest(k))
	case 3, 4:
		g.write()
	case 5:
		b := g.pick(func(b *vbind) bool { return true })
		if b == nil {
			return
		}
		g.print(b.name, b.kind, b.val.digest(b.kind))
		if g.shared && b.kind == kMap {
			key := g.r.IntN(20)
			c := maps.Clone(b.val.m)
			c[key] = key
			for range 2 {
				g.line(fmt.Sprintf("Console.Println(\"\\(putM(%s, %d))\")", b.name, key))
				fmt.Fprintf(&g.want, "%d\n", dmModel(c))
			}
			g.reached["argument"] = true
		}
		if g.shared && b.kind == kSlice {
			v := g.r.IntN(9)
			c := append(slices.Clone(b.val.s), v)
			if len(c) > 1 {
				c[0] = v
			}
			g.line(fmt.Sprintf("Console.Println(\"\\(putS(%s, %d))\")", b.name, v))
			fmt.Fprintf(&g.want, "%d\n", dsModel(c))
			g.reached["argument"] = true
		}
	case 6:
		if !g.shared {
			return // next shares the package let under the reference
		}
		g.line("Console.Println(\"\\(next(7))\")")
		g.want.WriteString("1\n")
		g.reached["pure"] = true
	case 7:
		// A capture: a function literal or a task reads the value now; it
		// is called or waited for at the end.
		b := g.pick(func(b *vbind) bool { return g.shared || !b.mut })
		if b == nil {
			return
		}
		name := g.fresh()
		d := b.val.digest(b.kind)
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("let %s = func() int { %s(%s) }", name, kindDigest[b.kind], b.name))
			g.pending = append(g.pending, func() {
				g.line(fmt.Sprintf("Console.Println(\"\\(%s())\")", name))
				fmt.Fprintf(&g.want, "%d\n", d)
			})
			g.reached["closure"] = true
		} else {
			g.line(fmt.Sprintf("let %s = task.Go() { %s(%s) }", name, kindDigest[b.kind], b.name))
			g.pending = append(g.pending, func() {
				g.line(fmt.Sprintf("Console.Println(\"\\(%s.Wait())\")", name))
				fmt.Fprintf(&g.want, "%d\n", d)
			})
			g.reached["task"] = true
		}
	case 8:
		// A read kept in a loop that writes the root after it.
		if !g.shared {
			return
		}
		b := g.pick(func(b *vbind) bool { return b.mut && b.kind == kMap })
		if b == nil {
			return
		}
		kept := g.fresh()
		g.line("for i in 0..2 {")
		g.line(fmt.Sprintf("\tlet %s = %s", kept, b.name))
		g.line(fmt.Sprintf("\t%s[50 + i] = i", b.name))
		g.line(fmt.Sprintf("\tConsole.Println(\"\\(dm(%s))\")", kept))
		g.line("}")
		for i := range 2 {
			fmt.Fprintf(&g.want, "%d\n", dmModel(b.val.m))
			b.val.m[50+i] = i
		}
		b.written = true
		g.reached["loop kept"] = true
	case 9:
		// A write in a non-escaping literal.
		b := g.pick(func(b *vbind) bool { return b.mut && b.kind == kMap })
		if b == nil {
			return
		}
		v := g.r.IntN(9)
		g.line(fmt.Sprintf("apply(func(k int) { %s[k] = %d })", b.name, v))
		b.val.m[3], b.val.m[4] = v, v
		b.written = true
		g.reached["literal write"] = true
	}
}

// reassignSource is the value of a whole reassignment of b: a source that
// is not b itself (go vet refuses x = x).
func (g *valueGen) reassignSource(b *vbind) (string, mval, string) {
	x, v, how := g.source(b.kind)
	if x == b.name {
		x, v = g.literal(b.kind)
		how = "literal"
	}
	return x, v, how
}

// write writes through a let mut root and runs the write on the model.
func (g *valueGen) write() {
	b := g.pick(func(b *vbind) bool {
		return b.mut && (b.kind != kMaps || len(b.val.ms) > 0) && (b.kind != kBox || len(b.val.s) > 0)
	})
	if b == nil {
		return
	}
	b.written = true
	v := g.r.IntN(9)
	switch b.kind {
	case kMap:
		switch g.r.IntN(4) {
		case 0:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s[%d] = %d", b.name, key, v))
			b.val.m[key] = v
			g.reached["write map"] = true
		case 1:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s = %s.Delete(%d)", b.name, b.name, key))
			delete(b.val.m, key)
			g.reached["delete in place"] = true
		case 2:
			g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s[i + 10] = i + %d\n\t}", b.name, v))
			for i := range 3 {
				b.val.m[i+10] = i + v
			}
			g.reached["write loop"] = true
		default:
			x, nv, how := g.reassignSource(b)
			g.line(b.name + " = " + x)
			b.val = nv
			g.reached["source "+how], g.reached["reassign"] = true, true
		}
	case kSlice:
		switch g.r.IntN(3) {
		case 0:
			i := g.r.IntN(len(b.val.s))
			g.line(fmt.Sprintf("%s[%d] = %d", b.name, i, v))
			b.val.s[i] = v
			g.reached["write slice"] = true
		case 1:
			g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s = %s.Append(i + %d)\n\t}", b.name, b.name, v))
			for i := range 3 {
				b.val.s = append(b.val.s, i+v)
			}
			g.reached["append in place"] = true
		default:
			x, nv, how := g.reassignSource(b)
			g.line(b.name + " = " + x)
			b.val = nv
			g.reached["source "+how], g.reached["reassign"] = true, true
		}
	case kBox:
		switch g.r.IntN(3) {
		case 0:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s.M[%d] = %d", b.name, key, v))
			b.val.m[key] = v
			g.reached["write field map"] = true
		case 1:
			if len(b.val.s) > 0 {
				g.line(fmt.Sprintf("%s.S[0] = %d", b.name, v))
				b.val.s[0] = v
			}
			g.reached["write field slice"] = true
		default:
			g.line(fmt.Sprintf("%s.S = %s.S.Append(%d)", b.name, b.name, v))
			b.val.s = append(b.val.s, v)
			g.reached["append field"] = true
		}
	case kMaps:
		if len(b.val.ms) == 0 {
			return
		}
		key := g.r.IntN(9)
		g.line(fmt.Sprintf("%s[0][%d] = %d", b.name, key, v))
		b.val.ms[0][key] = v
		g.reached["write element"] = true
	}
}

// valueProgram writes the program of seed, shared or not, and the lines
// it must print.
func valueProgram(seed uint64, shared bool, reached map[string]bool) (src, want string) {
	salt := uint64(0x76616c)
	if shared {
		salt++
	}
	g := &valueGen{r: rand.New(rand.NewPCG(seed, salt)), shared: shared, reached: reached}
	for range 60 {
		g.stmt()
	}
	for _, f := range g.pending {
		f()
	}
	g.line("Console.Println(\"\\(len(calls))\")")
	g.want.WriteString("0\n")
	g.line("let last = task.Go() { len(calls) + 1 }")
	g.line("Console.Println(\"\\(last.Wait())\")")
	g.want.WriteString("1\n")
	body := g.b.String()
	for _, b := range g.binds {
		kw := "let"
		if b.written {
			kw = "let mut"
		}
		body = strings.ReplaceAll(body, "\x00"+b.name+"\x00", kw)
	}
	return valuePrelude + "func main() needs Console {\n" + body + "}\n", g.want.String()
}

// lowerValueMode lowers src with the value rule or with the reference.
func lowerValueMode(t *testing.T, name, src string, rule bool) *lowered {
	t.Helper()
	saved := valueLowering
	defer func() { valueLowering = saved }()
	valueLowering = ""
	if !rule {
		valueLowering = "alias"
	}
	return mustLower(t, name, src)
}

// TestValueRuleAgrees is the seeded property (see above).
func TestValueRuleAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *valueSeed != 0 {
		seeds = []uint64{*valueSeed}
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
		src, want := valueProgram(seed, true, reached)
		name := fmt.Sprintf("vr%dshared", seed)
		programs = append(programs, lowerValueMode(t, name, src, true))
		runs = append(runs, run{seed, src, want, name})
		usrc, uwant := valueProgram(seed, false, reached)
		for _, rule := range []bool{true, false} {
			name := fmt.Sprintf("vr%dunshared%v", seed, rule)
			programs = append(programs, lowerValueMode(t, name, usrc, rule))
			runs = append(runs, run{seed, usrc, uwant, name})
		}
	}
	for _, p := range programs[:1] {
		for _, fn := range []string{"next", "putM"} {
			if body := goFunc(t, p.mainGo(), fn); strings.Contains(strings.SplitN(body, "\n", 2)[0], "*Env") {
				t.Errorf("%s is not lowered pure:\n%s", fn, body)
			}
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the value programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the value model differ\nReplay: go test -run '^TestValueRuleAgrees$' ./internal/sketch/lower -args -value.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	t.Logf("seeds %v: %d paths reached", seeds, len(reached))
	if *valueSeed == 0 {
		for _, path := range valuePaths {
			if !reached[path] {
				t.Errorf("the fixed seeds never reach %q: %v", path, reached)
			}
		}
	}
}

// valuePaths are the paths the fixed seeds must reach.
var valuePaths = []string{
	"bind", "reassign", "source literal", "source binding", "source field", "source element", "source result", "source fresh result",
	"source append live", "source delete live", "source append element", "source update",
	"argument", "pure", "closure", "task", "loop kept", "literal write",
	"write map", "delete in place", "write loop", "write slice", "append in place",
	"write field map", "write field slice", "append field", "write element",
}
