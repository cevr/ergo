package lower

import (
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdFileOfIndexAgrees holds stdFileOf's node index to the walk it
// replaces (stdFileOfWalkOnly): two lowerers of one program, asked about
// the same nodes in the same seeded order (a sample of the nodes of the
// reached std declarations, their admissions and the program's own file,
// which no std file holds, and repeats), give the same answer to each question and keep
// the same answers, admissionNodes' included. A failing seed is printed;
// replay it with the same seed.
//
// Mutants it alone catches: testdata/mutants.json, rows
// stdfile-index-lib-path and stdfile-index-decl-at.
func TestStdFileOfIndexAgrees(t *testing.T) {
	const src = `import strings
import slices

func main() {
	let parts = strings.Split("a,b", ",")
	let _ = slices.Contains(parts, "a")
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("checker diagnostics %v", diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	var nodes []syntax.Node
	probe := newLowerer(u)
	for _, p := range probe.libraries() {
		if !p.Std() {
			continue
		}
		for _, f := range p.Files() {
			for _, d := range f.Decls {
				syntax.Inspect(d, func(m syntax.Node) bool {
					if m != nil {
						nodes = append(nodes, m)
					}
					return m != nil
				})
			}
		}
		admissionNodes(p, func(law, n syntax.Node) { nodes = append(nodes, law, n) })
	}
	syntax.Inspect(file, func(m syntax.Node) bool {
		if m != nil {
			nodes = append(nodes, m)
		}
		return m != nil
	})
	if len(nodes) < 1000 {
		t.Fatalf("the program reaches only %d std nodes", len(nodes))
	}
	for _, seed := range []uint64{1, 2, 3} {
		r := rand.New(rand.NewPCG(seed, 0))
		order := append([]syntax.Node(nil), nodes...)
		r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		order = order[:min(len(order), 3000)]
		order = append(order, order[:len(order)/4]...)
		walk, index := newLowerer(u), newLowerer(u)
		for i, n := range order {
			stdFileOfWalkOnly = true
			want, wantOK := walk.stdFileOf(n)
			stdFileOfWalkOnly = false
			got, gotOK := index.stdFileOf(n)
			if got != want || gotOK != wantOK {
				t.Fatalf("seed %d, question %d (%T at %d): index answers %q %v, the walk %q %v", seed, i, n, n.Pos(), got.path, gotOK, want.path, wantOK)
			}
		}
		shared, outside := 0, 0
		for n, at := range index.stdNodeIndex().decl {
			if at < 0 {
				shared++
			} else if d := index.stdIx.decls[at].d; d.Pos() > n.Pos() || n.End() > d.End() {
				outside++
			}
		}
		t.Logf("seed %d: %d questions; the index holds %d nodes, %d of two declarations, %d outside their declaration's span", seed, len(order), len(index.stdIx.decl), shared, outside)
		if len(walk.stdSrc) != len(index.stdSrc) {
			t.Fatalf("seed %d: the index keeps %d answers, the walk %d", seed, len(index.stdSrc), len(walk.stdSrc))
		}
		for n, sf := range walk.stdSrc {
			if index.stdSrc[n] != sf {
				t.Fatalf("seed %d: kept answer for %T at %d: index %q, walk %q", seed, n, n.Pos(), index.stdSrc[n].path, sf.path)
			}
		}
	}
}
