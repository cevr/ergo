package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// splitProgram is a generated program of several Ergo packages: a base
// package, two sibling packages over it, a package top over the two
// siblings, and the root over them all.
type splitProgram struct {
	libs []pkgSource // path is the package path; text its one file
	root string
}

// genSplitProgram is the program of seed. Each package draws the forms
// that cross a package boundary in the lowering: private fields read
// through exported functions, unions matched in another package, an
// operation type with an adapter, generic types, lets in initialization
// order, machines (Go init functions), std calls from sibling packages
// (shared placement), error types over another package's types, and Go
// methods (196m functions).
func genSplitProgram(seed uint64) splitProgram {
	r := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	k := func() int { return 1 + r.IntN(9) }
	var p splitProgram
	base := fmt.Sprintf(`import strings

type Item { id int, Name string }

func NewItem(n int, name string) Item { Item{id: n, Name: name} }

func ID(it Item) int { it.id }

type Shape = Circle{R int} | Rect{W int, H int}

func Area(s Shape) int {
	match s {
	case Circle as c:
		3 * c.R * c.R
	case Rect as x:
		x.W * x.H
	}
}

type Read { Get() int }

type Q { N int }

func Get(q Q) int { q.N }

func Make() Read { Q{N: %d} }

func start() int { %d }

let N = start()

type Box[T] { V T }

func Wrap[T](v T) Box[T] { Box[T]{V: v} }

func Unwrap[T](b Box[T]) T { b.V }

func Label(xs []string) string { strings.Join(xs, "-") }

type S = A{N int}

type Sig = Go{}

func open() S { A{N: N} }

func step(s S, e Sig) Decision[S, int] { Enter[S]{Next: s} }

type M machine[S, Sig] { initial: open(), step: step }

type Bag { Xs []int }

func scale(n int) int { n * %d + 1 }

func Scale(n int) int { scale(n) }
`, k(), k(), k())
	p.libs = append(p.libs, pkgSource{"gen/base", base})
	for _, name := range []string{"left", "right"} {
		imports := "import gen/base\nimport strings\n"
		dep := "0"
		src := imports + fmt.Sprintf(`
func start() int { base.N + %d + %s }

let N = start()

func Sum(n int) int {
	let mut t = 0
	for j in 0..n {
		t = t + base.Area(base.Circle{R: j}) + base.Area(base.Rect{W: j, H: %d})
	}
	t
}

func Name() string { strings.Join([]string{"%s", "\(N)"}, ":") + base.Label([]string{"a", "b"}) }

func Item() base.Item { base.NewItem(N, "%s") }

type T = One{N int} | Two{}

func Pick(n int) T {
	if n > %d {
		return One{N: n}
	}
	Two
}

func Show(t T) int {
	match t {
	case One as x:
		x.N
	case Two:
		0
	}
}

func boxed(n int) base.Box[int] { base.Wrap(n + N) }

func Boxed(n int) int { base.Unwrap(boxed(n)) }

func Same(n int) bool { base.Bag{Xs: []int{n}} == base.Bag{Xs: []int{n, N}} }

func Scaled(n int) int { base.Scale(n) }
`, k(), dep, k(), name, name, r.IntN(4))
		p.libs = append(p.libs, pkgSource{"gen/" + name, src})
	}
	// top sees base only through left and right.
	p.libs = append(p.libs, pkgSource{"gen/top", fmt.Sprintf("import gen/left\nimport gen/right\n\nfunc Both(n int) bool { left.Same(n) == right.Same(n + %d) }\n\nfunc Sizes(n int) int { left.Boxed(n) + len(right.Item().Name) }\n", r.IntN(2))})
	p.root = fmt.Sprintf(`import gen/base
import gen/left
import gen/right
import gen/top

let Total = left.N + right.N + base.N

type Oops[T] { V T }

func oops() fails Oops[base.Q] { Oops[base.Q]{V: base.Q{N: %d}} }

func line() string {
	"\(Total) \(left.Sum(%d)) \(right.Sum(%d)) \(left.Name()) \(right.Name()) \(base.ID(left.Item())) \(base.ID(right.Item())) \(base.Make().Get()) \(left.Show(left.Pick(%d))) \(right.Show(right.Pick(%d))) \(left.Boxed(%d)) \(right.Boxed(%d)) \(left.Same(%d)) \(right.Same(%d)) \(left.Scaled(%d)) \(right.Scaled(%d)) \(top.Both(%d)) \(top.Sizes(%d))"
}

func main() needs Console {
	let _ = base.M{}
	Console.Println(line())
}

test Line { assert line() == line() }

test Fails { try oops() }

law Ident(n int) { left.Boxed(n) == n + left.N }
`, k(), k(), k(), k(), k(), k(), k(), k(), k(), k(), k(), k(), k())
	return p
}

// check checks the program, and returns its root unit.
func (g splitProgram) check(t *testing.T) Unit {
	t.Helper()
	imp := pkgImporter{}
	var units []Unit
	for _, lib := range g.libs {
		u := checkPackage(t, lib.path, []pkgSource{{"/split/" + lib.path + "/a.ergo", lib.text}}, imp)
		imp[lib.path] = u.Package
		units = append(units, u)
	}
	root := checkPackage(t, "main", []pkgSource{{"/split/main.ergo", g.root}}, imp)
	root.Imports = units
	return root
}

// splitLowered is o's module split into its Go packages, as the program
// p/NAME of the test module: its main.go is the root package, and its
// other packages are extra files below p/NAME.
func splitLowered(t *testing.T, o *lowered, u Unit, name string) (*lowered, SplitReport) {
	t.Helper()
	files, rep := splitMain(u, []byte(o.mainGo()), programModule+"/p/"+name)
	out := &lowered{name: name, src: o.src, pkg: o.pkg}
	if files == nil {
		out.files = o.files
		return out, rep
	}
	for _, f := range files {
		if f.Path == "main.go" {
			out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, f}
			continue
		}
		out.extra = append(out.extra, File{Path: "p/" + name + "/" + f.Path, Data: f.Data})
	}
	return out, rep
}

// TestSplitLoweringAgrees is the agreement property of the package split
// (compile-speed CS4): on generated programs of several Ergo packages, the
// one-package lowering (the reference) and its split into Go packages
// print the same, fail the same and exit the same, in the run build and in
// the test build; and every program splits (the split is not vacuous). A
// one-package program does not split (its groups all start in the root,
// and nothing below the root uses them), so every corpus program agrees by
// identity.
//
// It builds Go because the question is the behaviour of the linked program:
// Go's package initialization order, method sets and reflection of type
// names differ between one package and several, and no in-process check
// runs them. A failing seed prints the Ergo source, both outputs and the
// replay command (ERGO_SPLIT_SEED=n).
func TestSplitLoweringAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if s := os.Getenv("ERGO_SPLIT_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	type pair struct {
		seed  uint64
		tests bool
		ref   *lowered
		split *lowered
	}
	var pairs []pair
	var programs []*lowered
	for _, seed := range seeds {
		g := genSplitProgram(seed)
		u := g.check(t)
		for _, tests := range []bool{false, true} {
			name := fmt.Sprintf("s%dt%v", seed, tests)
			ref := mustLowerUnit(t, name+"ref", u, tests)
			split, rep := splitLowered(t, ref, u, name+"split")
			if rep.Packages < 3 || rep.Reason != "" {
				t.Fatalf("seed %d tests=%v: the program did not split: %+v\nsource:\n%s", seed, tests, rep, ref.src)
			}
			pairs = append(pairs, pair{seed, tests, ref, split})
			programs = append(programs, ref, split)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the programs do not build")
	}
	for _, p := range pairs {
		o1, e1, c1 := runExe(t, exes[p.ref.name])
		o2, e2, c2 := runExe(t, exes[p.split.name])
		t.Logf("seed %d tests=%v: exit %d, %d bytes of stdout, %d of stderr", p.seed, p.tests, c1, len(o1), len(e1))
		if o1 != o2 || e1 != e2 || c1 != c2 {
			var split strings.Builder
			for _, f := range p.split.extra {
				split.WriteString("// " + f.Path + "\n" + string(f.Data))
			}
			t.Errorf("seed %d tests=%v: the split program differs\nsource:\n%s\nreference: exit %d\nstdout:\n%s\nstderr:\n%s\nsplit: exit %d\nstdout:\n%s\nstderr:\n%s\nsplit Go (root):\n%s\n%s\nreplay: ERGO_SPLIT_SEED=%d go test -run TestSplitLoweringAgrees ./internal/sketch/lower",
				p.seed, p.tests, p.ref.src, c1, o1, e1, c2, o2, e2, p.split.mainGo(), split.String(), p.seed)
		}
	}
}
