package lower

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
)

// eq-structural (G-ex-d-5, types.md Operators 1): == and != on records,
// unions and tuples that hold slices or Maps compare element by element;
// a Map key and a comparable constraint keep Go's comparability.

const eqDecls = `type Value = Leaf{N int} | Branch{Items []Value, Tags Map[string, int]}

type Pair { A Value, B []int }

type Box[T] { Items []T }

func same[T comparable](a Box[T], b Box[T]) bool { a == b }
`

// TestErgoRunStructuralEquality is the unit's repro: a recursive union
// over a slice and a Map, a record with such a field, a tuple, a generic
// record compared inside a generic function, and an empty slice against
// an omitted (Go nil) one.
func TestErgoRunStructuralEquality(t *testing.T) {
	needsBuild(t)
	wantRun(t, eqDecls+`
func main() needs Console {
	let a Value = Branch{Items: []Value{Leaf{N: 1}, Branch{Items: []Value{}, Tags: Map[string, int]{"x": 1}}}, Tags: Map[string, int]{}}
	let b Value = Branch{Items: []Value{Leaf{N: 1}, Branch{Tags: Map[string, int]{"x": 1}}}, Tags: Map[string, int]{}}
	let c Value = Branch{Items: []Value{Leaf{N: 2}}, Tags: Map[string, int]{}}
	let p = Pair{A: a, B: []int{2}}
	let q = Pair{A: b, B: []int{2}}
	let r = Pair{A: b, B: []int{3}}
	Console.Println("\(a == b) \(a != c) \(a == c) \(p == q) \(p == r) \((a, 1) == (b, 1)) \((a, 1) == (c, 1))")
	Console.Println("\(same(Box[int]{Items: []int{1}}, Box[int]{Items: []int{1}})) \(same(Box[int]{}, Box[int]{Items: []int{}})) \(same(Box[int]{Items: []int{1}}, Box[int]{Items: []int{2}}))")
	Console.Println("\(Branch{Items: []Value{Leaf{N: 2}}, Tags: Map[string, int]{}} == c) \(c != Branch{Tags: Map[string, int]{}}) \(Leaf{N: 2} == c)")
	Console.Println("\(Map[string, int]{"a": 0} == Map[string, int]{"b": 0}) \(Map[string, int]{"a": 1} == Map[string, int]{"a": 1}) \([]int{} == []int{0})")
}
`, "true true false true false true false\ntrue true false\ntrue true false\nfalse true false\n")
}

// TestStructuralEqualityRefusals: comparability is still Go's where a Map
// key, a comparable constraint or a type set reads it, and a function and a
// row value have no equality.
func TestStructuralEqualityRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"map-key":    {eqDecls + "func f(m Map[Value, int]) int { len(m) }\n", "map key type Value is not comparable"},
		"constraint": {eqDecls + "func eq[T comparable](a T, b T) bool { a == b }\nfunc f(a Value) bool { eq(a, a) }\n", "Value is not comparable"},
		"function":   {"func f(g escaping func() int) bool { g == g }\n", "func() int values cannot be compared with =="},
		"row-value":  {"type E { N []int }\ntype F {}\nfunc g() int fails E | F { 1 }\nfunc f() int { g() catch { case err: if (err, 1) == (err, 1) { 1 } else { 0 } } }\n", "(E | F, int) values cannot be compared with =="},
	} {
		t.Run(name, func(t *testing.T) {
			o := lowerSource("refuse"+name, tc.src+"\nfunc main() {}\n", true)
			found := false
			for _, d := range o.diags {
				found = found || d.Code == "S108" && strings.Contains(d.Message, tc.want)
			}
			if !found {
				t.Fatalf("want S108 %q; got %v (err %v)\n%s", tc.want, o.diags, o.err, tc.src)
			}
		})
	}
}

var eqSeed = flag.Uint64("eq.seed", 0, "run TestEqualStructuralAgrees on this seed alone")

// eqSeeds are the fixed seeds; each writes one program of eqPairs pairs.
var eqSeeds = []uint64{1, 2, 3}

const eqPairs = 60

// eqValue is the test's model of a Value: a Leaf (items == nil, leaf
// set) or a Branch.
type eqValue struct {
	leaf      bool
	n         int
	items     []*eqValue
	omitItems bool // written with Items omitted: Go's nil slice
	tags      map[string]int
}

func (g *eqGen) value(depth int) *eqValue {
	if depth == 0 || g.r.IntN(3) == 0 {
		return &eqValue{leaf: true, n: g.r.IntN(3)}
	}
	v := &eqValue{tags: map[string]int{}}
	for range g.r.IntN(4) {
		v.items = append(v.items, g.value(depth-1))
	}
	for range g.r.IntN(3) {
		v.tags[string(rune('a'+g.r.IntN(3)))] = g.r.IntN(3)
	}
	v.omitItems = len(v.items) == 0 && g.r.IntN(2) == 0
	return v
}

func (v *eqValue) clone() *eqValue {
	c := *v
	c.items = nil
	for _, it := range v.items {
		c.items = append(c.items, it.clone())
	}
	if v.tags != nil {
		c.tags = map[string]int{}
		for k, x := range v.tags {
			c.tags[k] = x
		}
	}
	return &c
}

// nodes is every node of v, v first.
func (v *eqValue) nodes() []*eqValue {
	out := []*eqValue{v}
	for _, it := range v.items {
		out = append(out, it.nodes()...)
	}
	return out
}

// mutate changes one node of v in one way.
func (g *eqGen) mutate(v *eqValue) {
	all := v.nodes()
	n := all[g.r.IntN(len(all))]
	switch k := g.r.IntN(6); {
	case n.leaf && k < 3:
		n.n += 1 + g.r.IntN(2)
	case n.leaf:
		*n = eqValue{tags: map[string]int{}} // a Branch with nothing
	case k == 0 && len(n.items) > 0:
		n.items = n.items[:len(n.items)-1]
	case k == 1:
		n.items = append(n.items, g.value(1))
	case k == 2 && len(n.items) > 0:
		n.items[0] = g.value(1)
	case k == 3:
		n.tags[string(rune('a'+g.r.IntN(4)))] = 7
	case k == 4 && len(n.tags) > 0:
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range n.tags {
				if !yield(k) {
					return
				}
			}
		})
		delete(n.tags, keys[0])
	case k == 5 && len(n.tags) > 0:
		// The same size, a key renamed: b lacks a's key, whose value
		// Go's zero read of b would match when it is 0.
		keys := slices.Sorted(maps.Keys(n.tags))
		key := keys[0]
		for _, c := range keys {
			if n.tags[c] == 0 {
				key = c
			}
		}
		n.tags["z"+key] = n.tags[key]
		delete(n.tags, key)
	default:
		*n = eqValue{leaf: true, n: 9}
	}
	n.omitItems = len(n.items) == 0 && !n.leaf && g.r.IntN(2) == 0
}

func eqModel(a, b *eqValue) bool {
	if a.leaf != b.leaf {
		return false
	}
	if a.leaf {
		return a.n == b.n
	}
	if len(a.items) != len(b.items) || len(a.tags) != len(b.tags) {
		return false
	}
	for i := range a.items {
		if !eqModel(a.items[i], b.items[i]) {
			return false
		}
	}
	for k, x := range a.tags {
		if y, ok := b.tags[k]; !ok || x != y {
			return false
		}
	}
	return true
}

// ergo is v as an Ergo literal of Value's variant.
func (v *eqValue) ergo() string {
	if v.leaf {
		return fmt.Sprintf("Leaf{N: %d}", v.n)
	}
	var items []string
	for _, it := range v.items {
		items = append(items, it.ergo())
	}
	keys := make([]string, 0, len(v.tags))
	for k := range v.tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tags []string
	for _, k := range keys {
		tags = append(tags, fmt.Sprintf("%q: %d", k, v.tags[k]))
	}
	fields := []string{"Tags: Map[string, int]{" + strings.Join(tags, ", ") + "}"}
	if !v.omitItems {
		fields = append([]string{"Items: []Value{" + strings.Join(items, ", ") + "}"}, fields...)
	}
	return "Branch{" + strings.Join(fields, ", ") + "}"
}

type eqGen struct{ r *rand.Rand }

// eqReference is the hand-written Equal of Value, the property's
// reference: a match on both variants, then each item and tag.
const eqReference = `
func sameValue(a Value, b Value) bool {
	match a {
	case Leaf as x:
		match b {
		case Leaf as y:
			x.N == y.N
		case Branch:
			false
		}
	case Branch as x:
		match b {
		case Leaf:
			false
		case Branch as y:
			sameBranch(x, y)
		}
	}
}

func sameBranch(x Branch, y Branch) bool {
	if len(x.Items) != len(y.Items) || len(x.Tags) != len(y.Tags) {
		return false
	}
	for i, v in x.Items {
		if !sameValue(v, y.Items[i]) {
			return false
		}
	}
	for k, v in x.Tags {
		match y.Tags.Get(k) {
		case Some[int] as s:
			if s.Value != v {
				return false
			}
		case None:
			return false
		}
	}
	true
}

func check(i int, a Value, b Value, ka []int, kb []int) string {
	"\(i) \(a == b) \(a != b) \(sameValue(a, b)) \(Pair{A: a, B: ka} == Pair{A: b, B: kb}) \((a, len(ka)) == (b, len(kb)))"
}
`

// TestEqualStructuralAgrees is the generated property of eq-structural:
// over random Value trees, each pair equal, one mutation apart, or written
// with an empty slice against an omitted one, the generated == agrees with
// the model and with the hand-written Equal, != is its negation, and a
// record and a tuple that hold the pair agree too. A failing line names the
// seed and the two values.
// Replay: go test -run '^TestEqualStructuralAgrees$' ./internal/sketch/lower -args -eq.seed=N
func TestEqualStructuralAgrees(t *testing.T) {
	needsBuild(t)
	seeds := eqSeeds
	if *eqSeed != 0 {
		seeds = []uint64{*eqSeed}
	}
	type pair struct {
		a, b   *eqValue
		ka, kb []int
	}
	var programs []*lowered
	pairs := map[uint64][]pair{}
	kinds := map[string]int{}
	for _, seed := range seeds {
		g := &eqGen{r: rand.New(rand.NewPCG(seed, 0x6571))}
		var b strings.Builder
		b.WriteString(eqDecls + eqReference + "\nfunc main() needs Console {\n")
		for i := range eqPairs {
			p := pair{a: g.value(3), ka: []int{g.r.IntN(2)}}
			p.b, p.kb = p.a.clone(), slices.Clone(p.ka)
			switch g.r.IntN(4) {
			case 0:
				kinds["equal"]++
			case 1:
				// The same tree, each empty Items written the other way.
				for _, n := range p.b.nodes() {
					if !n.leaf && len(n.items) == 0 {
						n.omitItems = !n.omitItems
					}
				}
				kinds["nil-vs-empty"]++
			case 2:
				g.mutate(p.b)
				kinds["mutated"]++
			default:
				p.kb = append(p.kb, 1) // the record's slice field differs
				kinds["field"]++
			}
			pairs[seed] = append(pairs[seed], p)
			fmt.Fprintf(&b, "\tConsole.Println(check(%d, %s, %s, []int{%s}, []int{%s}))\n", i, p.a.ergo(), p.b.ergo(), ints(p.ka), ints(p.kb))
		}
		b.WriteString("}\n")
		programs = append(programs, mustLower(t, fmt.Sprintf("eq%d", seed), b.String()))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the equality programs do not build (seeds %v)", seeds)
	}
	unequal := 0
	for k, seed := range seeds {
		stdout, stderr, code := runExe(t, exes[programs[k].name])
		if code != 0 {
			t.Fatalf("seed %d: exit %d\n%s", seed, code, stderr)
		}
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(lines) != eqPairs {
			t.Fatalf("seed %d: %d lines\n%s", seed, len(lines), stdout)
		}
		for i, p := range pairs[seed] {
			eq := eqModel(p.a, p.b)
			rec := eq && slices.Equal(p.ka, p.kb)
			tup := eq && len(p.ka) == len(p.kb)
			if !eq {
				unequal++
			}
			want := fmt.Sprintf("%d %v %v %v %v %v", i, eq, !eq, eq, rec, tup)
			if lines[i] != want {
				t.Errorf("seed %d, pair %d: got %q, want %q (index eq ne reference record tuple)\nReplay: go test -run '^TestEqualStructuralAgrees$' ./internal/sketch/lower -args -eq.seed=%d\na = %s\nb = %s\nka = %v, kb = %v", seed, i, lines[i], want, seed, p.a.ergo(), p.b.ergo(), p.ka, p.kb)
			}
		}
	}
	if *eqSeed == 0 && (unequal == 0 || len(kinds) != 4) {
		t.Errorf("the fixed seeds reach %v and %d unequal pairs; want every kind and some unequal", kinds, unequal)
	}
	t.Logf("seeds %v: %d pairs (%v), %d unequal", seeds, eqPairs*len(seeds), kinds, unequal)
}

func ints(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}
