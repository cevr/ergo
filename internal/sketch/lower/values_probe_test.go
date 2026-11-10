package lower

import "testing"

// The value rule's probes (value-rule; owner q26; the map-alias table of
// batch 11). Each program writes a Map or a slice through a `let mut` root
// that another binding, field, element, argument, capture or task also
// reaches, or calls Delete or Append on a value that is not dead; the
// value rule says the write is seen only through its root. The test uses
// only the package's long-standing helpers, so it runs unchanged at a base
// without the rule, where each probe prints the shared result (red).
var valueProbes = []struct {
	name, src, want string
}{
	{"P1package", `let calls = Map[int, int]{}

func next(n int) int {
	let mut c = calls
	c[len(c)] = n
	len(c)
}

func main() needs Console {
	let a = next(7)
	let b = next(7)
	Console.Println("\(a) \(b) \(len(calls))")
}
`, "1 1 0\n"},
	{"P2local", `func main() needs Console {
	let m = Map[int, int]{}
	let mut c = m
	c[1] = 1
	let s = []int{1, 2, 3}
	let mut t = s
	t[0] = 9
	Console.Println("\(len(m)) \(s[0]) \(len(c)) \(t[0])")
}
`, "0 1 1 9\n"},
	{"P3field", `type Box { M Map[int, int], S []int }

func main() needs Console {
	let b = Box{M: Map[int, int]{}, S: []int{1}}
	let mut c = b.M
	c[1] = 1
	let mut t = b.S
	t[0] = 9
	let mut whole = b
	whole.S[0] = 7
	whole.M[2] = 2
	Console.Println("\(len(b.M)) \(b.S[0]) \(len(whole.M)) \(whole.S[0])")
}
`, "0 1 1 7\n"},
	{"P4argument", `func put(m Map[int, int], s []int) int {
	let mut c = m
	c[len(c)] = 1
	let mut t = s
	t[0] = 9
	len(c)
}

func main() needs Console {
	let m = Map[int, int]{}
	let s = []int{1}
	let a = put(m, s)
	let b = put(m, s)
	Console.Println("\(a) \(b) \(len(m)) \(s[0])")
}
`, "1 1 0 1\n"},
	{"P5closure", `func main() needs Console {
	let m = Map[int, int]{}
	let f = func(n int) int {
		let mut c = m
		c[len(c)] = n
		len(c)
	}
	let a = f(1)
	let b = f(1)
	Console.Println("\(a) \(b) \(len(m))")
}
`, "1 1 0\n"},
	{"P6tasks", `import task

func fill(m Map[int, int], base int) int {
	let mut c = m
	for i in 0..2000 {
		c[base+i] = i
	}
	len(c)
}

func main() needs Console {
	let m = Map[int, int]{}
	let a = task.Go() { fill(m, 0) }
	let b = task.Go() { fill(m, 100000) }
	let x = a.Wait()
	let y = b.Wait()
	Console.Println("\(x) \(y) \(len(m))")
}
`, "2000 2000 0\n"},
	{"P7slice", `func main() needs Console {
	let ms = []Map[int, int]{Map[int, int]{}}
	let mut c = ms[0]
	c[1] = 1
	let mut all = ms
	all[0][2] = 2
	Console.Println("\(len(ms[0])) \(len(c)) \(len(all[0]))")
}
`, "0 1 1\n"},
	{"P7delete", `func main() needs Console {
	let m = Map[int, int]{1: 1}
	let d = m.Delete(1)
	let mut n = Map[int, int]{1: 1, 2: 2}
	n = n.Delete(1)
	Console.Println("\(len(m)) \(len(d)) \(len(n))")
}
`, "1 0 1\n"},
	{"P7append", `import slices

func main() needs Console {
	let grow = slices.WithCapacity[int](4).Append(1)
	let a = grow.Append(2)
	let b = grow.Append(3)
	Console.Println("\(a[1]) \(b[1]) \(len(grow))")
}
`, "2 3 1\n"},
	{"Pkept", `func main() needs Console {
	let mut xs = []int{1, 2}
	let kept = xs
	xs[0] = 9
	let mut m = Map[int, int]{}
	let mut ys = []Map[int, int]{}
	for i in 0..3 {
		ys = ys.Append(m)
		m[i] = i
	}
	Console.Println("\(kept[0]) \(xs[0]) \(len(ys[0])) \(len(ys[2])) \(len(m))")
}
`, "1 9 0 2 3\n"},
	{"Pborrow", `type Box { M Map[int, int] }

func add(b *Box, k int) {
	b.M[k] = k
}

func leak(b *Box) Map[int, int] {
	b.M
}

func main() needs Console {
	let mut b = Box{M: Map[int, int]{}}
	add(&b, 1)
	let l = leak(&b)
	add(&b, 2)
	Console.Println("\(len(l)) \(len(b.M))")
}
`, "1 2\n"},
}

// TestValueRuleProbes runs every probe: each prints what value semantics
// says. They are the smallest repros of the map-alias bug, one per path,
// each red at f719e9f1. They build and run because the bug is Go's map
// and slice sharing at run time, which no in-process check of the emitted
// text observes; the generated programs of TestValueRuleAgrees cover the
// paths in combination.
//
// It builds Go because the bug is Go's map and slice sharing at run time
// (above).
func TestValueRuleProbes(t *testing.T) {
	needsBuild(t)
	var programs []*lowered
	for _, p := range valueProbes {
		programs = append(programs, mustLower(t, "vp"+p.name, p.src))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the probes do not build")
	}
	for _, p := range valueProbes {
		out, stderr, code := runExe(t, exes["vp"+p.name])
		if out != p.want || code != 0 {
			t.Errorf("%s: got %q, want %q (exit %d)\n%s\n%s", p.name, out, p.want, code, stderr, p.src)
		}
	}
}
