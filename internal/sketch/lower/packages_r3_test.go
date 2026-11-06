package lower

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Round 3 (196b): the lowering output is a function of the program's
// canonical identity. Lowering one checked unit again, with its files and
// imports listed in another order, or checked again from files and import
// lines in another order, emits the same Go files, byte for byte, in the
// run build and the test build. Round 4: every declaration kind of the
// generated program lands in any of several files of its package.

// genProgram is a generated program: its libraries and the root's files.
type genProgram struct {
	libs  []genLib
	alias map[string]string // library path to the root's import name
	root  []pkgSource
	want  string // main's output
}

type genLib struct {
	path  string
	files []pkgSource
}

// genChunk is one top-level declaration of a generated package and the
// libraries (by index) whose import it needs.
type genChunk struct {
	text string
	uses []int
}

// spread lays chunks out over one to three files of dir, in an order and
// a file each that r chooses, every file non-empty. Each file starts with
// the import lines of the libraries its chunks use, in an order r chooses.
func spread(r *rand.Rand, dir string, chunks []genChunk, names, paths []string) []pkgSource {
	files := 1 + r.Intn(3)
	if files > len(chunks) {
		files = len(chunks)
	}
	order := r.Perm(len(chunks))
	of := make([][]genChunk, files)
	for k, c := range order {
		f := k // the first chunks fill each file once
		if k >= files {
			f = r.Intn(files)
		}
		of[f] = append(of[f], chunks[c])
	}
	base := []string{"main", "errors", "more"}
	var out []pkgSource
	for f, cs := range of {
		uses := map[int]bool{}
		var body strings.Builder
		for _, c := range cs {
			for _, i := range c.uses {
				uses[i] = true
			}
			body.WriteString(c.text + "\n")
		}
		var head strings.Builder
		for _, k := range r.Perm(len(paths)) {
			if uses[k] {
				fmt.Fprintf(&head, "import %s %s\n", names[k], paths[k])
			}
		}
		out = append(out, pkgSource{"/gen/" + dir + base[f] + ".ergo", head.String() + body.String()})
	}
	return out
}

// generateProgram builds a program from seed. It has two to four
// libraries whose paths end alike (x/lib, y/lib, ...), each with the same
// declarations: an operation type Read that Q converts to (an adapter), a
// generic Box with functions over it, a function Show over each of Q and
// Box (methods), generic overloads Pick[T] over Q and R, which lower as
// package functions, and a test. The root names each library's Box[int]
// (instances whose displays coincide) and has an error type Oops over each
// library's Q and Box[int] (error-text instances); methods and generic
// overloads of its own; error types generic over a failure row only
// (Hold), over a type and a row (Pair) and over a service row (Job), and
// records over them, whose text functions are one per Go type; tests,
// properties and a law. Every package's declarations land in one to three
// files, each declaration in a file and at a place the seed chooses.
func generateProgram(seed int64) genProgram {
	r := rand.New(rand.NewSource(seed))
	paths := []string{"x/lib", "y/lib", "z/lib", "w/lib"}[:2+r.Intn(3)]
	names := []string{"a", "b", "c", "d"}
	g := genProgram{alias: map[string]string{}}
	var calls, want []string
	var root []genChunk
	for i, path := range paths {
		n := fmt.Sprint(i + 1)
		lib := []genChunk{
			{"type Read { Get() int }", nil},
			{"type Q { N int }", nil},
			{"func Get(q Q) int { q.N }", nil},
			{"func Make() Read { Q{N: " + n + "} }", nil},
			{"func Show(q Q, n int) int { n + q.N }", nil},
			{"type Box[T] { V T }", nil},
			{"func Wrap[T](v T) Box[T] { Box[T]{V: v} }", nil},
			{"func Unwrap[T](b Box[T]) T { b.V }", nil},
			{"func Show[T](b Box[T], n int) int { n }", nil},
			{"type R { M int }", nil},
			{"func Pick[T](q Q, x T) T { x }", nil},
			{"func Pick[T](r R, x T) T { x }", nil},
			{"test Own { assert 1 == 1 }", nil},
		}
		g.libs = append(g.libs, genLib{path: path, files: spread(r, path+"/", lib, nil, nil)})
		a := names[i]
		g.alias[path] = a
		calls = append(calls, fmt.Sprintf(`\(%s.Make().Get()) \(%s.Unwrap(%s.Wrap(%d))) \(%s.Q{N: 1}.Show(100)) \(%s.Wrap(true).Show(200)) \(%s.Q{N: 1}.Pick(7)) \(%s.R{M: 1}.Pick(8))`, a, a, a, 10*(i+1), a, a, a, a))
		want = append(want, fmt.Sprintf("%d %d 101 200 7 8", i+1, 10*(i+1)))
		root = append(root,
			genChunk{fmt.Sprintf("func q%s() fails Oops[%s.Q] { Oops[%s.Q]{V: %s.Q{N: %d}} }", a, a, a, a, i+1), []int{i}},
			genChunk{fmt.Sprintf("func box%s() fails Oops[%s.Box[int]] { Oops[%s.Box[int]]{V: %s.Wrap(%d)} }", a, a, a, a, i+1), []int{i}},
			genChunk{fmt.Sprintf("test Q%s { try q%s() }", a, a), nil},
			genChunk{fmt.Sprintf("test Box%s { try box%s() }", a, a), nil},
			genChunk{fmt.Sprintf("test Prop%s(n int) { assert %s.Q{N: n}.Get() == n }", a, a), []int{i}},
		)
	}
	all := make([]int, len(paths))
	for i := range all {
		all[i] = i
	}
	calls = append(calls, `\(P{N: 3}.Get()) \(P{N: 3}.Show(4)) \(P{N: 0}.Pick(5)) \(S.Pick(6))`)
	want = append(want, "3 4 5 6")
	root = append(root,
		genChunk{"type lib {}", nil},
		genChunk{"func main() needs Console { Console.Println(\"" + strings.Join(calls, " ") + "\") }", all},
		genChunk{"type Oops[T] { V T }", nil},
		genChunk{"type P { N int }", nil},
		genChunk{"func Get(p P) int { p.N }", nil},
		genChunk{"func Show(p P, n int) int { n }", nil},
		genChunk{"type S {}", nil},
		genChunk{"func Pick[T](p P, x T) T { x }", nil},
		genChunk{"func Pick[T](s S, x T) T { x }", nil},
		// An inline variant whose name is a Go name an overload of Pick
		// can take: the naming pass names inline variants from the type
		// list, so the order of that list is visible in the output.
		genChunk{"type U = Pick_1 { N int } | Other { M int }", nil},
		genChunk{"type Bad {}", nil},
		genChunk{"type Hold[E] { F func() fails E }", nil},
		genChunk{"type Outer { V Hold[Bad] }", nil},
		genChunk{"func bad() fails Bad { Bad }", nil},
		genChunk{"func hold() fails Hold[Bad] { Hold[Bad]{F: bad} }", nil},
		genChunk{"func outer() fails Outer { Outer{V: Hold[Bad]{F: bad}} }", nil},
		genChunk{"type Pair[T, E] { V T, F func() fails E }", nil},
		genChunk{"func pair() fails Pair[int, Bad] { Pair[int, Bad]{V: 1, F: bad} }", nil},
		genChunk{"type Job[R] { Run func() needs R }", nil},
		genChunk{"type Late { J Job[Console] }", nil},
		genChunk{"func say() needs Console { Console.Println(\"x\") }", nil},
		genChunk{"func late() fails Late { Late{J: Job[Console]{Run: say}} }", nil},
		genChunk{"test THold { try hold() }", nil},
		genChunk{"test TOuter { try outer() }", nil},
		genChunk{"test TPair { try pair() }", nil},
		genChunk{"test TLate { try late() }", nil},
		genChunk{"test Prop(n int) { assert n + 0 == n }", nil},
		genChunk{"law Ident(n int) { n + 0 == n }", nil},
	)
	g.root = spread(r, "", root, names, paths)
	g.want = strings.Join(want, " ") + "\n"
	return g
}

// permuteImports reorders the import lines at the head of src, which are
// all of one length, so no later declaration moves.
func permuteImports(r *rand.Rand, src string) string {
	lines := strings.SplitAfter(src, "\n")
	n := 0
	for n < len(lines) && strings.HasPrefix(lines[n], "import ") {
		n++
	}
	head := lines[:n]
	r.Shuffle(len(head), func(i, j int) { head[i], head[j] = head[j], head[i] })
	return strings.Join(lines, "")
}

// check checks g, each package's files in the order r gives (nil: as
// generated), and lists the root unit's imports in that order too.
func (g genProgram) check(t *testing.T, r *rand.Rand) Unit {
	t.Helper()
	shuffled := func(files []pkgSource) []pkgSource {
		out := append([]pkgSource(nil), files...)
		if r != nil {
			r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			for i := range out {
				out[i].text = permuteImports(r, out[i].text)
			}
		}
		return out
	}
	imp := pkgImporter{}
	var units []Unit
	for _, lib := range g.libs {
		u := checkPackage(t, lib.path, shuffled(lib.files), nil)
		imp[lib.path] = u.Package
		units = append(units, u)
	}
	root := checkPackage(t, "main", shuffled(g.root), imp)
	if r != nil {
		r.Shuffle(len(units), func(i, j int) { units[i], units[j] = units[j], units[i] })
	}
	root.Imports = units
	return root
}

// permuteUnit lists u's files, and its imports and their files, in the
// order r gives, over the same checked packages.
func permuteUnit(r *rand.Rand, u Unit) Unit {
	files := func(u Unit) Unit {
		order := r.Perm(len(u.Files))
		out := u
		out.Files, out.Sources = nil, nil
		for _, k := range order {
			out.Files = append(out.Files, u.Files[k])
			out.Sources = append(out.Sources, u.Sources[k])
		}
		return out
	}
	out := files(u)
	out.Imports = nil
	for _, k := range r.Perm(len(u.Imports)) {
		out.Imports = append(out.Imports, files(u.Imports[k]))
	}
	return out
}

// emitted is every file the run build and the test build emit, by build
// and path.
func emitted(t *testing.T, name string, u Unit) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tests := range []bool{false, true} {
		for _, f := range mustLowerUnit(t, name, u, tests).files {
			out[fmt.Sprintf("tests=%v/%s", tests, f.Path)] = string(f.Data)
		}
	}
	return out
}

// sameEmission fails t at the first line where got differs from want.
func sameEmission(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	for path, w := range want {
		g, ok := got[path]
		if !ok {
			t.Fatalf("%s: emits no %s", what, path)
		}
		if g == w {
			continue
		}
		wl, gl := strings.Split(w, "\n"), strings.Split(g, "\n")
		for i := range wl {
			if i >= len(gl) || wl[i] != gl[i] {
				other := ""
				if i < len(gl) {
					other = gl[i]
				}
				t.Fatalf("%s: %s differs at line %d: %q versus %q", what, path, i+1, wl[i], other)
			}
		}
		t.Fatalf("%s: %s differs past line %d", what, path, len(wl))
	}
	if len(got) != len(want) {
		t.Fatalf("%s: emits %d files, want %d", what, len(got), len(want))
	}
}

func TestLoweringIsCanonical(t *testing.T) {
	const programs = 12
	for seed := int64(0); seed < programs; seed++ {
		g := generateProgram(seed)
		r := rand.New(rand.NewSource(seed + 1000))
		base := g.check(t, nil)
		want := emitted(t, "canonical", base)
		if seed == 0 {
			p := mustLowerUnit(t, "canonical", base, false)
			buildAndRun(t, []*lowered{p}, map[string]string{"canonical": g.want})
		}
		for i := 0; i < 10; i++ {
			sameEmission(t, fmt.Sprintf("seed %d, lowering %d of one unit", seed, i+2), want, emitted(t, "canonical", base))
		}
		for i := 0; i < 4; i++ {
			sameEmission(t, fmt.Sprintf("seed %d, unit order %d", seed, i+1), want, emitted(t, "canonical", permuteUnit(r, base)))
		}
		for i := 0; i < 4; i++ {
			sameEmission(t, fmt.Sprintf("seed %d, check order %d", seed, i+1), want, emitted(t, "canonical", g.check(t, r)))
		}
	}
}

// The round 2 verify's probe (M1): the text functions of Oops over two
// packages' Q, whose displays are both Oops[lib.Q], are named the same in
// every lowering of one checked unit.
func TestVerifyR2StableTextNames(t *testing.T) {
	a := checkPackage(t, "x/lib", []pkgSource{{"/probe/x/lib.ergo", "type Q { N int }\n"}}, nil)
	b := checkPackage(t, "y/lib", []pkgSource{{"/probe/y/lib.ergo", "type Q { M string }\n"}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/probe/text.ergo", `import a x/lib
import b y/lib
type lib {}
type Oops[T] { V T }
func f() fails Oops[a.Q] { Oops[a.Q]{V: a.Q{N: 1}} }
func g() fails Oops[b.Q] { Oops[b.Q]{V: b.Q{M: "two"}} }
func main() {}
test F { try f() }
test G { try g() }
`}}, pkgImporter{"x/lib": a.Package, "y/lib": b.Package})
	root.Imports = []Unit{a, b}
	first := emitted(t, "stable", root)
	for i := 0; i < 100; i++ {
		sameEmission(t, fmt.Sprintf("run %d", i+1), first, emitted(t, "stable", root))
	}
}
