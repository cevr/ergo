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
// lines in another order, emits the same Go files, byte for byte.

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

// generateProgram builds a program from seed: two to four libraries whose
// paths end alike (x/lib, y/lib, ...), each with the same declarations
// over one or two files: an operation type Read that Q converts to (an
// adapter), a generic Box with functions over it, and a function Show over
// each of Q and Box, in two files when the library has two. The root names each
// library's Box[int] (instances whose displays coincide), and an error type
// Oops over each library's Q and Box[int] (error-text instances), over one
// or two files, whose import lines come in a seeded order.
func generateProgram(seed int64) genProgram {
	r := rand.New(rand.NewSource(seed))
	paths := []string{"x/lib", "y/lib", "z/lib", "w/lib"}[:2+r.Intn(3)]
	names := []string{"a", "b", "c", "d"}
	g := genProgram{alias: map[string]string{}}
	var calls, errs, tests []string
	var want []string
	for i, path := range paths {
		n := fmt.Sprint(i + 1)
		ops := "type Read { Get() int }\ntype Q { N int }\nfunc Get(q Q) int { q.N }\nfunc Make() Read { Q{N: " + n + "} }\nfunc Show(q Q, n int) int { n + q.N }\n"
		box := "type Box[T] { V T }\nfunc Wrap[T](v T) Box[T] { Box[T]{V: v} }\nfunc Unwrap[T](b Box[T]) T { b.V }\nfunc Show[T](b Box[T], n int) int { n }\n"
		lib := genLib{path: path}
		if r.Intn(2) == 0 {
			lib.files = []pkgSource{{"/gen/" + path + "/lib.ergo", ops + box}}
		} else {
			lib.files = []pkgSource{{"/gen/" + path + "/ops.ergo", ops}, {"/gen/" + path + "/box.ergo", box}}
		}
		g.libs = append(g.libs, lib)
		a := names[i]
		g.alias[path] = a
		calls = append(calls, fmt.Sprintf(`\(%s.Make().Get()) \(%s.Unwrap(%s.Wrap(%d))) \(%s.Q{N: 1}.Show(100)) \(%s.Wrap(true).Show(200))`, a, a, a, 10*(i+1), a, a))
		want = append(want, fmt.Sprintf("%d %d 101 200", i+1, 10*(i+1)))
		errs = append(errs,
			fmt.Sprintf("func q%s() fails Oops[%s.Q] { Oops[%s.Q]{V: %s.Q{N: %d}} }\n", a, a, a, a, i+1),
			fmt.Sprintf("func box%s() fails Oops[%s.Box[int]] { Oops[%s.Box[int]]{V: %s.Wrap(%d)} }\n", a, a, a, a, i+1))
		tests = append(tests, fmt.Sprintf("test Q%s { try q%s() }\ntest Box%s { try box%s() }\n", a, a, a, a))
	}
	imports := func() string {
		order := r.Perm(len(paths))
		var b strings.Builder
		for _, k := range order {
			fmt.Fprintf(&b, "import %s %s\n", names[k], paths[k])
		}
		return b.String()
	}
	mainDecls := "type lib {}\nfunc main() needs Console { Console.Println(\"" + strings.Join(calls, " ") + "\") }\n"
	errDecls := "type Oops[T] { V T }\n" + strings.Join(errs, "") + strings.Join(tests, "")
	if r.Intn(2) == 0 {
		g.root = []pkgSource{{"/gen/main.ergo", imports() + mainDecls + errDecls}}
	} else {
		g.root = []pkgSource{{"/gen/main.ergo", imports() + mainDecls}, {"/gen/errors.ergo", imports() + errDecls}}
	}
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

// emitted is every file a lowering emits, by path.
func emitted(t *testing.T, name string, u Unit) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range mustLowerUnit(t, name, u, true).files {
		out[f.Path] = string(f.Data)
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
	const programs = 8
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
