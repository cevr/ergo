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
//   - The shared program uses every path, owned parameters (V5) included:
//     a rebinding x = upM(x, k), in a loop and on a field, an argument
//     that stays live, and a rebinding whose callback reads the binding.
//     It also calls maps.Update with a named callback (rebinding, on a
//     field, and on a Map that stays live): bumpOpt, which has a comma-ok
//     variant, and, on a rebinding, bumpVia, which has none. Lowered with
//     the rule, with the copying lowering "copy" (the rule without owned
//     parameters), with maps.Update written at its site with its Option
//     (updateLowering "option") and with it called (updateLowering
//     "call", update.go), all four print the model's lines. After it, the
//     program makes Refs of Maps (V6) from literals and bindings, and
//     changes them with literals that write in place, delete, keep a
//     share (idM), read through a pure call (sizes), call an owned helper
//     with two returns (putPair), return from two arms, return the value
//     twice ((m, m)) or store a binding, with the plain functions upRef and
//     dupM, and with Get and Set; then it prints every binding and Ref. The
//     copying lowering, which hands nothing over, is the reference.
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

// keyOf is a key of m, the smallest, when m has one and the coin says
// so, else a random key: an Update then sees Some as often as None.
func (g *valueGen) keyOf(m map[int]int) int {
	if len(m) > 0 && g.r.IntN(2) == 0 {
		return slices.Min(slices.Collect(maps.Keys(m)))
	}
	return g.r.IntN(9)
}

// bumpModel is maps.Update(m, k, bumpOpt) on the model, in place.
func bumpModel(m map[int]int, k int) {
	if v, ok := m[k]; ok {
		m[k] = v + 7
	} else {
		m[k] = 300
	}
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
	refs    []*vref
}

// vref is a sync.Ref[Map[int, int]] of main and the model of its value.
type vref struct {
	name string
	m    map[int]int
}

const refFn = "func(m Map[int, int]) "

// refStmt writes one statement over the Refs (V6) and runs it on the
// model: a Ref owns its value, so no binding outside it sees a write made
// through it, and a value the Ref keeps is never one a binding sees
// written. The literal sites take the value over and write it in place;
// the functions upRef and dupM take the plain call.
func (g *valueGen) refStmt() {
	maps1 := func(b *vbind) bool { return b.kind == kMap }
	r := g.refs
	if len(r) == 0 || g.r.IntN(8) == 0 {
		x, v, how := g.source(kMap)
		name := g.fresh()
		g.line(fmt.Sprintf("let %s = cell(%s)", name, x))
		g.refs = append(g.refs, &vref{name: name, m: v.m})
		g.reached["ref new "+how] = true
		return
	}
	ref := r[g.r.IntN(len(r))]
	k, v := g.r.IntN(9), g.r.IntN(9)
	keep := func(kind string, m map[int]int) {
		name := g.fresh()
		g.binds = append(g.binds, &vbind{name: name, kind: kMap, val: mval{m: maps.Clone(m)}})
		g.reached["ref "+kind] = true
		_ = name
	}
	switch g.r.IntN(12) {
	case 0:
		g.line(fmt.Sprintf("%s.Update(%sMap[int, int] { let mut u = m\n\t\tu[%d] = %d\n\t\tu })", ref.name, refFn, k, v))
		ref.m[k] = v
		g.reached["ref own write"] = true
	case 1:
		g.line(fmt.Sprintf("%s.Update(%sMap[int, int] { m.Delete(%d) })", ref.name, refFn, k))
		delete(ref.m, k)
		g.reached["ref own delete"] = true
	case 2:
		g.line(fmt.Sprintf("let v%d = %s.Get()", g.n+1, ref.name))
		keep("get", ref.m)
	case 3:
		b := g.pick(maps1)
		if b == nil {
			return
		}
		g.line(fmt.Sprintf("%s.Set(%s)", ref.name, b.name))
		ref.m = maps.Clone(b.val.m)
		g.reached["ref set"] = true
	case 4:
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, %s(Map[int, int], Map[int, int]) { (m, m) })", g.n+1, ref.name, refFn))
		keep("modify both", ref.m)
	case 5:
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, %s(int, Map[int, int]) { let kept = idM(m)\n\t\tlet mut u = m\n\t\tu[%d] = %d\n\t\t(dm(kept), u) })", g.n+1, ref.name, refFn, k, v))
		fmt.Fprintf(&g.want, "%d\n", dmModel(ref.m))
		g.line(fmt.Sprintf("Console.Println(\"\\(v%d)\")", g.n+1))
		g.n++
		ref.m[k] = v
		g.reached["ref kept"] = true
	case 6:
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, %s([]int, Map[int, int]) { let s = sizes(m)\n\t\tlet mut u = m\n\t\tu[%d] = %d\n\t\t(s, u) })", g.n+1, ref.name, refFn, k, v))
		fmt.Fprintf(&g.want, "%d\n", dsModel([]int{len(ref.m)}))
		g.line(fmt.Sprintf("Console.Println(\"\\(ds(v%d))\")", g.n+1))
		g.n++
		ref.m[k] = v
		g.reached["ref pure read"] = true
	case 7:
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, %s(int, Map[int, int]) { putPair(m, %d) })", g.n+1, ref.name, refFn, k))
		fmt.Fprintf(&g.want, "%d\n", len(ref.m))
		g.line(fmt.Sprintf("Console.Println(\"\\(v%d)\")", g.n+1))
		g.n++
		if k <= 4 {
			ref.m[k] = k + 500
		}
		g.reached["ref owned helper"] = true
	case 8:
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, %s(int, Map[int, int]) {\n\t\tif %d > 4 {\n\t\t\treturn (1, m)\n\t\t}\n\t\tlet mut u = m\n\t\tu[%d] = %d\n\t\t(2, u)\n\t})", g.n+1, ref.name, refFn, k, k, v))
		want := 2
		if k > 4 {
			want = 1
		} else {
			ref.m[k] = v
		}
		fmt.Fprintf(&g.want, "%d\n", want)
		g.line(fmt.Sprintf("Console.Println(\"\\(v%d)\")", g.n+1))
		g.n++
		g.reached["ref arms"] = true
	case 9:
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("%s.Update(upRef)", ref.name))
			ref.m[9] = 900
			g.reached["ref plain update"] = true
			return
		}
		g.line(fmt.Sprintf("let v%d = sync.Modify(%s, dupM)", g.n+1, ref.name))
		keep("plain modify", ref.m)
	case 10:
		b := g.pick(maps1)
		if b == nil {
			return
		}
		g.line(fmt.Sprintf("%s.Update(%sMap[int, int] { %s })", ref.name, refFn, b.name))
		ref.m = maps.Clone(b.val.m)
		g.reached["ref store outside"] = true
	default:
		g.line(fmt.Sprintf("Console.Println(\"\\(sync.Modify(%s, %s(int, Map[int, int]) { (dm(m), m) }))\")", ref.name, refFn))
		fmt.Fprintf(&g.want, "%d\n", dmModel(ref.m))
		g.reached["ref print"] = true
	}
}

const valuePrelude = `import maps
import slices
import sync
import task

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

// viewS writes through std's Slice of s, whose Go body returns a part of
// s, so the bind copies it (gobody.go: only a body that makes its result
// gives a fresh one).
func viewS(s []int, v int) int {
	let mut c = slices.Slice(s, 0, 1)
	c[0] = v
	ds(c)
}

// roomS builds its result in the slice WithCapacity's Go body makes,
// which the bind takes with no copy.
func roomS(s []int, v int) []int {
	let mut c = slices.WithCapacity[int](len(s) + 1)
	for x in s {
		c = c.Append(x)
	}
	c.Append(v)
}

func idM(m Map[int, int]) Map[int, int] { m }

func idB(b Box) Box { b }

func freshM(k int) Map[int, int] {
	let mut m = Map[int, int]{}
	m[k] = k * 2
	m
}

// upM, upS and upF own their first parameter (values.go, V5): a caller
// passes it unshared, and a rebinding x = upM(x, k) hands it over.
func upM(m Map[int, int], k int) Map[int, int] {
	let mut c = m
	c[k] = k + 100
	c
}

func upS(s []int, v int) []int {
	let mut c = s
	c = c.Append(v)
	c[0] = v
	c
}

// upF calls f after it writes: f's capture of the caller's binding sees
// the old value.
func upF(m Map[int, int], k int, f func() int) Map[int, int] {
	let mut c = m
	c[k] = k + 200
	c[k + 1] = f()
	c
}

// bumpOpt is maps.Update's callback, which update.go calls directly, as
// its comma-ok variant.
func bumpOpt(old Option[int]) int {
	match old {
	case Some[int] as v:
		v.Value + 7
	case None:
		300
	}
}

// bumpVia passes its Option on, so it has no comma-ok variant: update.go
// calls it with the Option.
func bumpVia(old Option[int]) int { bumpOpt(old) }

// bumpAt passes its parameter to maps.Update: a copy at the call, and the
// prelude imports maps in every program.
func bumpAt(m Map[int, int], k int) Map[int, int] {
	maps.Update(m, k, bumpOpt)
}

// peekM reads m after it writes its copy: m is not owned.
func peekM(m Map[int, int], k int) int {
	let mut c = m
	c[k] = k + 300
	dm(c) + dm(m)
}

// cell is a Ref that holds m (V6: NewRef stores a value shared outside).
func cell(m Map[int, int]) sync.Ref[Map[int, int]] { sync.NewRef[Map[int, int]](m) }

// sizes is pure, and its result reaches no Map type of its argument, so
// its call keeps nothing of m.
func sizes(m Map[int, int]) []int { []int{len(m)} }

// putPair owns m, and returns it from two returns, as fstest's write does.
func putPair(m Map[int, int], k int) (int, Map[int, int]) {
	let at = len(m)
	let mut u = m
	if k > 4 {
		return (at, u)
	}
	u[k] = k + 500
	(at, u)
}

// upRef and dupM are Ref functions that are not literals: the plain call.
func upRef(m Map[int, int]) Map[int, int] {
	let mut c = m
	c[9] = 900
	c
}

func dupM(m Map[int, int]) (Map[int, int], Map[int, int]) { (m, m) }

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
				if key%2 == 0 { // no new draw: the seeds keep their paths
					out.m[key] = key + 300
					return fmt.Sprintf("%s.Put(%d, %d)", b.name, key, key+300), out, "put live"
				}
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
			// An owned parameter's argument stays live: it is copied,
			// and the binding prints unchanged. peekM reads its
			// parameter after its write, so it owns nothing.
			key = g.r.IntN(9)
			up := maps.Clone(b.val.m)
			up[key] = key + 100
			g.line(fmt.Sprintf("Console.Println(\"\\(dm(upM(%s, %d)))\")", b.name, key))
			fmt.Fprintf(&g.want, "%d\n", dmModel(up))
			g.print(b.name, b.kind, b.val.digest(b.kind))
			peek := maps.Clone(b.val.m)
			peek[key] = key + 300
			g.line(fmt.Sprintf("Console.Println(\"\\(peekM(%s, %d))\")", b.name, key))
			fmt.Fprintf(&g.want, "%d\n", dmModel(peek)+dmModel(b.val.m))
			g.reached["own live"] = true
			// maps.Update on a Map that stays live: a copy is updated.
			key = g.keyOf(b.val.m)
			upd := maps.Clone(b.val.m)
			bumpModel(upd, key)
			g.line(fmt.Sprintf("Console.Println(\"\\(dm(maps.Update(%s, %d, bumpOpt)))\")", b.name, key))
			fmt.Fprintf(&g.want, "%d\n", dmModel(upd))
			g.line(fmt.Sprintf("Console.Println(\"\\(dm(bumpAt(%s, %d)))\")", b.name, key))
			fmt.Fprintf(&g.want, "%d\n", dmModel(upd))
			g.print(b.name, b.kind, b.val.digest(b.kind))
			g.reached["update live"] = true
		}
		if g.shared && b.kind == kSlice {
			v := g.r.IntN(9)
			c := append(slices.Clone(b.val.s), v)
			if len(c) > 1 {
				c[0] = v
			}
			g.line(fmt.Sprintf("Console.Println(\"\\(putS(%s, %d))\")", b.name, v))
			fmt.Fprintf(&g.want, "%d\n", dsModel(c))
			// A std result that shares its argument, and one its Go
			// body makes (gobody.go); no new draw, so the seeds keep
			// their paths.
			g.line(fmt.Sprintf("Console.Println(\"\\(viewS(%s, %d)) \\(ds(roomS(%s, %d)))\")", b.name, v, b.name, v))
			fmt.Fprintf(&g.want, "%d %d\n", dsModel([]int{v}), dsModel(append(slices.Clone(b.val.s), v)))
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
		// is called or waited for at the end. A binding that may be written
		// is not captured: a task that captures a let mut written after it
		// is S108 (types.md, Function values 3; TestCaptureRuleModel).
		b := g.pick(func(b *vbind) bool { return !b.mut })
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
		switch g.r.IntN(9) {
		case 7:
			// maps.Update with a named callback, rebinding the Map.
			key := g.keyOf(b.val.m)
			g.line(fmt.Sprintf("%s = maps.Update(%s, %d, bumpOpt)", b.name, b.name, key))
			bumpModel(b.val.m, key)
			g.reached["update rebind"] = true
		case 8:
			// The same with a callback that has no comma-ok variant.
			key := g.keyOf(b.val.m)
			g.line(fmt.Sprintf("%s = maps.Update(%s, %d, bumpVia)", b.name, b.name, key))
			bumpModel(b.val.m, key)
			g.reached["update rebind option"] = true
		case 4:
			// A rebinding hands the old value to an owned parameter.
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s = upM(%s, %d)", b.name, b.name, key))
			b.val.m[key] = key + 100
			g.reached["own rebind"] = true
		case 5:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s = upM(%s, i + %d)\n\t}", b.name, b.name, key))
			for i := range 3 {
				b.val.m[i+key] = i + key + 100
			}
			g.reached["own loop"] = true
		case 6:
			if !g.shared {
				return // the reference shares upF's write with the capture
			}
			// The callback reads the binding the call rebinds: it is
			// not handed over, so the callback sees the old value.
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s = upF(%s, %d, func() int { dm(%s) })", b.name, b.name, key, b.name))
			old := dmModel(b.val.m)
			b.val.m[key] = key + 200
			b.val.m[key+1] = old
			g.reached["own capture"] = true
		case 0:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s[%d] = %d", b.name, key, v))
			b.val.m[key] = v
			g.reached["write map"] = true
		case 1:
			key := g.r.IntN(9)
			// The parity of v, already drawn, picks Put or Delete, so the
			// seeds draw what they drew before Put and reach every path.
			if v%2 == 0 {
				g.line(fmt.Sprintf("for i in 0..3 {\n\t\t%s = %s.Put(i + %d, %d)\n\t}", b.name, b.name, key, v))
				for i := range 3 {
					b.val.m[i+key] = v
				}
				g.reached["put in place"] = true
				break
			}
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
		switch g.r.IntN(4) {
		case 3:
			g.line(fmt.Sprintf("%s = upS(%s, %d)", b.name, b.name, v))
			b.val.s = append(b.val.s, v)
			b.val.s[0] = v
			g.reached["own slice"] = true
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
		switch g.r.IntN(5) {
		case 4:
			key := g.keyOf(b.val.m)
			g.line(fmt.Sprintf("%s.M = maps.Update(%s.M, %d, bumpOpt)", b.name, b.name, key))
			bumpModel(b.val.m, key)
			g.reached["update field"] = true
		case 3:
			key := g.r.IntN(9)
			g.line(fmt.Sprintf("%s.M = upM(%s.M, %d)", b.name, b.name, key))
			b.val.m[key] = key + 100
			g.reached["own field"] = true
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
	if shared {
		// The Refs (V6), then every binding and Ref as the model has it:
		// a write through a Ref changes no binding.
		for range 30 {
			g.refStmt()
		}
		for _, b := range g.binds {
			g.print(b.name, b.kind, b.val.digest(b.kind))
		}
		for _, ref := range g.refs {
			g.line(fmt.Sprintf("Console.Println(\"\\(dm(%s.Get()))\")", ref.name))
			fmt.Fprintf(&g.want, "%d\n", dmModel(ref.m))
		}
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

// lowerValueMode lowers src with the value rule or with the reference
// "alias"; a mode names another lowering: "copy", the rule without owned
// parameters, "update-call", the rule with maps.Update called, not
// written at its site, or "update-option", the rule with it written at
// its site with its Option, no comma-ok variant (update.go).
func lowerValueMode(t *testing.T, name, src string, rule bool, mode ...string) *lowered {
	t.Helper()
	saved, savedUpdate, savedFresh, savedOwn := valueLowering, updateLowering, freshLowering, ownLowering
	defer func() {
		valueLowering, updateLowering, freshLowering, ownLowering = saved, savedUpdate, savedFresh, savedOwn
	}()
	valueLowering, updateLowering, freshLowering, ownLowering = "", "", "", ""
	switch {
	case !rule:
		valueLowering = "alias"
	case len(mode) > 0 && mode[0] == "names":
		freshLowering, ownLowering = "name", "name"
	case len(mode) > 0 && mode[0] == "update-call":
		updateLowering = "call"
	case len(mode) > 0 && mode[0] == "update-option":
		updateLowering = "option"
	case len(mode) > 0:
		valueLowering = mode[0]
	}
	return mustLower(t, name, src)
}

// TestValueRuleAgrees is the seeded property (see above).
//
// It builds Go because it compares the runs of built programs with a
// reference (another lowering, a fold, or Go's own package), and the
// question is behaviour at run time; no in-process check observes that, and
// Ergo has no reference interpreter yet (test-speed T8).
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
		// The copying lowering, the reference of owned parameters.
		programs = append(programs, lowerValueMode(t, name+"copy", src, true, "copy"))
		runs = append(runs, run{seed, src, want, name + "copy"})
		// maps.Update called, the reference of its rewrite at the site.
		programs = append(programs, lowerValueMode(t, name+"call", src, true, "update-call"))
		runs = append(runs, run{seed, src, want, name + "call"})
		// maps.Update at its site with its Option, the reference of the
		// callback's comma-ok variant.
		programs = append(programs, lowerValueMode(t, name+"option", src, true, "update-option"))
		runs = append(runs, run{seed, src, want, name + "option"})
		// The name-keyed references OPT6 retires: WithCapacity fresh by
		// its name (gobody.go), the owned Ref sites by sync's names
		// (refown.go).
		programs = append(programs, lowerValueMode(t, name+"names", src, true, "names"))
		runs = append(runs, run{seed, src, want, name + "names"})
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
	"source append live", "source delete live", "source put live", "source append element", "source update",
	"argument", "pure", "closure", "task", "loop kept", "literal write",
	"write map", "delete in place", "put in place", "write loop", "write slice", "append in place",
	"write field map", "write field slice", "append field", "write element",
	"own rebind", "own loop", "own capture", "own slice", "own field", "own live",
	"update rebind", "update rebind option", "update field", "update live",
	"ref new literal", "ref new binding", "ref own write", "ref own delete", "ref get", "ref set", "ref modify both",
	"ref kept", "ref pure read", "ref owned helper", "ref arms", "ref plain update", "ref plain modify",
	"ref store outside", "ref print",
}
