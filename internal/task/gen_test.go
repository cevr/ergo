package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lawcache"
)

var taskSeed = flag.Int64("task.seed", 0, "run TestGenCheckIsFreshRegeneration on this seed only")

// fakeGen is a generator of the property: it reads data files and the
// outputs of other generators, lists folders, and writes one output, the
// digest of its name and everything it read. A sly one also reads a file
// it does not declare.
type fakeGen struct {
	name   string
	reads  []string // data files, declared
	lists  []string // folders, declared
	after  []int    // generators whose output it reads, declared
	sly    string   // a file it reads and does not declare ("" for none)
	output string
}

// world is a seeded workspace of fake generators.
type world struct {
	gens []fakeGen
	data []string // the data files' paths
}

func newWorld(rng *rand.Rand) world {
	var w world
	for i := range 3 + rng.IntN(4) {
		w.data = append(w.data, fmt.Sprintf("src/d%d/f%d.txt", i%2, i))
	}
	n := 2 + rng.IntN(4)
	// rank is the true dependency order; declaration order is a shuffle
	// of it, so the runner must find the order itself.
	rank := rng.Perm(n)
	byRank := make([]int, n)
	for i, r := range rank {
		byRank[r] = i
	}
	w.gens = make([]fakeGen, n)
	for i := range n {
		g := fakeGen{name: fmt.Sprintf("g%d", i), output: fmt.Sprintf("gen/g%d.out", i)}
		for _, d := range w.data {
			if rng.IntN(3) == 0 {
				g.reads = append(g.reads, d)
			}
		}
		if rng.IntN(3) == 0 {
			g.lists = append(g.lists, fmt.Sprintf("src/d%d", rng.IntN(2)))
		}
		for r := 0; r < rank[i]; r++ {
			if rng.IntN(2) == 0 {
				g.after = append(g.after, byRank[r])
			}
		}
		w.gens[i] = g
	}
	if rng.IntN(3) == 0 {
		i := rng.IntN(n)
		w.gens[i].sly = "src/undeclared.txt"
	}
	return w
}

// produce is what generator i writes over the files in tree.
func (w world) produce(i int, tree map[string]string, listing func(dir string) string) string {
	g := w.gens[i]
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", g.name)
	for _, r := range g.reads {
		fmt.Fprintf(h, "r %s %q\n", r, tree[r])
	}
	for _, d := range g.lists {
		fmt.Fprintf(h, "l %s %s\n", d, listing(d))
	}
	for _, a := range g.after {
		fmt.Fprintf(h, "a %q\n", tree[w.gens[a].output])
	}
	if g.sly != "" {
		fmt.Fprintf(h, "s %q\n", tree[g.sly])
	}
	return hex.EncodeToString(h.Sum(nil)) + "\n"
}

// reference is the tree a fresh regeneration makes of tree: every
// generator run, dependencies first, until nothing changes.
func (w world) reference(tree map[string]string) map[string]string {
	out := maps(tree)
	listing := func(dir string) string {
		var names []string
		for p := range out {
			if parentDir(p) == dir {
				names = append(names, p[len(dir)+1:])
			}
		}
		slices.Sort(names)
		return strings.Join(names, ",")
	}
	for range len(w.gens) + 1 {
		for i, g := range w.gens {
			out[g.output] = w.produce(i, out, listing)
		}
	}
	return out
}

func maps(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// readTree is the files below root, but .git.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		out[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nodes are the world's generators as nodes reading and writing the tree
// at the root they are given.
func (w world) nodes() []Node {
	var nodes []Node
	for i, g := range w.gens {
		nodes = append(nodes, Node{
			Name:  g.name,
			Class: Vouched,
			Ident: g.name,
			Env:   func(context.Context) (string, error) { return "env", nil },
			Inputs: func(_ context.Context, root string) (Inputs, error) {
				in := Inputs{Files: slices.Clone(g.reads), Dirs: slices.Clone(g.lists)}
				for _, a := range g.after {
					in.Files = append(in.Files, w.gens[a].output)
				}
				return in, nil
			},
			Run: func(_ context.Context, root string) ([]byte, error) {
				tree := map[string]string{}
				for _, p := range append(append(slices.Clone(g.reads), g.sly), outputsOf(w, g.after)...) {
					if p == "" {
						continue
					}
					data, err := os.ReadFile(filepath.Join(root, p))
					if err == nil {
						tree[p] = string(data)
					}
				}
				listing := func(dir string) string {
					entries, _ := os.ReadDir(filepath.Join(root, dir))
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					return strings.Join(names, ",")
				}
				p := filepath.Join(root, g.output)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return nil, err
				}
				return nil, os.WriteFile(p, []byte(w.produce(i, tree, listing)), 0o644)
			},
		})
	}
	return nodes
}

func outputsOf(w world, gens []int) []string {
	var out []string
	for _, a := range gens {
		out = append(out, w.gens[a].output)
	}
	return out
}

// generated are the outputs that differ between two trees, sorted.
func (w world) generatedDiff(a, b map[string]string) []string {
	var out []string
	for _, g := range w.gens {
		if a[g.output] != b[g.output] {
			out = append(out, g.output)
		}
	}
	slices.Sort(out)
	return out
}

// TestGenCheckIsFreshRegeneration is the law of `ergo task gen` (build.md,
// Generators), a seeded property over workspaces of generators that read
// data, list folders and read each other's outputs, declared in a shuffled
// order, and over edit scripts (edit a file a generator reads, one none
// reads, a generated file by hand; add a file to a listed folder; delete
// an output; edit a file a sly generator reads without declaring it):
//
//   - gen --check writes nothing and fails exactly when a fresh
//     regeneration (the reference: every generator run until nothing
//     changes) would change a generated file; with a sly generator whose
//     undeclared file changed, plain --check may miss it, and --check
//     --fresh catches it and names the generator;
//   - gen leaves the tree as the fresh regeneration does, runs each
//     generator at most once once every one has run before, each after
//     every generator whose output it reads; and a second gen runs none.
//
// It catches task-order-ignores-edge (a generator runs before one it
// reads, so it runs twice) and task-gen-check-writes (--check regenerates
// in the tree), and the rows task-listing-unkeyed, task-outputs-unchecked
// and task-fresh-trusts-hits (testdata/mutants.json). A failing seed
// prints the world, the trees and `go test ./internal/task -run
// TestGenCheckIsFreshRegeneration -task.seed N`.
func TestGenCheckIsFreshRegeneration(t *testing.T) {
	seeds := []int64{}
	if *taskSeed != 0 {
		seeds = append(seeds, *taskSeed)
	} else {
		for s := int64(1); s <= 60; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { genProperty(t, seed) })
	}
}

func genProperty(t *testing.T, seed int64) {
	rng := rand.New(rand.NewPCG(uint64(seed), 7))
	w := newWorld(rng)
	root := t.TempDir()
	scratchRoot := t.TempDir()
	for _, d := range w.data {
		writeFile(t, root, d, fmt.Sprintf("%s v0\n", d))
	}
	writeFile(t, root, "src/undeclared.txt", "u0\n")
	writeFile(t, root, "src/unread.txt", "n0\n")
	var log strings.Builder
	var ran []string
	r := &Runner{
		Root:  root,
		Store: lawcache.Memory(StoreIdentity),
		Out:   &log,
		Scratch: func() (string, func(), error) {
			d, err := os.MkdirTemp(scratchRoot, "copy")
			return d, func() { os.RemoveAll(d) }, err
		},
		Ran: func(name, _ string) { ran = append(ran, name) },
	}
	nodes := w.nodes()
	fail := func(step string, format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d, step %s: %s\nworld: %+v\ntree: %v\nlog:\n%s\nreplay: go test ./internal/task -run TestGenCheckIsFreshRegeneration -task.seed %d",
			seed, step, fmt.Sprintf(format, args...), w.gens, readTree(t, root), log.String(), seed)
	}
	gen := func(step string, mode Mode) Report {
		t.Helper()
		ran = ran[:0]
		rep, err := r.Gen(context.Background(), nodes, mode)
		if err != nil {
			fail(step, "gen: %v", err)
		}
		return rep
	}
	// The first run: nothing known; afterwards the tree is the reference.
	before := readTree(t, root)
	gen("first", Mode{})
	if got, want := readTree(t, root), w.reference(before); len(w.generatedDiff(got, want)) > 0 {
		fail("first", "gen left %v unlike a fresh regeneration", w.generatedDiff(got, want))
	}
	slyDirty := false
	for step := range 8 {
		name := fmt.Sprint(step)
		// An edit.
		switch k := rng.IntN(6); k {
		case 0:
			d := w.data[rng.IntN(len(w.data))]
			writeFile(t, root, d, fmt.Sprintf("%s v%d\n", d, step+1))
			name += " edit " + d
		case 1:
			writeFile(t, root, "src/unread.txt", fmt.Sprintf("n%d\n", step+1))
			name += " edit unread"
		case 2:
			g := w.gens[rng.IntN(len(w.gens))]
			writeFile(t, root, g.output, "by hand\n")
			name += " hand-edit " + g.output
		case 3:
			writeFile(t, root, fmt.Sprintf("src/d%d/new%d.txt", rng.IntN(2), step), "new\n")
			name += " add a file"
		case 4:
			g := w.gens[rng.IntN(len(w.gens))]
			os.Remove(filepath.Join(root, g.output))
			name += " delete " + g.output
		case 5:
			writeFile(t, root, "src/undeclared.txt", fmt.Sprintf("u%d\n", step+1))
			name += " edit undeclared"
			for _, g := range w.gens {
				if g.sly != "" {
					slyDirty = true
				}
			}
		}
		tree := readTree(t, root)
		want := w.generatedDiff(tree, w.reference(tree))
		// gen --check --fresh: exact, writes nothing.
		rep := gen(name+" check fresh", Mode{Check: true, Fresh: true})
		if !maps2Equal(readTree(t, root), tree) {
			fail(name, "gen --check --fresh wrote the tree")
		}
		if !slices.Equal(rep.Stale, want) {
			fail(name, "gen --check --fresh: stale %v, a fresh regeneration changes %v", rep.Stale, want)
		}
		if slyDirty && len(rep.Contradicted) == 0 && len(want) > 0 {
			// The sly generator's stored result, keyed without the file
			// it read, must be contradicted by the fresh run.
			fail(name, "gen --check --fresh did not name the generator whose undeclared read changed (stale %v)", rep.Stale)
		}
		// gen --check: exact unless a sly generator's undeclared read moved.
		rep = gen(name+" check", Mode{Check: true})
		if !maps2Equal(readTree(t, root), tree) {
			fail(name, "gen --check wrote the tree")
		}
		if !slyDirty && !slices.Equal(rep.Stale, want) {
			fail(name, "gen --check: stale %v, a fresh regeneration changes %v", rep.Stale, want)
		}
		// gen: the reference, each generator at most once, in order.
		rep = gen(name+" gen", Mode{Fresh: slyDirty})
		got := readTree(t, root)
		if d := w.generatedDiff(got, w.reference(tree)); len(d) > 0 {
			fail(name, "gen left %v unlike a fresh regeneration", d)
		}
		for n, c := range rep.Ran {
			if c > 1 {
				fail(name, "gen ran %s %d times (order %v)", n, c, rep.Order)
			}
		}
		pos := map[string]int{}
		for i, n := range ran {
			pos[n] = i
		}
		for b, g := range w.gens {
			for _, a := range g.after {
				pa, oka := pos[w.gens[a].name]
				pb, okb := pos[w.gens[b].name]
				if oka && okb && pa > pb {
					fail(name, "gen ran %s before %s, whose output it reads (runs %v)", w.gens[b].name, w.gens[a].name, ran)
				}
			}
		}
		slyDirty = false
		// A second gen runs nothing.
		rep = gen(name+" again", Mode{})
		if len(ran) > 0 {
			fail(name, "a second gen ran %v", ran)
		}
	}
}

// ringNodes are n generators in a ring: ring/i.out is written from the
// output of the one before it. A restless ring appends a mark to what it
// read, so it never settles; a settling ring writes one mark more than
// it read, at most cap of them, so it settles once every output holds cap.
func ringNodes(n, capMarks int, restless bool) []Node {
	var nodes []Node
	out := func(i int) string { return fmt.Sprintf("ring/%d.out", i) }
	for i := range n {
		prev := out((i + n - 1) % n)
		nodes = append(nodes, Node{
			Name:   fmt.Sprintf("r%d", i),
			Class:  Vouched,
			Ident:  fmt.Sprint(i),
			Env:    func(context.Context) (string, error) { return "env", nil },
			Inputs: func(context.Context, string) (Inputs, error) { return Inputs{Files: []string{prev}}, nil },
			Run: func(_ context.Context, root string) ([]byte, error) {
				data, _ := os.ReadFile(filepath.Join(root, prev))
				text := string(data) + "x"
				if !restless && len(text) > capMarks {
					text = text[:capMarks]
				}
				if err := os.MkdirAll(filepath.Join(root, "ring"), 0o755); err != nil {
					return nil, err
				}
				return nil, os.WriteFile(filepath.Join(root, out(i)), []byte(text), 0o644)
			},
		})
	}
	return nodes
}

// TestGenStopsARestlessCycle is the bound on passes (build.md,
// Generators): over seeded rings of 2 to 5 generators that read each
// other's outputs, declared in a shuffled order, a ring that settles is
// run to its fixed point (every output holds the cap) with no error, and
// a ring that never settles fails within k+1 running passes, k its size,
// naming its generators, rather than loop. It catches
// task-cycle-unbounded (testdata/mutants.json). A failing seed prints
// `go test ./internal/task -run TestGenStopsARestlessCycle -task.seed N`.
func TestGenStopsARestlessCycle(t *testing.T) {
	seeds := []int64{}
	if *taskSeed != 0 {
		seeds = append(seeds, *taskSeed)
	} else {
		for s := int64(1); s <= 20; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		rng := rand.New(rand.NewPCG(uint64(seed), 11))
		n := 2 + rng.IntN(4)
		restless := rng.IntN(2) == 0
		capMarks := 1 + rng.IntN(n) // a ring of n settles in n+1 passes at a cap of at most n
		nodes := ringNodes(n, capMarks, restless)
		rng.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		root := t.TempDir()
		var log strings.Builder
		r := &Runner{Root: root, Store: lawcache.Memory(StoreIdentity), Out: &log}
		rep, err := r.Gen(context.Background(), nodes, Mode{})
		replay := fmt.Sprintf("seed %d (ring of %d, restless %v, cap %d)\nlog:\n%s\nreplay: go test ./internal/task -run TestGenStopsARestlessCycle -task.seed %d", seed, n, restless, capMarks, log.String(), seed)
		running := 0
		for _, c := range rep.Ran {
			running = max(running, c)
		}
		if restless {
			if err == nil || !strings.Contains(err.Error(), "keep changing") {
				t.Fatalf("a restless ring: %v\n%s", err, replay)
			}
			if running > n+1 {
				t.Fatalf("a restless ring ran a generator %d times, past the bound %d\n%s", running, n+1, replay)
			}
			continue
		}
		if err != nil {
			t.Fatalf("a settling ring: %v\n%s", err, replay)
		}
		for i := range n {
			data, _ := os.ReadFile(filepath.Join(root, fmt.Sprintf("ring/%d.out", i)))
			if string(data) != strings.Repeat("x", capMarks) {
				t.Fatalf("a settling ring left ring/%d.out = %q, want %d marks\n%s", i, data, capMarks, replay)
			}
		}
	}
}

// TestGenMissSaysWhy: every miss names its reason, the first input whose
// digest moved since the last run (a file, a listed folder's names, the
// environment), so an unexplained miss cannot pass unseen (MONOREPO WT1b,
// the c10 miss). It catches task-why-unnamed (testdata/mutants.json).
func TestGenMissSaysWhy(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("in/a.txt", "a")
	write("in/b.txt", "b")
	env := "go1"
	node := Node{
		Name: "n", Class: Vouched, Ident: "n",
		Env: func(context.Context) (string, error) { return env, nil },
		Inputs: func(context.Context, string) (Inputs, error) {
			return Inputs{Files: []string{"in/a.txt", "in/b.txt"}, Dirs: []string{"in"}}, nil
		},
		Run: func(_ context.Context, root string) ([]byte, error) {
			a, _ := os.ReadFile(filepath.Join(root, "in/a.txt"))
			b, _ := os.ReadFile(filepath.Join(root, "in/b.txt"))
			return nil, os.WriteFile(filepath.Join(root, "out.txt"), append(a, b...), 0o644)
		},
	}
	r := &Runner{Root: root, Store: lawcache.Memory(StoreIdentity)}
	gen := func() string {
		t.Helper()
		var log strings.Builder
		r.Out = &log
		if _, err := r.Gen(context.Background(), []Node{node}, Mode{}); err != nil {
			t.Fatal(err)
		}
		return log.String()
	}
	for _, step := range []struct {
		edit func()
		want string
	}{
		{func() {}, "ran (no stored last run)"},
		{func() { write("in/b.txt", "b2") }, "ran (input in/b.txt changed)"},
		{func() { write("in/c.txt", "c") }, "ran (the names in folder in changed)"},
		{func() { env = "go2" }, `ran (the environment changed: "go1" -> "go2")`},
		{func() { write("out.txt", "hand") }, "ran (output out.txt no longer hashes as stored)"},
		{func() {}, "hit"},
	} {
		step.edit()
		if log := gen(); !strings.Contains(log, step.want) {
			t.Fatalf("want %q in:\n%s", step.want, log)
		}
	}
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
