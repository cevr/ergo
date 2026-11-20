package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// shortcutWitness is, per registered shortcut (shortcuts.go), a program
// whose run build takes it at least once, with a test, so that its test
// build lowers the same sites.
var shortcutWitness = map[string]string{
	"fact-range":    factsWitness,
	"fact-index":    factsWitness,
	"fact-cut":      factsWitness,
	"fact-disjoint": factsWitness,
	"fact-pure":     factsWitness,
	"bounds-unchecked": `func at(s string, i int) int {
	let mut acc = 0
	if 0 <= i && i <= len(s)-1 {
		let r = s[i]
		acc = int(r)
	}
	acc
}

func main() needs Console {
	Console.Println("\(at("ab", 1))")
}

test At {
	assert at("ab", 1) == 98
}
`,
	"host-reads": `import go sort

func find(xs []int, n int) int needs go.Foreign {
	sort.SearchInts(xs, n) + len(xs)
}

func main() needs Console | go.Foreign {
	Console.Println("\(find([]int{1, 3, 5}, 3))")
}
`,
	"host-view": `import go hash/crc32
import strings

func sum(s string) uint32 {
	crc32.ChecksumIEEE(strings.Bytes(s))
}

func main() needs Console {
	Console.Println("\(sum("abc"))")
}

test Sum {
	assert sum("abc") == crc32.ChecksumIEEE(strings.Bytes("abc"))
}
`,
	"mono-instance": `func count[T](xs []T) int {
	let mut n = 0
	for _ in xs {
		n = n + 1
	}
	n
}

func main() needs Console {
	Console.Println("\(count([]string{"a", "b"}))")
}

test Count {
	assert count([]int{1, 2, 3}) == 3
}
`,
	"layout-pointers-first": `type Row { N int, Name string }

func main() needs Console {
	Console.Println(Row{N: 1, Name: "a"}.Name)
}

test Name {
	assert Row{N: 1, Name: "a"}.N == 1
}
`,
	"own-consume": `import slices

func join(parts [][]int) []int {
	let mut doc = []int{}
	for p in parts {
		doc = slices.Concat(doc, p)
	}
	doc
}

func main() needs Console {
	Console.Println("\(len(join([][]int{[]int{1}, []int{2, 3}})))")
}

test Join {
	assert len(join([][]int{[]int{1}, []int{2, 3}})) == 3
}
`,
	"own-field": `type Bag { Items []int, N int }

func grow(n int) int {
	let mut total = 0
	for i in 0..n {
		let c = Bag{Items: []int{i}, N: i}
		let d = Bag{N: c.N + 1, Items: c.Items.Append(i)}
		total = total + len(d.Items)
	}
	total
}

func main() needs Console {
	Console.Println("\(grow(3))")
}

test Grow {
	assert grow(3) == 6
}
`,
	"own-call-pass": `type Bag { Items []int, N int }

type Store { bags []Bag }

func view(b Bag, w int) int {
	len(b.Items) + b.N + w
}

func bump(s mut Store, i int) int {
	s.bags[i].N = s.bags[i].N + 1
	0
}

func peek(s mut Store) int {
	view(s.bags[0], bump(s, 0))
}

func main() needs Console {
	let mut s = Store{bags: []Bag{Bag{Items: []int{1}, N: 0}}}
	Console.Println("\(peek(&s))")
}

test Peek {
	let mut s = Store{bags: []Bag{Bag{Items: []int{1}, N: 0}}}
	assert peek(&s) == 1
}
`,
	"own-enter-field": `type Walk { rows []int, n []int }

func tally(w mut Walk, v int) {
	w.n = w.n.Append(v)
}

func count(rows []int, v int) int {
	let mut w = Walk{rows: rows, n: []int{}}
	tally(&w, v)
	len(w.rows) + len(w.n)
}

func main() needs Console {
	Console.Println("\(count([]int{1, 2}, 3))")
}

test Count {
	assert count([]int{1, 2}, 3) == 3
}
`,
	"own-borrow-return": `type Bag { Items []int, N int }

type Store { bags []Bag }

func items(s mut Store) []int {
	s.bags[0].Items
}

func total(s mut Store) int {
	let it = items(s)
	len(it)
}

func main() needs Console {
	let mut s = Store{bags: []Bag{Bag{Items: []int{1}, N: 0}}}
	Console.Println("\(total(&s))")
}

test Total {
	let mut s = Store{bags: []Bag{Bag{Items: []int{1}, N: 0}}}
	assert total(&s) == 1
}
`,
	"own-read": `type Block { Kind int, Lines []string }

type Store { blocks []Block }

func total(p mut Store) int {
	let mut n = 0
	for i in 0..len(p.blocks) {
		let b = p.blocks[i]
		p.blocks[i].Kind = b.Kind + 1
		n += len(b.Lines)
	}
	n
}

func main() needs Console {
	let mut s = Store{blocks: []Block{Block{Kind: 0, Lines: []string{"a"}}}}
	Console.Println("\(total(&s))")
}

test Total {
	let mut s = Store{blocks: []Block{Block{Kind: 0, Lines: []string{"a"}}}}
	assert total(&s) == 1
}
`,
	"own-reslice": `type Stack { xs []int }

func pop(s mut Stack) {
	if len(s.xs) > 0 {
		s.xs = s.xs[:len(s.xs)-1]
	}
}

func main() needs Console {
	let mut s = Stack{xs: []int{1, 2}}
	pop(&s)
	Console.Println("\(len(s.xs))")
}

test Pop {
	let mut s = Stack{xs: []int{1, 2}}
	pop(&s)
	assert len(s.xs) == 1
}
`,
	"own-ref-in-place": `import sync

type Tally { Seen []int, N int }

func bump(r sync.Ref[Tally], x int) int {
	sync.Modify(r, func(v Tally) (int, Tally) {
		let mut s = v
		s.Seen = s.Seen.Append(x)
		s.N = s.N + 1
		(s.N, s)
	})
}

func main() needs Console {
	let r = sync.NewRef[Tally](Tally{Seen: []int{}, N: 0})
	Console.Println("\(bump(r, 3))")
}

test Bump {
	let r = sync.NewRef[Tally](Tally{Seen: []int{}, N: 0})
	assert bump(r, 3) == 1
}
`,
	"own-fresh-call": `type Doc { Items []int, Last []int }

func step(d Doc, xs []int) Doc {
	let mut items = d.Items
	items = items.Append(len(xs))
	Doc{Items: items, Last: xs}
}

func fill(n int) int {
	let mut d = Doc{Items: []int{}, Last: []int{}}
	for i in 0..n {
		d = step(d, []int{i})
	}
	len(d.Items)
}

func main() needs Console {
	Console.Println("\(fill(3))")
}

test Fill {
	assert fill(3) == 3
}
`,
	"own-return": `type Set { xs []int }

func insertNew(s Set, x int) Set {
	if len(s.xs) > 0 && s.xs[len(s.xs)-1] == x {
		return s
	}
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

func fill(n int) int {
	let mut a = Set{xs: []int{}}
	for i in 0..n {
		a = insertNew(a, i)
	}
	len(a.xs)
}

func main() needs Console {
	Console.Println("\(fill(3))")
}

test Fill {
	assert fill(3) == 3
}
`,
	"own-parts": `type Set { xs []int }

type St { set Set, n int }

func insert(s Set, x int) Set {
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

func lit(s St, x int) St { St{set: insert(s.set, x), n: s.n + 1} }

func fill(n int) int {
	let mut s = St{set: Set{xs: []int{}}, n: 0}
	for i in 0..n {
		s = lit(s, i)
	}
	len(s.set.xs) + s.n
}

func main() needs Console {
	Console.Println("\(fill(3))")
}

test Fill {
	assert fill(3) == 6
}
`,
	"own-move": `type Set { xs []int }

type St { set Set, n int }

func insert(s Set, x int) Set {
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

func upd(s St, x int) St { St{..s, set: insert(s.set, x)} }

func fill(n int) int {
	let mut s = St{set: Set{xs: []int{}}, n: 1}
	for i in 0..n {
		s = upd(s, i)
	}
	len(s.set.xs) + s.n
}

func main() needs Console {
	Console.Println("\(fill(3))")
}

test Fill {
	assert fill(3) == 4
}
`,
	"own-catch": `type Full { Cap int }

type Set { xs []int }

func InsertCapped(s Set, x int, cap int) Set fails Full {
	if len(s.xs) >= cap {
		return Full{Cap: cap}
	}
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

func fill(n int) int {
	let mut a = Set{xs: []int{}}
	for i in 0..n {
		a = InsertCapped(a, i, 3) catch {
		case Full:
			a
		}
	}
	len(a.xs)
}

func main() needs Console {
	Console.Println("\(fill(5))")
}
`,
	"own-update-self": `type Pos { x int, y int, path []int }

func walk(n int) int {
	let mut p = Pos{x: 0, y: 1, path: []int{}}
	for _ in 0..n {
		p = Pos{..p, x: p.y, y: p.x + p.y}
	}
	p.y
}

func main() needs Console {
	Console.Println("\(walk(5))")
}

test Walk {
	assert walk(5) == 8
}
`,
	"own-lineage": `type Bag { Items []int, N int }

func count(b Bag) int { b.N }

// step reads b whole (count), so b is not owned by parts (own-parts):
// the Append is the one-extender site's.
func step(b Bag, x int) Bag {
	let n = count(b)
	Bag{Items: b.Items.Append(x), N: n + 1}
}

func main() needs Console {
	Console.Println("\(len(step(step(Bag{Items: []int{}, N: 0}, 1), 2).Items))")
}

test Step {
	assert len(step(step(Bag{Items: []int{}, N: 0}, 1), 2).Items) == 2
}
`,
	"uni-slot": `type Tree = Leaf{N int} | Node{L Tree, R Tree}

func pick(t Tree, d int) Tree {
	if d > 0 {
		return pick(t, d - 1)
	}
	t
}

func size(t Tree) int {
	match pick(t, 2) {
	case Leaf as l:
		l.N
	case Node:
		2
	}
}

func main() needs Console {
	Console.Println("\(size(Leaf{N: 1}))")
}

test Size {
	assert size(Leaf{N: 1}) == 1
}
`,
}

// factsWitness exports a fact of each kind (facts.go).
const factsWitness = `type Port int where 1 <= it && it <= 65535

type C { N int }

func port(p Port) int { int(p) + 1 }

func swap(a mut C, b mut C) {
	let n = a.N
	a.N = b.N
	b.N = n
}

func at(s string, i int) int {
	let mut acc = 0
	if 0 <= i && i <= len(s)-2 {
		acc = int(s[i]) + len(s[i:i + 2])
	}
	acc
}

func main() needs Console {
	let mut x = C{N: 1}
	let mut y = C{N: 2}
	swap(&x, &y)
	Console.Println("\(port(8080) + at("abc", 1) + x.N)")
}

test Port {
	assert port(80) == 81
}
`

// TestTestBuildsTakeNoShortcut lowers the witness of each registered
// shortcut twice: the run build takes the shortcut at some site, and the
// test build (`ergo test`, LowerTests) takes it at none, so every proved
// fact is checked where tests run (plan IR-G §2 item 4); a row marked
// inTests (mono-instance) is taken by the test build too, at some site
// (TestTestBuildCallsCopies holds the row to it). It is also the
// receipt of the registry: the count of shortcuts and of the sites each
// witness takes.
//
// Mutant it alone catches: assert-test-build-takes-shortcut (takes keeps a
// shortcut on in a test build); and a registered shortcut with no witness.
func TestTestBuildsTakeNoShortcut(t *testing.T) {
	saved := statsHook
	defer func() { statsHook = saved }()
	var stats knownStats
	statsHook = func(s knownStats) { stats = s }
	for _, s := range shortcuts {
		src, ok := shortcutWitness[s.name]
		if !ok {
			t.Errorf("the shortcut %s has no witness program in shortcutWitness", s.name)
			continue
		}
		if s.referenced() {
			t.Logf("%s: the reference lowering is selected; the run build takes nothing", s.name)
			continue
		}
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("%s: the witness does not check: %v", s.name, diags)
		}
		u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
		for _, tests := range []bool{false, true} {
			stats = knownStats{}
			mustLowerUnit(t, s.name, u, tests)
			n := stats.Taken[s.name]
			switch {
			case !tests && n == 0:
				t.Errorf("%s: the run build of its witness takes it at no site\n%s", s.name, src)
			case tests && s.inTests && n == 0:
				t.Errorf("%s: the test build takes it at no site: it is marked inTests, so tests run what ships", s.name)
			case tests && n > 0 && !s.inTests:
				t.Errorf("%s: the test build takes it at %d sites: a test build lowers every site to its checking form", s.name, n)
			case !tests:
				t.Logf("%s (%s class): the run build takes %d sites", s.name, s.class, n)
			}
		}
	}
	t.Logf("%d shortcuts registered", len(shortcuts))
}
