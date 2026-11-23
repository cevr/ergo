package task

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// dirBase is a Base over a folder holding the base tree.
type dirBase struct{ root, work string }

func (b dirBase) Changed(p string) bool {
	x, errx := os.ReadFile(filepath.Join(b.root, p))
	y, erry := os.ReadFile(filepath.Join(b.work, p))
	return (errx == nil) != (erry == nil) || !bytes.Equal(x, y)
}

func (b dirBase) Listing(dir string) (string, error) { return listing(b.root, dir) }

// TestAffectedIsWhereTheKeyMoved is the law of --affected (build.md,
// Generators): over the seeded worlds of TestGenCheckIsFreshRegeneration,
// generated, then copied as the base and edited (a read file, an unread
// one, a generated file by hand, a new file in a listed folder, a deleted
// output), the affected nodes are exactly the reference's: a node whose
// key over its last run (inputs, listings) differs between the base and
// the working tree, or whose outputs differ, and every node reachable
// from one by the graph's edges. It catches task-affected-ignores-base
// (testdata/mutants.json). A failing seed prints its replay command.
func TestAffectedIsWhereTheKeyMoved(t *testing.T) {
	seeds := []int64{}
	if *taskSeed != 0 {
		seeds = append(seeds, *taskSeed)
	} else {
		for s := int64(1); s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		rng := rand.New(rand.NewPCG(uint64(seed), 13))
		w := newWorld(rng)
		for i := range w.gens {
			w.gens[i].sly = "" // the base and the tree differ only where the key can see
		}
		work, base := t.TempDir(), t.TempDir()
		for _, d := range w.data {
			writeFile(t, work, d, d+" v0\n")
		}
		writeFile(t, work, "src/unread.txt", "n0\n")
		r := &Runner{Root: work, Store: lawcache.Memory(StoreIdentity), Out: &strings.Builder{}}
		nodes := w.nodes()
		if _, err := r.Gen(context.Background(), nodes, Mode{}); err != nil {
			t.Fatal(err)
		}
		if err := copyTree(work, base); err != nil {
			t.Fatal(err)
		}
		for step := range 1 + rng.IntN(3) {
			switch rng.IntN(5) {
			case 0:
				d := w.data[rng.IntN(len(w.data))]
				writeFile(t, work, d, fmt.Sprintf("%s v%d\n", d, step+1))
			case 1:
				writeFile(t, work, "src/unread.txt", fmt.Sprintf("n%d\n", step+1))
			case 2:
				writeFile(t, work, w.gens[rng.IntN(len(w.gens))].output, "by hand\n")
			case 3:
				writeFile(t, work, fmt.Sprintf("src/d%d/new%d.txt", rng.IntN(2), step), "new\n")
			case 4:
				os.Remove(filepath.Join(work, w.gens[rng.IntN(len(w.gens))].output))
			}
		}
		got, err := r.Affected(nodes, dirBase{root: base, work: work})
		if err != nil {
			t.Fatal(err)
		}
		var gotNames []string
		for _, a := range got {
			gotNames = append(gotNames, a.Name)
		}
		want := affectedReference(t, r, nodes, base, work)
		slices.Sort(gotNames)
		if !slices.Equal(gotNames, want) {
			t.Fatalf("seed %d: affected %v (%+v), want %v\nworld %+v\nreplay: go test ./internal/task -run TestAffectedIsWhereTheKeyMoved -task.seed %d", seed, gotNames, got, want, w.gens, seed)
		}
	}
}

// affectedReference keys every node's last run in both trees and closes
// the moved ones over the graph's edges.
func affectedReference(t *testing.T, r *Runner, nodes []Node, base, work string) []string {
	t.Helper()
	g, err := r.Graph(nodes)
	if err != nil {
		t.Fatal(err)
	}
	hb, hw := newHasher(base), newHasher(work)
	moved := make([]bool, len(nodes))
	for i, n := range nodes {
		text, _ := r.Store.Get(r.lastKey(n))
		rec, _ := decodeRecord(text)
		kb, _, _, err := contentKey(n, rec.env, rec.in, base, hb)
		if err != nil {
			t.Fatal(err)
		}
		kw, _, _, err := contentKey(n, rec.env, rec.in, work, hw)
		if err != nil {
			t.Fatal(err)
		}
		moved[i] = kb != kw
		for _, o := range rec.outs {
			db, _ := hb.digest(o.path)
			dw, _ := hw.digest(o.path)
			moved[i] = moved[i] || db != dw
		}
	}
	for range nodes {
		for _, e := range g.Edges {
			moved[e[1]] = moved[e[1]] || moved[e[0]]
		}
	}
	var out []string
	for i, n := range nodes {
		if moved[i] {
			out = append(out, n.Name)
		}
	}
	slices.Sort(out)
	return out
}
