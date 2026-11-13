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
	"own-read": `type Block { Kind int, Lines []string }

type Store { blocks []Block }

func total(p *Store) int {
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
	"own-lineage": `type Bag { Items []int, N int }

func step(b Bag, x int) Bag {
	Bag{Items: b.Items.Append(x), N: b.N + 1}
}

func main() needs Console {
	Console.Println("\(len(step(step(Bag{Items: []int{}, N: 0}, 1), 2).Items))")
}

test Step {
	assert len(step(step(Bag{Items: []int{}, N: 0}, 1), 2).Items) == 2
}
`,
}

// TestTestBuildsTakeNoShortcut lowers the witness of each registered
// shortcut twice: the run build takes the shortcut at some site, and the
// test build (`ergo test`, LowerTests) takes it at none, so every proved
// fact is checked where tests run (plan IR-G §2 item 4). It is also the
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
			case tests && n > 0:
				t.Errorf("%s: the test build takes it at %d sites: a test build lowers every site to its checking form", s.name, n)
			case !tests:
				t.Logf("%s (%s class): the run build takes %d sites", s.name, s.class, n)
			}
		}
	}
	t.Logf("%d shortcuts registered", len(shortcuts))
}
