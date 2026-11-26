package lower

import (
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdFileOfAgreesWithEveryWalk (PERF-LOWER-BUILD-1): stdFileOf, which
// places each candidate declaration's nodes once, answers every node of
// every reached std declaration, every node an admission law made and a
// node of no file as the reference does (the first declaration in package
// and file order whose tree holds the node, else the file of the law that
// made it, else none), whatever order the nodes are asked in; and it walks
// each std declaration at most once over all the asks (stdWalks), where
// the walk it replaced walked every candidate again for each node.
// Replay: the seed is the row's; a failure names it and the node.
//
// Mutants (lower mutants.json): perf-lower-stdfile-rewalks-placed,
// perf-lower-stdfile-wrong-file, perf-lower-stdfile-admission-dropped,
// perf-lower-stdfile-overwrites-placement.
func TestStdFileOfAgreesWithEveryWalk(t *testing.T) {
	src := "import json\nimport strings\nimport tasks\nimport actor\nimport log\n\nfunc uses(a json.Text, b tasks.FailFast, c actor.Overflowed, d log.LevelInfo) string {\n\tstrings.ToUpper(\"a\")\n}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("the probe does not check: %v", diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	type answer struct {
		file *syntax.File
		ok   bool
	}
	want := map[syntax.Node]answer{}
	var nodes []syntax.Node
	decls, admitted := 0, 0
	ref := newLowerer(u)
	for _, p := range ref.libraries() {
		if !p.Std() {
			continue
		}
		for _, f := range p.Files() {
			for _, d := range f.Decls {
				decls++
				syntax.Inspect(d, func(m syntax.Node) bool {
					if m == nil {
						return false
					}
					if _, ok := want[m]; !ok {
						want[m] = answer{f, true}
						nodes = append(nodes, m)
					}
					return true
				})
			}
		}
	}
	for _, p := range ref.libraries() {
		if !p.Std() {
			continue
		}
		admissionNodes(p, func(law, n syntax.Node) {
			if _, ok := want[n]; !ok {
				want[n] = want[law]
				nodes = append(nodes, n)
				admitted++
			}
		})
	}
	stray := &syntax.Ident{Name: "stray"}
	want[stray] = answer{}
	nodes = append(nodes, stray)
	if decls == 0 || admitted == 0 {
		t.Fatalf("the probe reaches %d std declarations and %d admission nodes; the property needs both", decls, admitted)
	}
	for seed := uint64(1); seed <= 3; seed++ {
		l := newLowerer(u)
		order := append([]syntax.Node(nil), nodes...)
		r := rand.New(rand.NewPCG(seed, seed))
		r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		if seed == 1 {
			order = append([]syntax.Node{stray}, order...)
		}
		for _, n := range order {
			sf, ok := l.stdFileOf(n)
			if w := want[n]; ok != w.ok || ok && sf.file != w.file {
				t.Fatalf("seed %d: stdFileOf(%T at %d) = %q %t, want %q %t", seed, n, n.Pos(), fileName(sf.file), ok, fileName(w.file), w.ok)
			}
		}
		if l.stdWalks > decls {
			t.Errorf("seed %d: %d placing walks of std declarations for %d nodes, want at most one for each of the %d declarations", seed, l.stdWalks, len(order), decls)
		}
	}
}

// fileName is f's name, or "" for none.
func fileName(f *syntax.File) string {
	if f == nil {
		return ""
	}
	return f.Name
}
