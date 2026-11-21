package lower

import "testing"

// BenchmarkStdCollections is std-spike D1's hand-Go control: over the same
// 4,096 ints, Ergo's slices.GroupBy into 16 keys, slices.FilterMap and a
// set.Of then set.Has of every element, against the Go loops a programmer
// writes (a map of slices, an append in a loop, a map[int]struct{}).
// Reported either way.
func BenchmarkStdCollections(b *testing.B) {
	const src = `import set
import slices

func Groups(xs []int) int {
	let g = slices.GroupBy(xs, func(x int) int { x % 16 })
	len(g)
}

func Picks(xs []int) int {
	len(slices.FilterMap(xs, func(x int) Option[int] { if x%3 == 0 { Some[int]{Value: x * 2} } else { None } }))
}

func Members(xs []int) int {
	let s = set.Of(xs)
	let mut n = 0
	for x in xs {
		if set.Has(s, x + 1) {
			n = n + 1
		}
	}
	n
}

func main() needs Console {
	Console.Println("\(Groups([]int{1, 2})) \(Picks([]int{3})) \(Members([]int{1, 2}))")
}
`
	gen := func(mainGo string) string {
		return `package main

import "testing"

var xs = func() []int {
	out := make([]int, 4096)
	for i := range out {
		out[i] = i * 7919 % 10007
	}
	return out
}()

var sink int

func BenchmarkGroupByErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Groups", "xs") + `
	}
}

func BenchmarkGroupByGo(b *testing.B) {
	for range b.N {
		g := map[int][]int{}
		for _, x := range xs {
			g[x%16] = append(g[x%16], x)
		}
		sink = len(g)
	}
}

func BenchmarkFilterMapErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Picks", "xs") + `
	}
}

func BenchmarkFilterMapGo(b *testing.B) {
	for range b.N {
		var out []int
		for _, x := range xs {
			if x%3 == 0 {
				out = append(out, x*2)
			}
		}
		sink = len(out)
	}
}

func BenchmarkSetErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Members", "xs") + `
	}
}

func BenchmarkSetGo(b *testing.B) {
	for range b.N {
		s := map[int]struct{}{}
		for _, x := range xs {
			s[x] = struct{}{}
		}
		n := 0
		for _, x := range xs {
			if _, ok := s[x+1]; ok {
				n++
			}
		}
		sink = n
	}
}

func TestCollectionsAgree(t *testing.T) {
	g := map[int][]int{}
	n := 0
	s := map[int]struct{}{}
	for _, x := range xs {
		g[x%16] = append(g[x%16], x)
		if x%3 == 0 {
			n++
		}
		s[x] = struct{}{}
	}
	m := 0
	for _, x := range xs {
		if _, ok := s[x+1]; ok {
			m++
		}
	}
	if a, b, c := ` + emittedCall(mainGo, "Groups", "xs") + `, ` + emittedCall(mainGo, "Picks", "xs") + `, ` + emittedCall(mainGo, "Members", "xs") + `; a != len(g) || b != n || c != m {
		t.Fatalf("Ergo %d %d %d, Go %d %d %d", a, b, c, len(g), n, m)
	}
}
`
	}
	emittedBench(b, "std_collections", src, gen, "GroupByErgo", "GroupByGo", "FilterMapErgo", "FilterMapGo", "SetErgo", "SetGo")
}
