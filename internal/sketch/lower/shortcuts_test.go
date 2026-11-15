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
}

// factsWitness exports a fact of each kind (facts.go).
const factsWitness = `type Port int where 1 <= it && it <= 65535

type C { N int }

func port(p Port) int { int(p) + 1 }

func swap(a *C, b *C) {
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
