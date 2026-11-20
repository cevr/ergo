package irspike

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// intMapFuncs are GenerateIntMap's functions whose Maps the IR lowers to
// the runtime's IntMap (MI-2): each has an IR body in every seed, and
// that body calls the table's methods.
var intMapFuncs = []string{"steps", "small", "flags", "boxed", "copied"}

// intMapCallFuncs are GenerateIntMap's functions that pass an int-keyed
// Map to a generic function: each has an IR body, with its Map Go's map.
var intMapCallFuncs = []string{"seen", "throughStd", "throughUser"}

// GenerateIntMap is a program over Maps with an integer key (MI-2): seeded
// steps of Put (m[k] = v and m.Put), Delete and Get on a Map[int, int]
// (steps), on a Map[int8, string] whose keys wrap (small) and on a
// Map[uint16, bool] (flags); a Map in a record field written through the
// record (boxed); and a value copy written after it is taken, which must
// not reach the original (copied). One function iterates its own Map
// type (walked): the IR does not iterate an int map, so that type is Go's
// map in the whole program (the lowering's conflict), and the function
// prints an order-free sum. main prints each
// function's digest: a lost entry, a wrong value, a length, or a copy
// that shares storage changes the output.
func GenerateIntMap(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x1a77))
	var b strings.Builder
	key := func(lo, n int) string { return fmt.Sprint(lo + r.IntN(n)) }
	ops := func(m, k func() string, v func(int) string, n int) string {
		var s strings.Builder
		for i := 0; i < n; i++ {
			switch r.IntN(5) {
			case 0, 1:
				fmt.Fprintf(&s, "\t%s[%s] = %s\n", m(), k(), v(i))
			case 2:
				fmt.Fprintf(&s, "\t%s = %s.Put(%s, %s)\n", m(), m(), k(), v(i))
			case 3:
				fmt.Fprintf(&s, "\t%s = %s.Delete(%s)\n", m(), m(), k())
			default:
				fmt.Fprintf(&s, "\tsum = sum * 31 + look(%s, %s)\n", m(), k())
			}
		}
		return s.String()
	}
	m := func() string { return "m" }
	b.WriteString("import maps\nimport slices\nimport go gomaps maps\n\n")
	b.WriteString("type Box { index Map[int, int], name string }\n\n")
	b.WriteString("func look(m Map[int, int], k int) int {\n\tmatch m.Get(k) {\n\tcase Some[int] as v:\n\t\tv.Value\n\tcase None:\n\t\t-7\n\t}\n}\n\n")
	b.WriteString("func steps() int {\n\tlet mut m = Map[int, int]{}\n\tlet mut sum = 0\n")
	b.WriteString(ops(m, func() string { return key(-20, 60) }, func(i int) string { return fmt.Sprint(i*7 - 3) }, 30+r.IntN(30)))
	b.WriteString("\tsum * 1000 + len(m)\n}\n\n")
	b.WriteString("func smallLook(m Map[int8, string], k int8) string {\n\tmatch m.Get(k) {\n\tcase Some[string] as v:\n\t\tv.Value\n\tcase None:\n\t\t\"-\"\n\t}\n}\n\n")
	b.WriteString("func small() string {\n\tlet mut m = Map[int8, string]{}\n\tlet mut out = \"\"\n")
	for i := 0; i < 20+r.IntN(20); i++ {
		k := fmt.Sprint(r.IntN(256) - 128)
		switch r.IntN(3) {
		case 0:
			fmt.Fprintf(&b, "\tm = m.Delete(%s)\n", k)
		default:
			fmt.Fprintf(&b, "\tm[%s] = \"v%d\"\n", k, i)
		}
		fmt.Fprintf(&b, "\tout = out + smallLook(m, %s)\n", fmt.Sprint(r.IntN(256)-128))
	}
	b.WriteString("\tout + \"/\\(len(m))\"\n}\n\n")
	b.WriteString("func flagged(m Map[uint16, bool], k uint16) bool {\n\tmatch m.Get(k) {\n\tcase Some[bool] as v:\n\t\tv.Value\n\tcase None:\n\t\tfalse\n\t}\n}\n\n")
	b.WriteString("func flags(n int) int {\n\tlet mut m = Map[uint16, bool]{}\n\tlet mut hits = 0\n\tfor i in 0..n {\n\t\tm[uint16(i * 37 % 1000)] = i % 3 == 0\n\t}\n")
	fmt.Fprintf(&b, "\tfor i in 0..%d {\n\t\tif flagged(m, uint16(i)) {\n\t\t\thits += 1\n\t\t}\n\t}\n\thits * 10000 + len(m)\n}\n\n", 1000)
	b.WriteString("func boxed() int {\n\tlet mut b = Box{index: Map[int, int]{}, name: \"b\"}\n\tlet mut sum = 0\n")
	b.WriteString(ops(func() string { return "b.index" }, func() string { return key(0, 12) }, func(i int) string { return fmt.Sprint(i) }, 10+r.IntN(10)))
	b.WriteString("\tsum * 100 + len(b.index)\n}\n\n")
	b.WriteString("func copied() int {\n\tlet mut m = Map[int, int]{}\n\tlet mut sum = 0\n")
	b.WriteString(ops(m, func() string { return key(0, 10) }, func(i int) string { return fmt.Sprint(i + 1) }, 8+r.IntN(8)))
	b.WriteString("\tlet kept = m\n")
	b.WriteString(ops(m, func() string { return key(0, 10) }, func(i int) string { return fmt.Sprint(100 + i) }, 8+r.IntN(8)))
	// other is m with one more entry, a write m does not see (V3).
	b.WriteString("\tlet other = m.Put(99, 5)\n")
	b.WriteString("\tlet mut seen = look(other, 99) * 10 + look(m, 99)\n\tfor k in 0..10 {\n\t\tseen = seen * 7 + look(kept, k) - look(m, k)\n\t}\n\tseen * 1000 + sum + len(kept) * 10 + len(m) + len(other)\n}\n\n")
	// walked iterates its Map, which the IR does not do on an int map:
	// the lowering's conflict makes Map[int64, int] Go's map in the whole
	// program, and the output is an order-free sum.
	b.WriteString("func walked(n int) int {\n\tlet mut m = Map[int64, int]{}\n\tfor i in 0..n {\n\t\tm[int64(i * 13 % 40)] = i\n\t}\n\tlet mut s = 0\n\tfor k, v in m {\n\t\ts += int(k) * v\n\t}\n\ts * 100 + len(m)\n}\n\n")
	// The through functions pass an int-keyed Map to a generic Ergo
	// function, whose Go signature (and its monomorphic copy's) spells
	// Go's map (INTMAP-CALL): seen's Map[int, bool] through std's
	// maps.GetOr (modules/work memory/cloner.ergo), a Map[int32, int]
	// through each std maps function, and a Map[uint32, int] through a
	// user generic. Each Map type is Go's map in the whole program (the
	// call's conflict), and each function keeps its IR body.
	b.WriteString("func seen(xs []int) int {\n\tlet mut s = Map[int, bool]{}\n\tlet mut n = 0\n\tfor x in xs {\n\t\tif maps.GetOr(s, x, false) {\n\t\t\tcontinue\n\t\t}\n\t\ts[x] = true\n\t\tn += 1\n\t}\n\tn * 1000 + len(s)\n}\n\n")
	b.WriteString("func throughStd(n int) int {\n\tlet mut m = Map[int32, int]{}\n\tfor i in 0..n {\n\t\tm[int32(i * 7 % 23)] = i\n\t}\n")
	b.WriteString("\tlet c = maps.Clone(m)\n")
	b.WriteString("\tlet u = maps.Update(c, int32(5), func(old Option[int]) int {\n\t\tmatch old {\n\t\tcase Some[int] as v:\n\t\t\tv.Value + 1\n\t\tcase None:\n\t\t\t100\n\t\t}\n\t})\n")
	b.WriteString("\tlet f = maps.Filter(u, func(k int32, v int) bool { v % 2 == 0 })\n")
	b.WriteString("\tlet t = maps.TransformValues(f, func(v int) int { v * 3 })\n")
	b.WriteString("\tlet g = maps.Merge(t, m)\n")
	b.WriteString("\tlet mut s = maps.GetOr(g, int32(5), -1) * 1000 + len(c) + len(u) * 10 + len(f) * 100 + len(t) + len(g)\n")
	b.WriteString("\tfor k in slices.Sorted(maps.Keys(g)) {\n\t\ts = s * 31 + int(k) + maps.GetOr(g, k, 0)\n\t}\n\ts\n}\n\n")
	b.WriteString("func sizeOf[K comparable, V](m Map[K, V], k K) int {\n\tmatch m.Get(k) {\n\tcase Some[V]:\n\t\tlen(m) * 2 + 1\n\tcase None:\n\t\tlen(m) * 2\n\t}\n}\n\n")
	b.WriteString("func throughUser(n int) int {\n\tlet mut m = Map[uint32, int]{}\n\tlet mut s = 0\n\tfor i in 0..n {\n\t\tm[uint32(i * 5 % 17)] = i\n\t\ts = s * 7 + sizeOf(m, uint32(i % 9))\n\t}\n\ts\n}\n\n")
	// throughGo passes a Map[uint8, int] to Go's own maps package, a
	// function of a Go package: both paths must print the same. Its body
	// has no IR yet ("call of a Go package function": hostCall refuses
	// maps.Clone), so it is not in intMapCallFuncs and hostCall's
	// intMapCall has no killing row until it does (INTMAP-CALL-2).
	b.WriteString("func throughGo(n int) int needs go.Foreign {\n\tlet mut m = Map[uint8, int]{}\n\tfor i in 0..n {\n\t\tm[uint8(i * 3 % 50)] = i\n\t}\n")
	b.WriteString("\tlet mut c = gomaps.Clone(m)\n\tc[uint8(1)] = 999\n")
	b.WriteString("\tlet mut s = len(c)\n\tfor k in 0..50 {\n\t\tmatch c.Get(uint8(k)) {\n\t\tcase Some[int] as v:\n\t\t\ts = s * 31 + v.Value\n\t\tcase None:\n\t\t\ts = s * 7 + 1\n\t\t}\n\t}\n")
	b.WriteString("\ts\n}\n\n")
	xs := make([]string, 5+r.IntN(20))
	for i := range xs {
		xs[i] = fmt.Sprint(r.IntN(12))
	}
	fmt.Fprintf(&b, "func main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(steps()) \\(small()) \\(flags(%d)) \\(boxed()) \\(copied()) \\(walked(%d)) \\(seen([]int{%s})) \\(throughStd(%d)) \\(throughUser(%d)) \\(throughGo(%d))\")\n}\n", 50+r.IntN(400), 10+r.IntN(60), strings.Join(xs, ", "), 5+r.IntN(40), 5+r.IntN(40), 5+r.IntN(60))
	return b.String()
}
