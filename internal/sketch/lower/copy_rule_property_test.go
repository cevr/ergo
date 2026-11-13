package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The seeded properties of the entry rule (V8) and the entry move-out
// (V9), values.go and moveout.go. Each seed writes a program of functions
// over stores {m Map[K, E], s []int, out [][]int}, whose entry E is int,
// []int, Map[int, int] or a Box of a Map and a slice, read through a
// borrow (r *st) or through a let mut local. The stores come in two
// families: plain ones (K int), which no function moves an entry out of,
// and moved ones (K string), which the entry rmw shapes move out of, so
// their Map types are entry-written (V9). Each function is one shape:
//
//   - get keep, get match, get none: Get reads an entry, and a write
//     through the root follows. Plain: no copy, no write reaches an
//     entry. Moved: a kept Option copies the receiver, a matched Get the
//     entry it gives, a compared one nothing;
//   - get in flight: the key's evaluation writes the root (bump), so the
//     receiver is copied once;
//   - window: slices.Slice of a field that is written after it is kept
//     in another field: one copy, of the window;
//   - chain param, chain loop, chain put, chain delete: Append, Put and
//     Delete on the result of another: one copy of a shared source, none
//     of a let mut root's; chain root, a chain on a shared []Map into a
//     let mut whose Maps are then written (agreement only);
//   - copy root: a let mut copy of the Map, written, while the root is
//     read after: one copy, of the table (plain) or of the entries too
//     (moved);
//   - entry copy (plain): an entry read into a let mut, written and stored
//     under another key: one copy of the entry;
//   - entry rmw (moved): an entry read into a let mut, written and stored
//     back: no copy (V9); rmw root between, rmw kept after: the same with
//     the root named between, or X read after the store, so the entry is
//     copied once;
//   - ref modify: one key of a Map in a Ref changed in the owned callback:
//     the cell's copy-if-shared function, and in the moved family the
//     entry the kept match gives; ref rmw (moved): the X1 shape, an entry
//     moved out in the callback: the cell's function only;
//   - rmw snapshot, rmw kept get, rmw put shared, rmw literal shared, rmw
//     across call, rmw generic, rmw option fn, rmw range, rmw ref get, rmw
//     same value (moved, agreement only): a value that shares an entry's
//     storage under V8 alone (a table copy, a kept Option, a Put or
//     literal value, a generic store, a returned Option, a range value, a
//     Ref's Get, one value at two keys) is read after an entry move-out,
//     and must not see it.
//   - update, update named: m = maps.Update(m, k, f) with a literal or a
//     top-level function by name whose results are fresh: no Map copy
//     (argsUnshared); store read: an entry stored under another key, then
//     read: no copy at the store unless the type is entry-written;
//     update snapshot, update other, update shared, update named shared
//     (agreement only): an Update whose result may share an entry.
//
// copyShape's model is the number of copies the lowering makes in each
// function: TestCopyCountsMatchModel counts the copies in the lowered Go
// of each function (maps.Clone, slices.Clone, slices.Clip, a generated copy
// function) and holds them equal, and holds that no plain Map is copied
// entry by entry (V8). TestCopyRuleAgrees builds the same programs under
// the rules and under the reference (copyLowering "whole": no V8, no V9,
// whose Maps are never written in place) and holds their outputs equal.
//
// A failure prints the seed, the Ergo source and the Go of the function.
// Replay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=N

var copySeed = flag.Uint64("copy.seed", 0, "run the copy-rule properties on this seed alone")

func copySeeds() []uint64 {
	if *copySeed != 0 {
		return []uint64{*copySeed}
	}
	return []uint64{1, 2, 3, 4}
}

// copyEntry is an entry kind: its suffix, its Ergo type, a zero literal,
// a sample literal, whether it holds a Map or slice, and a write through
// a let mut s of it.
type copyEntry struct {
	k, typ, zero, sample, write string
	holds                       bool
}

var copyEntries = []copyEntry{
	{"I", "int", "0", "5", "s = s + 1", false},
	{"S", "[]int", "[]int{}", "[]int{1, 2}", "s = s.Append(9)", true},
	{"M", "Map[int, int]", "Map[int, int]{}", "Map[int, int]{1: 2}", "s[8] = 9", true},
	{"B", "Box", "Box{M: Map[int, int]{}, S: []int{}}", "Box{M: Map[int, int]{3: 4}, S: []int{5}}", "s.M[8] = 9", true},
}

// copyFamily is a family of stores: its prefix, key type, and the Ergo
// text of key n, of the key after the expression k, and of a key's digest.
type copyFamily struct {
	name, pre, kt string
	lit           func(n int) string
	next, digest  string
	moved         bool
}

var copyFamilies = []copyFamily{
	{"plain", "st", "int", func(n int) string { return fmt.Sprint(n) }, "k + 1", "k", false},
	{"moved", "sf", "string", func(n int) string { return fmt.Sprintf("%q", fmt.Sprint(n)) }, `k + "1"`, "len(k)", true},
}

const copyPrelude = `import maps
import slices
import sync

type Box { M Map[int, int], S []int }

type key { s string }

func szI(v int) int { v }

func szS(v []int) int {
	let mut t = len(v) * 10
	for x in v {
		t = t + x
	}
	t
}

func szM(v Map[int, int]) int {
	let mut t = len(v) * 100
	for k, x in v {
		t = t + k * 3 + x
	}
	t
}

func szB(v Box) int { szM(v.M) * 7 + szS(v.S) }

func szMs(v []Map[int, int]) int {
	let mut t = len(v) * 10000
	for i, m in v {
		t = t + (i + 1) * szM(m)
	}
	t
}

// head, cell and upd keep the imports used in every seed.
func head(s []int) []int { slices.Slice(s, 0, 1) }

func cell(m Map[int, int]) sync.Ref[Map[int, int]] { sync.NewRef[Map[int, int]](m) }

func upd(m Map[int, int]) Map[int, int] { maps.Update(m, 1, func(_ Option[int]) int { 1 }) }

// put2 stores v in a copy of m through a type parameter, where a copy
// stops (rmw generic).
func put2[K comparable, V](m Map[K, V], k K, v V) Map[K, V] {
	let mut c = m
	c[k] = v
	c
}

`

// copyStore is the store type of entry e in family f, its constructor,
// its digests, its bump, which writes the root through a borrow, and, in
// the moved family, its find (an Option of an entry) and rmw (a move-out
// through a borrow).
func copyStore(f copyFamily, e copyEntry) string {
	s := fmt.Sprintf(`type %[1]s%[2]s { m Map[%[6]s, %[3]s], s []int, out [][]int }

func new%[1]s%[2]s() %[1]s%[2]s { %[1]s%[2]s{m: Map[%[6]s, %[3]s]{%[7]s: %[4]s, %[8]s: %[5]s}, s: []int{1, 2, 3, 4}, out: [][]int{}} }

func d%[1]s%[2]s(r %[1]s%[2]s) int {
	let mut t = len(r.m) * 1000 + szS(r.s) + len(r.out) * 7
	for k, v in r.m {
		t = t + %[9]s + sz%[2]s(v)
	}
	for o in r.out {
		t = t + szS(o)
	}
	t
}

func dm%[1]s%[2]s(m Map[%[6]s, %[3]s]) int {
	let mut t = len(m) * 1000
	for k, v in m {
		t = t + %[9]s * 3 + sz%[2]s(v)
	}
	t
}

func bump%[1]s%[2]s(r *%[1]s%[2]s, k %[6]s) %[6]s {
	r.m[k] = %[4]s
	k
}

`, f.pre, e.k, e.typ, e.zero, e.sample, f.kt, f.lit(1), f.lit(2), f.digest)
	if f.moved {
		s += fmt.Sprintf(`func find%[1]s%[2]s(m Map[%[5]s, %[3]s], k %[5]s) Option[%[3]s] { m.Get(k) }

func rmw%[1]s%[2]s(r *%[1]s%[2]s, k %[5]s) int {
	let mut s = %[4]s
	match r.m.Get(k) {
	case Some[%[3]s] as v:
		s = v.Value
	case None:
	}
	%[6]s
	r.m[k] = s
	len(r.m)
}

`, f.pre, e.k, e.typ, e.zero, f.kt, e.write)
	}
	return s
}

// copyShapes is the number of shapes copyGen.shape draws from in each
// family.
const copyShapes = 13

type copyGen struct {
	r       *rand.Rand
	fns     strings.Builder
	main    strings.Builder
	model   map[string]int  // function name -> the copies its Go makes; -1: agreement only
	mapOnly map[string]bool // functions whose model counts Map copies only
	order   []string
	reached map[string]bool
	n       int
}

// rmwBody is a move-out of the entry at key, from root r, with the write w
// and the Ergo text between the read and the store.
func rmwBody(e copyEntry, key, between string) string {
	return fmt.Sprintf("\tlet mut s = %s\n\tmatch r.m.Get(%s) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n%s\t%s\n\tr.m[%s] = s\n", e.zero, key, e.typ, between, e.write, key)
}

// shape writes one function, its call in main and its model count.
func (g *copyGen) shape(fam copyFamily, combo int) {
	e := copyEntries[combo%len(copyEntries)]
	ptr := combo/len(copyEntries)%2 == 0
	pick := combo / (2 * len(copyEntries))
	g.n++
	name := fmt.Sprintf("f%d", g.n)
	k := 1 + g.r.IntN(3)
	sz := "sz" + e.k
	st := fam.pre + e.k
	holds := 0
	if e.holds {
		holds = 1
	}
	moved := 0
	if fam.moved {
		moved = holds
	}
	// head opens the function over the store; call is main's call of it,
	// with the store digest after.
	head := fmt.Sprintf("func %s(r *%s, k %s) int {\n", name, st, fam.kt)
	if !ptr {
		head = fmt.Sprintf("func %s(k %s) int {\n\tlet mut r = new%s()\n", name, fam.kt, st)
	}
	call := func() {
		if ptr {
			g.main.WriteString(fmt.Sprintf("\tlet mut r%d = new%s()\n\tConsole.Println(\"\\(%s(&r%d, %s)) \\(d%s(r%d))\")\n", g.n, st, name, g.n, fam.lit(k), st, g.n))
		} else {
			g.main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s(%s))\")\n", name, fam.lit(k)))
		}
	}
	tail := ""
	if !ptr {
		tail = fmt.Sprintf(" + d%s(r)", st)
	}
	match := func(x string) string {
		return fmt.Sprintf("match %s {\n\tcase Some[%s] as v:\n\t\t%s(v.Value)\n\tcase None:\n\t\t0\n\t}", x, e.typ, sz)
	}
	access := map[bool]string{true: "borrow", false: "local"}[ptr]
	var body string
	want := 0
	shape := ""
	switch pick {
	case 0:
		shape = "get keep"
		body = fmt.Sprintf("\tlet o = r.m.Get(k)\n\tr.m[k] = %s\n\tlet n = %s\n\tn%s\n", e.zero, match("o"), tail)
		want = moved // the Option is kept whole: the receiver
	case 1:
		shape = "get match"
		body = fmt.Sprintf("\tlet n = %s\n\tr.m[%s] = %s\n\tn%s\n", match("r.m.Get(k)"), fam.next, e.zero, tail)
		want = moved // the entry the match binds
	case 2:
		shape = "get none"
		body = fmt.Sprintf("\tlet b = r.m.Get(k) == None\n\tr.m[k] = %s\n\tlet n = if b { 1 } else { 2 }\n\tn%s\n", e.zero, tail)
	case 3:
		shape = "get in flight"
		bump := "bump" + st + "(r, k)"
		if !ptr {
			bump = "bump" + st + "(&r, k)"
		}
		body = fmt.Sprintf("\tlet o = r.m.Get(%s)\n\tlet n = %s\n\tn%s\n", bump, match("o"), tail)
		want = 1 // the receiver, before bump writes the root
	case 4:
		shape = "window"
		body = fmt.Sprintf("\tr.out = r.out.Append(slices.Slice(r.s, 1, 3))\n\tr.s[1] = 70\n\tlen(r.out)%s\n", tail)
		want = 1 // the window
	case 5:
		shape = "copy root"
		body = fmt.Sprintf("\tlet mut c = r.m\n\tc[k] = %s\n\tr.s[0] = 9\n\tdm%s(c) + len(r.m)%s\n", e.zero, st, tail)
		want = 1 // the table, or a copy function of the entries
	case 6:
		if !fam.moved {
			shape = "entry copy"
			body = fmt.Sprintf("\tlet mut s = %s\n\tmatch r.m.Get(k) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%s\n\tr.m[%s] = s\n\tlen(r.m)%s\n", e.zero, e.typ, e.write, fam.next, tail)
			want = holds // the entry, which r.m[k] still holds
			break
		}
		shape = "entry rmw"
		body = rmwBody(e, "k", "") + "\tlen(r.m)" + tail + "\n"
		if !ptr {
			// The key a path of fields from an immutable binding.
			body = "\tlet q = key{s: k}\n" + rmwBody(e, "q.s", "") + "\tlen(r.m)" + tail + "\n"
		}
	case 7:
		if !fam.moved {
			shape = "ref modify"
			break
		}
		switch {
		case ptr:
			shape = "rmw root between"
			body = rmwBody(e, "k", "\tlet z = len(r.m)\n") + "\tz" + tail + "\n"
			want = holds // the entry, at its read
		default:
			shape = "rmw kept after"
			body = rmwBody(e, "k", "") + "\tlen(r.m) + " + sz + "(s)" + tail + "\n"
			want = 2 * holds // the entry at its read, and s at the store: s lives on (V1)
		}
	case 8:
		shape = "ref modify"
	case 9:
		// The rebinding hands the Map over, through a let mut or a
		// borrow, and the literal's result is fresh (argsUnshared).
		shape = "update"
		lit := e.zero
		if e.k == "S" {
			// The old entry appended to: the Append's result is
			// fresh, so the Map is not copied (mapOnly).
			lit = "match o {\n\tcase Some[[]int] as v:\n\t\tv.Value.Append(9)\n\tcase None:\n\t\t[]int{}\n\t}"
		}
		g.mapOnly[name] = true
		body = fmt.Sprintf("\tr.m = maps.Update(r.m, k, func(o Option[%s]) %s { %s })\n\tlen(r.m)%s\n", e.typ, e.typ, lit, tail)
	case 11:
		// The same with a top-level callback passed by name, whose
		// result is fresh (namedFunc, freshResult).
		shape = "update named"
		g.fns.WriteString(fmt.Sprintf("func cb%s(_ Option[%s]) %s { %s }\n\n", name, e.typ, e.typ, e.zero))
		g.mapOnly[name] = true
		body = fmt.Sprintf("\tr.m = maps.Update(r.m, k, cb%s)\n\tlen(r.m)%s\n", name, tail)
	case 10:
		// An entry read, written and stored under another key, then read:
		// plain, the store copies nothing (storedAsEntry); moved, it
		// copies, since a move-out may write the stored entry.
		shape = "store read"
		body = fmt.Sprintf("\tlet mut s = %s\n\tmatch r.m.Get(k) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%s\n\tr.m[%s] = s\n\tlen(r.m) + %s(s)%s\n", e.zero, e.typ, e.write, fam.next, sz, tail)
		want = holds + moved
	default:
		g.chain(name, k, &shape, &head, &body, &want, &call)
		access = "value"
	}
	if shape == "ref modify" {
		// A Ref site has no store: the moved family's two draws of it
		// are one of each.
		rmw := pick == 8 && fam.moved && ptr
		head, body, want, call = g.refShape(fam, e, name, k, rmw)
		if rmw {
			shape = "ref rmw"
		}
	}
	switch {
	case shape == "window" || shape == "copy root":
		g.reached[fam.name+" "+shape+" "+access] = true
	case strings.HasPrefix(shape, "chain"):
		g.reached[shape] = true
	case strings.HasPrefix(shape, "ref"):
		g.reached[fam.name+" "+shape+" "+e.k] = true // a Ref site has no store
	default:
		g.reached[fam.name+" "+shape+" "+access+" "+e.k] = true
	}
	g.fns.WriteString(head + body + "}\n\n")
	call()
	g.model[name] = want
	g.order = append(g.order, name)
}

// refShape is a function over a Ref of a Map of e, changing one key in
// sync.Modify's owned callback: by a kept match then a write (rmw false),
// or by a move-out (rmw true, the moved family only).
func (g *copyGen) refShape(fam copyFamily, e copyEntry, name string, k int, rmw bool) (head, body string, want int, call func()) {
	mt := fmt.Sprintf("Map[%s, %s]", fam.kt, e.typ)
	head = fmt.Sprintf("func %s(c sync.Ref[%s], k %s) int {\n", name, mt, fam.kt)
	inner := fmt.Sprintf("\t\tlet n = %s\n\t\tm[k] = %s\n", strings.ReplaceAll(fmt.Sprintf("match m.Get(k) {\n\tcase Some[%s] as v:\n\t\tsz%s(v.Value)\n\tcase None:\n\t\t0\n\t}", e.typ, e.k), "\n", "\n\t\t"), e.sample)
	want = 1 // the cell's copy, made only when its value is shared
	if fam.moved && e.holds {
		want = 2 // and the entry the kept match gives (V9)
	}
	if rmw {
		inner = fmt.Sprintf("\t\tlet mut s = %s\n\t\tmatch m.Get(k) {\n\t\tcase Some[%s] as v:\n\t\t\ts = v.Value\n\t\tcase None:\n\t\t}\n\t\t%s\n\t\tm[k] = s\n\t\tlet n = len(m)\n", e.zero, e.typ, e.write)
		want = 1
	}
	body = fmt.Sprintf("\tsync.Modify(c, func(all %[1]s) (int, %[1]s) {\n\t\tlet mut m = all\n%[2]s\t\t(n, m)\n\t})\n", mt, inner)
	call = func() {
		g.main.WriteString(fmt.Sprintf("\tlet c%d = sync.NewRef[%s](%s{%s: %s})\n\tConsole.Println(\"\\(%s(c%d, %s)) \\(%s(c%d, %s)) \\(dm%s%s(c%d.Get()))\")\n", g.n, mt, mt, fam.lit(1), e.sample, name, g.n, fam.lit(k), name, g.n, fam.lit(k+1), fam.pre, e.k, g.n))
	}
	return
}

// chain writes one of the chains, over a slice or a Map of int.
func (g *copyGen) chain(name string, k int, shape, head, body *string, want *int, call *func()) {
	switch g.r.IntN(5) {
	case 4:
		*shape = "chain root"
		*head = fmt.Sprintf("func %s(x []Map[int, int], a Map[int, int]) int {\n", name)
		*body = "\tlet mut y = x.Append(a).Append(a)\n\ty[0][9] = 9\n\ty[1][9] = 9\n\ty[2][8] = 8\n\tszMs(x) * 3 + szM(a) * 5 + szMs(y)\n"
		*want = -1
		*call = func() {
			g.main.WriteString(fmt.Sprintf("\tlet x%d = []Map[int, int]{Map[int, int]{1: 1}}\n\tlet a%d = Map[int, int]{2: 2}\n\tConsole.Println(\"\\(%s(x%d, a%d)) \\(szMs(x%d)) \\(szM(a%d))\")\n", g.n, g.n, name, g.n, g.n, g.n, g.n))
		}
	case 0:
		*shape = "chain param"
		*head = fmt.Sprintf("func %s(x []int, a int) int {\n", name)
		*body = "\tszS(x.Append(a).Append(a + 1).Append(a + 2))\n"
		*want = 1 // x is shared: the first Append copies it
		*call = func() {
			g.main.WriteString(fmt.Sprintf("\tlet x%d = []int{1, 2}\n\tConsole.Println(\"\\(%s(x%d, %d)) \\(szS(x%d))\")\n", g.n, name, g.n, k, g.n))
		}
	case 1:
		*shape = "chain loop"
		*head = fmt.Sprintf("func %s(n int) int {\n", name)
		*body = "\tlet mut x = []int{}\n\tfor i in 0..n {\n\t\tx = x.Append(i).Append(i * 2)\n\t}\n\tszS(x)\n"
		*want = 0
		*call = func() { g.main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s(%d))\")\n", name, k+3)) }
	case 2:
		*shape = "chain put"
		*head = fmt.Sprintf("func %s(m Map[int, int], a int) int {\n", name)
		*body = "\tszM(m.Put(a, 1).Put(a + 1, 2).Put(a + 2, 3))\n"
		*want = 1
		*call = func() {
			g.main.WriteString(fmt.Sprintf("\tlet m%d = Map[int, int]{1: 1}\n\tConsole.Println(\"\\(%s(m%d, %d)) \\(szM(m%d))\")\n", g.n, name, g.n, k, g.n))
		}
	default:
		*shape = "chain delete"
		*head = fmt.Sprintf("func %s(m Map[int, int], a int) int {\n", name)
		*body = "\tszM(m.Delete(a).Delete(a + 1))\n"
		*want = 1
		*call = func() {
			g.main.WriteString(fmt.Sprintf("\tlet m%d = Map[int, int]{1: 1, 2: 2, 3: 3}\n\tConsole.Println(\"\\(%s(m%d, %d)) \\(szM(m%d))\")\n", g.n, name, g.n, k, g.n))
		}
	}
}

// copyAdversaries are the agreement-only shapes of the moved family.
var copyAdversaries = []string{"rmw snapshot", "rmw kept get", "rmw put shared", "rmw literal shared", "rmw across call", "rmw generic", "rmw option fn", "rmw range", "rmw ref get", "rmw same value", "rmw return between", "update snapshot", "update other", "update shared", "update named shared"}

// adversary writes one agreement-only function of the moved family: a
// value that may share an entry's storage, read after a move-out of it.
func (g *copyGen) adversary(e copyEntry, which string, ptr bool) {
	fam := copyFamilies[1]
	g.n++
	name := fmt.Sprintf("f%d", g.n)
	st := fam.pre + e.k
	sz := "sz" + e.k
	mt := fmt.Sprintf("Map[string, %s]", e.typ)
	head := fmt.Sprintf("func %s(r *%s, k string) int {\n", name, st)
	if !ptr {
		head = fmt.Sprintf("func %s(k string) int {\n\tlet mut r = new%s()\n", name, st)
	}
	tail := ""
	if !ptr {
		tail = fmt.Sprintf(" + d%s(r)", st)
	}
	rmw := rmwBody(e, "k", "")
	var body string
	switch which {
	case "rmw snapshot":
		body = "\tlet snap = r.m\n" + rmw + fmt.Sprintf("\tdm%s(snap) * 3 + dm%s(r.m)%s\n", st, st, tail)
	case "rmw kept get":
		body = "\tlet o = r.m.Get(k)\n" + rmw + fmt.Sprintf("\tlet n = match o {\n\tcase Some[%s] as v:\n\t\t%s(v.Value)\n\tcase None:\n\t\t0\n\t}\n\tn * 3 + dm%s(r.m)%s\n", e.typ, sz, st, tail)
	case "rmw put shared":
		body = fmt.Sprintf("\tlet e = %s\n\tr.m = r.m.Put(k, e)\n", e.sample) + rmw + fmt.Sprintf("\t%s(e) * 3 + dm%s(r.m)%s\n", sz, st, tail)
	case "rmw literal shared":
		if !ptr {
			head, tail = fmt.Sprintf("func %s(k string) int {\n", name), "" // the local q is the root
		}
		body = fmt.Sprintf("\tlet e = %s\n\tlet mut q = %s{k: e}\n\tlet mut s = %s\n\tmatch q.Get(k) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%s\n\tq[k] = s\n\t%s(e) * 3 + dm%s(q)%s\n", e.sample, mt, e.zero, e.typ, e.write, sz, st, tail)
	case "rmw across call":
		p := "r"
		if !ptr {
			p = "&r"
		}
		body = fmt.Sprintf("\tlet o = r.m.Get(k)\n\tlet z = rmw%s(%s, k)\n\tlet n = match o {\n\tcase Some[%s] as v:\n\t\t%s(v.Value)\n\tcase None:\n\t\t0\n\t}\n\tn * 3 + z%s\n", st, p, e.typ, sz, tail)
	case "rmw generic":
		body = fmt.Sprintf("\tlet e = %s\n\tr.m = put2(r.m, k, e)\n", e.sample) + rmw + fmt.Sprintf("\t%s(e) * 3 + dm%s(r.m)%s\n", sz, st, tail)
	case "rmw option fn":
		body = fmt.Sprintf("\tlet o = find%s(r.m, k)\n", st) + rmw + fmt.Sprintf("\tlet n = match o {\n\tcase Some[%s] as v:\n\t\t%s(v.Value)\n\tcase None:\n\t\t0\n\t}\n\tn * 3 + dm%s(r.m)%s\n", e.typ, sz, st, tail)
	case "rmw range":
		body = fmt.Sprintf("\tlet mut t = 0\n\tfor kk, w in r.m {\n\t\tif kk == k {\n%s\t\t}\n\t\tt = t + %s(w)\n\t}\n\tt + dm%s(r.m)%s\n", indent2(rmw), sz, st, tail)
	case "rmw ref get":
		head = fmt.Sprintf("func %s(c sync.Ref[%s], k string) int {\n", name, mt)
		body = fmt.Sprintf("\tlet snap = c.Get()\n\tlet z = sync.Modify(c, func(all %[1]s) (int, %[1]s) {\n\t\tlet mut m = all\n\t\tlet mut s = %[2]s\n\t\tmatch m.Get(k) {\n\t\tcase Some[%[3]s] as v:\n\t\t\ts = v.Value\n\t\tcase None:\n\t\t}\n\t\t%[4]s\n\t\tm[k] = s\n\t\t(len(m), m)\n\t})\n\tdm%[5]s(snap) * 3 + z + dm%[5]s(c.Get())\n", mt, e.zero, e.typ, e.write, st)
		g.fns.WriteString(head + body + "}\n\n")
		g.main.WriteString(fmt.Sprintf("\tlet c%d = sync.NewRef[%s](%s{\"1\": %s, \"2\": %s})\n\tConsole.Println(\"\\(%s(c%d, \"1\"))\")\n", g.n, mt, mt, e.sample, e.sample, name, g.n))
		g.model[name] = -1
		g.order = append(g.order, name)
		g.reached[which+" "+e.k] = true
		return
	case "rmw return between":
		// Through a borrow, so the caller reads the store after the
		// return, which names nothing but X.
		ptr = true
		head, tail = fmt.Sprintf("func %s(r *%s, k string) int {\n", name, st), ""
		body = fmt.Sprintf("\tlet mut s = %s\n\tmatch r.m.Get(k) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%s\n\tif len(k) == 1 {\n\t\treturn %s(s)\n\t}\n\tr.m[k] = s\n\tdm%s(r.m) * 3\n", e.zero, e.typ, e.write, sz, st)
	case "update snapshot":
		body = fmt.Sprintf("\tlet snap = r.m\n\tr.m = maps.Update(r.m, k, func(_ Option[%s]) %s { %s })\n", e.typ, e.typ, e.zero) + rmw + fmt.Sprintf("\tdm%s(snap) * 3 + dm%s(r.m)%s\n", st, st, tail)
	case "update other":
		// Update's result shares r.m's entries (its body copies the
		// table at its type parameters), and the move-out writes one.
		// Through a borrow: nothing writes r.
		ptr = true
		head, tail = fmt.Sprintf("func %s(r *%s, k string) int {\n", name, st), ""
		body = fmt.Sprintf("\tlet mut t = maps.Update(r.m, k + \"z\", func(_ Option[%[1]s]) %[1]s { %[2]s })\n\tlet mut s = %[2]s\n\tmatch t.Get(k) {\n\tcase Some[%[1]s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%[3]s\n\tt[k] = s\n\tdm%[4]s(t) * 3 + dm%[4]s(r.m)%[5]s\n", e.typ, e.zero, e.write, st, tail)
	case "update named shared":
		// A callback by name that returns a package let: not fresh.
		g.fns.WriteString(fmt.Sprintf("let g%s = %s\n\nfunc cb%s(_ Option[%s]) %s { g%s }\n\n", name, e.sample, name, e.typ, e.typ, name))
		body = fmt.Sprintf("\tr.m = maps.Update(r.m, k, cb%s)\n", name) + rmw + fmt.Sprintf("\t%s(g%s) * 3 + dm%s(r.m)%s\n", sz, name, st, tail)
	case "update shared":
		body = fmt.Sprintf("\tlet e = %s\n\tr.m = maps.Update(r.m, k, func(_ Option[%s]) %s { e })\n", e.sample, e.typ, e.typ) + rmw + fmt.Sprintf("\t%s(e) * 3 + dm%s(r.m)%s\n", sz, st, tail)
	case "rmw same value":
		body = fmt.Sprintf("\tlet e = %s\n\tr.m[k] = e\n\tr.m[k + \"x\"] = e\n", e.sample) + rmw + fmt.Sprintf("\t%s(e) * 3 + dm%s(r.m)%s\n", sz, st, tail)
	}
	g.fns.WriteString(head + body + "}\n\n")
	if ptr {
		g.main.WriteString(fmt.Sprintf("\tlet mut r%d = new%s()\n\tConsole.Println(\"\\(%s(&r%d, \"1\")) \\(d%s(r%d))\")\n", g.n, st, name, g.n, st, g.n))
	} else {
		g.main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s(\"2\"))\")\n", name))
	}
	g.model[name] = -1
	g.order = append(g.order, name)
	g.reached[which+" "+e.k] = true
}

// indent2 puts each line of s two tabs deeper.
func indent2(s string) string {
	return strings.ReplaceAll("\t\t"+strings.TrimSuffix(s, "\n"), "\n", "\n\t\t") + "\n"
}

// copyProgram writes the program of seed and the model of its copies.
func copyProgram(seed uint64, reached map[string]bool) (string, *copyGen) {
	g := &copyGen{r: rand.New(rand.NewPCG(seed, 0x636f7079)), model: map[string]int{}, mapOnly: map[string]bool{}, reached: reached}
	// Every family, shape, entry and access once, in the seed's order
	// (copyShapes shapes, the last drawing one of the five chains), then
	// the adversaries, each entry once, at a drawn access.
	type item struct {
		fam   int
		combo int
	}
	var items []item
	for f := range copyFamilies {
		for c := range copyShapes * 2 * len(copyEntries) {
			items = append(items, item{f, c})
		}
	}
	for _, i := range g.r.Perm(len(items)) {
		g.shape(copyFamilies[items[i].fam], items[i].combo)
	}
	for _, which := range copyAdversaries {
		for _, e := range copyEntries {
			if e.holds {
				g.adversary(e, which, g.r.IntN(2) == 0)
			}
		}
	}
	var src strings.Builder
	src.WriteString(copyPrelude)
	for _, f := range copyFamilies {
		for _, e := range copyEntries {
			src.WriteString(copyStore(f, e))
		}
	}
	src.WriteString(g.fns.String())
	src.WriteString("func main() needs Console {\n" + g.main.String() + "}\n")
	return src.String(), g
}

var copyOps = regexp.MustCompile(`\bmaps\.Clone\(|\bslices\.Clone\(|\bslices\.Clip\(|\bergoClone\w*\(`)

// mapCopyOps are the copies of a Map (copyGen.mapOnly).
var mapCopyOps = regexp.MustCompile(`\bmaps\.Clone\(|\bergoCloneMap\w*\(`)

// goFuncsOf is the Go text of the function name: its body, and the body
// of its owned form (values.go, V5) when it has one.
func goFuncsOf(t *testing.T, mainGo, name string) string {
	t.Helper()
	text := goFuncBody(t, mainGo, name)
	if regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + name + `Owned\(`).MatchString(mainGo) {
		text += goFuncBody(t, mainGo, name+"Owned")
	}
	return text
}

// copyPaths are the paths the fixed seeds must reach.
func copyPaths() []string {
	var out []string
	for _, f := range copyFamilies {
		shapes := []string{"get keep", "get match", "get none", "get in flight", "entry copy", "update", "update named", "store read"}
		refs := []string{"ref modify"}
		if f.moved {
			shapes = []string{"get keep", "get match", "get none", "get in flight", "entry rmw", "update", "update named", "store read"}
			refs = append(refs, "ref rmw")
		}
		for _, e := range copyEntries {
			for _, s := range shapes {
				for _, a := range []string{"borrow", "local"} {
					out = append(out, f.name+" "+s+" "+a+" "+e.k)
				}
			}
			for _, s := range refs {
				out = append(out, f.name+" "+s+" "+e.k)
			}
		}
		if f.moved {
			for _, e := range copyEntries {
				out = append(out, f.name+" rmw root between borrow "+e.k, f.name+" rmw kept after local "+e.k)
			}
		}
		for _, s := range []string{"window", "copy root"} {
			for _, a := range []string{"borrow", "local"} {
				out = append(out, f.name+" "+s+" "+a)
			}
		}
	}
	for _, e := range copyEntries {
		if e.holds {
			for _, w := range copyAdversaries {
				out = append(out, w+" "+e.k)
			}
		}
	}
	out = append(out, "chain param", "chain loop", "chain put", "chain delete", "chain root")
	return out
}

// plainMapCopy matches a generated copy function of a plain family's Map
// (int keys): under V8 a plain Map is copied as a table.
var plainMapCopy = regexp.MustCompile(`(?m)^func ergoClone\w*\(v map\[int\]`)

// TestCopyCountsMatchModel is the copy-count property (above), in process
// on the lowered Go.
func TestCopyCountsMatchModel(t *testing.T) {
	reached := map[string]bool{}
	for _, seed := range copySeeds() {
		src, g := copyProgram(seed, reached)
		p := mustLower(t, fmt.Sprintf("copycount%d", seed), src)
		mainGo := p.mainGo()
		for _, name := range g.order {
			if g.model[name] < 0 {
				continue // read by the agreement only
			}
			text := goFuncsOf(t, mainGo, name)
			ops := copyOps
			if g.mapOnly[name] {
				ops = mapCopyOps
			}
			if got := len(ops.FindAllString(text, -1)); got != g.model[name] {
				t.Errorf("seed %d, %s: the lowering makes %d copies, the model %d\nReplay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=%d\n--- Go\n%s\n--- source\n%s", seed, name, got, g.model[name], seed, text, src)
			}
		}
		if m := plainMapCopy.FindString(mainGo); m != "" {
			t.Errorf("seed %d: a plain Map is copied entry by entry (%s)\nReplay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=%d\n%s", seed, m, seed, src)
		}
	}
	if *copySeed == 0 {
		for _, path := range copyPaths() {
			if !reached[path] {
				t.Errorf("the fixed seeds never reach %q", path)
			}
		}
	}
}

// TestGoGetsEntryCopies: a Map whose entries hold slices goes to Go as a
// copy of each entry (V8, cloneDeep), since Go may write an entry two Ergo
// Maps share; the same Map's Ergo copy is a table. The promised fact is the
// shape: Go's operand goes through a copy function that ranges over the
// entries. Mutant: cloneDeep copies the table only.
func TestGoGetsEntryCopies(t *testing.T) {
	src := "import go net/url\n\nfunc enc(m Map[string, []string]) string needs go.Foreign {\n\tlet mut c = m\n\tc[\"b\"] = []string{\"2\"}\n\tlet s = url.Values(c).Encode()\n\ts + \"\\(len(c))\"\n}\n\n" +
		"func main() needs Console | go.Foreign {\n\tConsole.Println(enc(Map[string, []string]{\"a\": []string{\"1\"}}))\n}\n"
	mainGo := mustLower(t, "goentries", src).mainGo()
	call := regexp.MustCompile(`(ergoClone\w*)\(url\.Values\(c\)\)\.Encode\(\)`).FindStringSubmatch(mainGo)
	if call == nil {
		t.Fatalf("Go's operand is not copied by a copy function:\n%s", mainGo)
	}
	if body := goFuncBody(t, mainGo, call[1]); !strings.Contains(body, "range") || !strings.Contains(body, "slices.Clone(") {
		t.Errorf("Go's operand is not copied entry by entry:\n%s", body)
	}
}

// TestCopyRuleAgrees runs the copy-count programs under the rules and
// under their reference (copyLowering "whole", the rule before V8 and V9):
// both print the same lines.
//
// It builds Go because the question is what the programs print at run
// time, which only a run of both lowerings shows.
func TestCopyRuleAgrees(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed uint64
		src  string
		rule *lowered
		ref  *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range copySeeds() {
		src, _ := copyProgram(seed, map[string]bool{})
		rule := mustLower(t, fmt.Sprintf("copyrule%d", seed), src)
		saved := copyLowering
		copyLowering = "whole"
		ref := mustLower(t, fmt.Sprintf("copywhole%d", seed), src)
		copyLowering = saved
		runs = append(runs, run{seed, src, rule, ref})
		programs = append(programs, rule, ref)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the copy programs do not build")
	}
	for _, r := range runs {
		got, gerr, gcode := runExe(t, exes[r.rule.name])
		want, werr, wcode := runExe(t, exes[r.ref.name])
		if got != want || gcode != wcode || gcode != 0 {
			t.Errorf("seed %d: the rule and the reference differ\nReplay: go test -run '^TestCopyRuleAgrees$' ./internal/sketch/lower -args -copy.seed=%d\n%s\n--- rule (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- diff\n%s", r.seed, r.seed, r.src, gcode, got, gerr, wcode, want, werr, diffLines(got, want))
		}
	}
}
