package check

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The checker meets a package's files in canonical order (canon.go), so a
// fresh check of a program's files, listed in any order and with each
// file's import lines in any order, gives every list in the same order, by
// the canonical key of each element, and every set (a row, a row value, a
// catch's subjects) in the same order. Its diagnostics name the caller's
// files.

// canonSource is one generated source file.
type canonSource struct{ path, text string }

// canonPackage is one generated package and the generated packages it
// imports, by path.
type canonPackage struct {
	path  string
	files []canonSource
}

// canonChunk is one top-level declaration and the libraries (by index)
// whose import it needs.
type canonChunk struct {
	text string
	uses []int
}

// canonChunks are the declarations of every generated package: each list
// kind (types with inline variants, functions with an overload group,
// tests, a property, top-level and member laws), so each kind lands in
// several files.
var canonChunks = []string{
	"type Read {\n\tGet() int,\n\n\tlaw Same(v int) { v == v },\n}",
	"type Q { N int }",
	"func Get(q Q) int { q.N }",
	"func Make() Read { Q{N: 1} }",
	"type Box[T] { V T }",
	"func Wrap[T](v T) Box[T] { Box[T]{V: v} }",
	"type R { M int }",
	"func Pick[T](q Q, x T) T { x }",
	"func Pick[T](r R, x T) T { x }",
	"type U = Va { N int } | Vb { M int }",
	"test Own1 { assert 1 == 1 }",
	"test Own2 { assert Wrap(2).V == 2 }",
	"test Prop(n int) { assert n == n }",
	"law Top1(n int) { n + 0 == n }",
	"law Top2(n int) { n * 1 == n }",
}

// canonSpread lays chunks out over two or three files of dir, each chunk
// in a file r chooses, every file non-empty. Each file starts with the
// import lines of the libraries its chunks use, all of one length.
func canonSpread(r *rand.Rand, dir string, chunks []canonChunk, libs []string) []canonSource {
	files := 2 + r.Intn(2)
	of := make([][]canonChunk, files)
	for k, c := range r.Perm(len(chunks)) {
		f := k
		if k >= files {
			f = r.Intn(files)
		}
		of[f] = append(of[f], chunks[c])
	}
	var out []canonSource
	for f, cs := range of {
		uses := map[int]bool{}
		var head, body strings.Builder
		for _, c := range cs {
			for _, i := range c.uses {
				uses[i] = true
			}
			body.WriteString(c.text + "\n")
		}
		for i, lib := range libs {
			if uses[i] {
				fmt.Fprintf(&head, "import %s\n", lib)
			}
		}
		out = append(out, canonSource{fmt.Sprintf("/gen/%sf%d.ergo", dir, f), head.String() + body.String()})
	}
	return out
}

// canonProgram generates a program from seed: two or three libraries
// x/a, y/b, z/c and a root that imports each, every package over
// two or three files. The root also has lets in a chain whose links land
// in different files, and two lets that read nothing in one file.
func canonProgram(seed int64) []canonPackage {
	r := rand.New(rand.NewSource(seed))
	libs := []string{"x/a", "y/b", "z/c"}[:2+r.Intn(2)]
	var prog []canonPackage
	var root []canonChunk
	var calls []string
	for _, text := range canonChunks {
		root = append(root, canonChunk{text, nil})
	}
	for i, lib := range libs {
		var chunks []canonChunk
		for _, text := range canonChunks {
			chunks = append(chunks, canonChunk{text, nil})
		}
		chunks = append(chunks, canonChunk{fmt.Sprintf("let Base int = %d", i), nil})
		prog = append(prog, canonPackage{lib, canonSpread(r, lib+"/", chunks, nil)})
		a := string(rune('a' + i))
		calls = append(calls, fmt.Sprintf(`\(%s.Make().Get())`, a))
		root = append(root, canonChunk{fmt.Sprintf("func use%s() int { %s.Base + %s.Q{N: 1}.Pick(2) }", a, a, a), []int{i}})
	}
	all := make([]int, len(libs))
	for i := range all {
		all[i] = i
	}
	root = append(root,
		canonChunk{`func main() needs Console { Console.Println("` + strings.Join(calls, " ") + `") }`, all},
		canonChunk{"let L0 int = 1", nil},
		canonChunk{"let L1 int = L0 + 1", nil},
		canonChunk{"let L2 int = L1 + 1", nil},
		canonChunk{"let L3 int = L2 + 1", nil},
		canonChunk{"let Free1 int = 1\nlet Free2 int = 2", nil},
	)
	return append(prog, canonPackage{"main", canonSpread(r, "", root, libs)})
}

// canonImporter serves the generated libraries and the standard library.
type canonImporter map[string]*Package

func (m canonImporter) Import(path string) (*Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return Std.Import(path)
}

// canonShuffles is the number of shuffled listings of each program.
const canonShuffles = 4

// canonListing lists a package's files for a fresh check: as generated
// (order 0), reversed (1), or shuffled by r with each file's import lines
// shuffled too (2 and on).
func canonListing(r *rand.Rand, files []canonSource, order int) []canonSource {
	out := slices.Clone(files)
	switch {
	case order == 1:
		slices.Reverse(out)
	case order > 1:
		r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		for i := range out {
			lines := strings.SplitAfter(out[i].text, "\n")
			n := 0
			for n < len(lines) && strings.HasPrefix(lines[n], "import ") {
				n++
			}
			r.Shuffle(n, func(a, b int) { lines[a], lines[b] = lines[b], lines[a] })
			out[i].text = strings.Join(lines, "")
		}
	}
	return out
}

// canonLists checks prog freshly, each package's files listed by order,
// and returns each package's lists by kind, each element by its canonical
// key: its file's key, the offset of its name, and its name.
func canonLists(t *testing.T, prog []canonPackage, r *rand.Rand, order int) map[string]map[string][]string {
	t.Helper()
	imp := canonImporter{}
	out := map[string]map[string][]string{}
	for _, cp := range prog {
		var files []*syntax.File
		for _, s := range canonListing(r, cp.files, order) {
			f, err := syntax.Parse(s.text)
			if err != nil {
				t.Fatalf("%s: %v\n%s", s.path, err, s.text)
			}
			files = append(files, f)
		}
		p, ds := Check(cp.path, files, imp)
		if len(ds) != 0 {
			t.Fatalf("package %s: %v", cp.path, ds)
		}
		imp[cp.path] = p
		inMetOrder(t, p)
		out[cp.path] = canonKeys(p)
	}
	return out
}

// inMetOrder fails t unless the checker met p's files in canonical order.
// No step after the run re-sorts a list (round 8 removed canonicalOrder):
// each list is in the order the checker collected it, which the order of
// the files fixes, so TestCheckListsAreCanonical compares the lists
// themselves across listings.
func inMetOrder(t *testing.T, p *Package) {
	t.Helper()
	var met []*syntax.File
	for _, f := range p.files {
		met = append(met, f.syntax)
	}
	for i, k := range canonicalFiles(met) {
		if i != k {
			t.Fatalf("package %s: files not met in canonical order: %v", p.Path, canonicalFiles(met))
		}
	}
}

// canonKeys is each list of p by kind, each element by its canonical key.
func canonKeys(p *Package) map[string][]string {
	key := func(f *fileInfo, pos syntax.Pos, name string) string {
		return fmt.Sprintf("%q@%d %s", fileKey(f.syntax), pos, name)
	}
	lists := map[string][]string{}
	for _, f := range p.files {
		lists["files"] = append(lists["files"], fmt.Sprintf("%q", fileKey(f.syntax)))
	}
	for _, f := range p.Funcs {
		lists["Funcs"] = append(lists["Funcs"], key(f.file, f.Pos, f.Name))
	}
	for _, tn := range p.Types {
		lists["Types"] = append(lists["Types"], key(tn.file, tn.Pos, tn.Name))
	}
	for _, x := range p.Tests {
		lists["Tests"] = append(lists["Tests"], key(x.file, x.Decl.Name.Pos(), x.Name))
	}
	for _, l := range p.Laws {
		lists["Laws"] = append(lists["Laws"], key(l.file, l.Decl.Name.Pos(), l.Name))
	}
	for _, in := range p.Info.InitOrder {
		var names []string
		for _, v := range in.Vars {
			names = append(names, v.Name)
		}
		lists["InitOrder"] = append(lists["InitOrder"], strings.Join(names, ","))
	}
	return lists
}

// initOrderParts is the part of an init order the program fixes: the
// order of the chain L0 to L3, whose links are in different files, and the
// order of Free1 and Free2, which are in one file and read nothing.
func initOrderParts(order []string) string {
	var chain, free []string
	for _, name := range order {
		switch {
		case strings.HasPrefix(name, "L"):
			chain = append(chain, name)
		case strings.HasPrefix(name, "Free"):
			free = append(free, name)
		}
	}
	return strings.Join(chain, " ") + " | " + strings.Join(free, " ")
}

// canonKinds are the lists the property compares.
var canonKinds = []string{"files", "Funcs", "Types", "Tests", "Laws", "InitOrder"}

func TestCheckListsAreCanonical(t *testing.T) {
	const programs = 10
	// spans counts, for each kind, the packages whose list holds elements
	// of two or more files, where a discovery order would show.
	spans := map[string]int{}
	for seed := int64(0); seed < programs; seed++ {
		prog := canonProgram(seed)
		want := canonLists(t, prog, nil, 0)
		for path, lists := range want {
			for _, kind := range canonKinds[:5] {
				files := map[string]bool{}
				for _, k := range lists[kind] {
					files[strings.SplitN(k, "@", 2)[0]] = true
				}
				if len(files) > 1 {
					spans[kind]++
				}
			}
			if got := initOrderParts(lists["InitOrder"]); path == "main" && got != "L0 L1 L2 L3 | Free1 Free2" {
				t.Fatalf("seed %d: init order %v: the chain and the lets of one file are out of order", seed, lists["InitOrder"])
			}
		}
		r := rand.New(rand.NewSource(seed))
		for order := 1; order < 2+canonShuffles; order++ {
			got := canonLists(t, prog, r, order)
			for _, cp := range prog {
				for _, kind := range canonKinds {
					// InitOrder too, whole: lets of different files that
					// do not read each other run in the order in which
					// the checker meets the files, the canonical order.
					w, g := want[cp.path][kind], got[cp.path][kind]
					if !slices.Equal(w, g) {
						t.Fatalf("seed %d, listing %d, package %s: %s differ:\n%s\nwant:\n%s", seed, order, cp.path, kind, strings.Join(g, "\n"), strings.Join(w, "\n"))
					}
				}
			}
		}
	}
	for _, kind := range canonKinds[:5] {
		if spans[kind] == 0 {
			t.Fatalf("no generated package spreads %s over two files; the property cannot see its order", kind)
		}
	}
}

// setSources are n files, each declaring the capability Ck and the error
// type Ek with a function whose rows name only them, and main.ergo, whose
// rows name each, with a catch over every Ek and a row value (case err) of
// the rest. Which single-member row is interned first follows the order of
// the files.
func setSources(n int) []string {
	var files, caps, errs, calls, tries []string
	for k := 0; k < n; k++ {
		files = append(files, fmt.Sprintf("type C%[1]d { Get() int }\nfunc use%[1]d() int needs C%[1]d { C%[1]d.Get() }\ntype E%[1]d { N int }\nfunc fail%[1]d() int fails E%[1]d { E%[1]d{N: 1} }\n", k))
		caps = append(caps, fmt.Sprintf("C%d", k))
		errs = append(errs, fmt.Sprintf("E%d", k))
		calls = append(calls, fmt.Sprintf("use%d()", k))
		tries = append(tries, fmt.Sprintf("\tlet _ = try fail%d()\n", k))
	}
	main := fmt.Sprintf("func all() int needs %s { %s }\nfunc each() int fails %s {\n%s\t0\n}\nfunc rest() int { each() catch { case E0: 0; case err: keep(err) } }\nfunc keep[T](v T) int { 1 }\n",
		strings.Join(caps, " | "), strings.Join(calls, " + "), strings.Join(errs, " | "), strings.Join(tries, ""))
	return append(files, main)
}

// canonOrders is every order of 0..n-1.
func canonOrders(n int) [][]int {
	if n == 0 {
		return [][]int{nil}
	}
	var out [][]int
	for _, p := range canonOrders(n - 1) {
		for i := 0; i <= len(p); i++ {
			out = append(out, append(append(slices.Clone(p[:i]), n-1), p[i:]...))
		}
	}
	return out
}

// parseAll parses each source, in order.
func parseAll(t *testing.T, srcs []string) []*syntax.File {
	t.Helper()
	var out []*syntax.File
	for _, src := range srcs {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		out = append(out, f)
	}
	return out
}

// listed is srcs in the order given.
func listed(srcs []string, order []int) []string {
	var out []string
	for _, k := range order {
		out = append(out, srcs[k])
	}
	return out
}

// mainSets checks files in the order given and returns each set of main's
// result, element by element in the checker's order, by String().
func mainSets(t *testing.T, files []string) map[string]string {
	t.Helper()
	p, ds := Check("main", parseAll(t, files), nil)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	text := func(ts []Type) string {
		var out []string
		for _, x := range ts {
			out = append(out, x.String())
		}
		return strings.Join(out, " , ")
	}
	sets := map[string]string{}
	for _, f := range p.Funcs {
		switch f.Name {
		case "all":
			sets["uses"] = text(f.Sig.Uses.Elems())
		case "each":
			sets["errors"] = text(f.Sig.Errors.Elems())
		}
	}
	for n, m := range p.Info.Matches {
		if _, ok := n.(*syntax.CatchExpr); ok {
			sets["catch"] = text(m.Subjects)
		}
	}
	for id, o := range p.Info.Defs {
		if v, ok := o.(*Var); ok && id.Name == "err" {
			u, ok := v.Type.(*Union)
			if !ok {
				t.Fatalf("err is %v, not a row value", v.Type)
			}
			sets["row value"] = text(u.Variants)
			sets["row value text"] = u.String()
		}
	}
	if len(sets) != 5 {
		t.Fatalf("found the sets %v, want uses, errors, catch and row value", sets)
	}
	return sets
}

// TestCheckSetsAreCanonical: for every order of the files that intern a
// set's members, main's among them, each set of the check result (a row's
// elements, a catch's subjects, a row value's variants and its text) is
// the same, element by element in the checker's own order. Round 7 sorted
// the sets after the fact; round 8 derives them from the canonical input,
// so there is no view: the raw fields are the order.
func TestCheckSetsAreCanonical(t *testing.T) {
	const n = 4
	files := setSources(n)
	var want map[string]string
	for _, order := range canonOrders(n + 1) {
		got := mainSets(t, listed(files, order))
		if want == nil {
			want = got
			continue
		}
		for kind, w := range want {
			if got[kind] != w {
				t.Fatalf("order %v: %s\n%s\nwant\n%s", order, kind, got[kind], w)
			}
		}
	}
}

// fileDiags checks srcs in the order given and returns each diagnostic
// with the text of the caller's file it names (by its label in labels),
// in the order Check returns them. It fails t unless they are sorted by
// the caller's index and each position is the file's own.
func fileDiags(t *testing.T, srcs []string, labels map[string]string) []string {
	t.Helper()
	files := parseAll(t, srcs)
	_, ds := Check("main", files, nil)
	var out []string
	for i, d := range ds {
		if i > 0 && ds[i-1].File > d.File {
			t.Fatalf("diagnostics not sorted by the caller's file: %v", ds)
		}
		f := files[d.File]
		line, col := f.Position(syntax.Pos(d.Offset))
		if line != d.Line || col != d.Column {
			t.Fatalf("%v: position %d:%d is not that of offset %d in the file it names (%d:%d)", d, d.Line, d.Column, d.Offset, line, col)
		}
		out = append(out, fmt.Sprintf("%s %s", labels[srcs[d.File]], d))
	}
	slices.Sort(out)
	return out
}

// diagSources are files with diagnostics in several of them: an unknown
// name, a result of the wrong type, a type declared in two files (the
// later file as met has the diagnostic) and unused imports.
var diagSources = []string{
	"type A {}\nfunc a() int { missing }\n",
	"import log\ntype B {}\nfunc b() int { \"s\" }\n",
	"type A {}\nfunc c() int { 1 }\n",
	"import trace\nfunc main() {}\n",
}

// TestCheckDiagnosticsNameCallerFiles: for every order of the files, Check
// returns the same diagnostics, each naming the caller's index of the same
// file (by its text), at the same place in it. The duplicate type is
// reported in the same one of its two files for every order.
func TestCheckDiagnosticsNameCallerFiles(t *testing.T) {
	labels := map[string]string{}
	for i, s := range diagSources {
		labels[s] = fmt.Sprintf("f%d", i)
	}
	var want []string
	for _, order := range canonOrders(len(diagSources)) {
		got := fileDiags(t, listed(diagSources, order), labels)
		if want == nil {
			want = got
			if len(want) < 4 {
				t.Fatalf("want diagnostics in several files, got %v", want)
			}
			continue
		}
		if !slices.Equal(got, want) {
			t.Fatalf("order %v: diagnostics\n%s\nwant\n%s", order, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// TestCheckFileKeyIsTotal: files that a partial key would tie are met in
// one order. (1) Two valid files whose first declarations have one name
// (overloads of Get) and which intern their error types in file order:
// main's row is the same for both orders. (2) Two files whose texts differ
// only in the order of their import lines (one fileKey) and which both
// declare A: the duplicate is reported in the same file, so the full text
// breaks the tie. (3) Two files of the same text: the caller's order of
// the two changes nothing.
func TestCheckFileKeyIsTotal(t *testing.T) {
	overloads := []string{
		"func Get(q Q0) int { 0 }\ntype Q0 {}\ntype E0 { N int }\nfunc fail0() int fails E0 { E0{N: 1} }\n",
		"func Get(q Q1) int { 1 }\ntype Q1 {}\ntype E1 { N int }\nfunc fail1() int fails E1 { E1{N: 1} }\n",
		"func each() int fails E0 | E1 {\n\tlet _ = try fail1()\n\tlet _ = try fail0()\n\t0\n}\nfunc rest() int { each() catch { case err: keep(err) } }\nfunc keep[T](v T) int { 1 }\nfunc all() needs Console {}\n",
	}
	var want map[string]string
	for _, order := range canonOrders(len(overloads)) {
		got := mainSets(t, listed(overloads, order))
		if want == nil {
			want = got
		} else if got["errors"] != want["errors"] || got["row value"] != want["row value"] || got["catch"] != want["catch"] {
			t.Fatalf("order %v: overload files tie: %v, want %v", order, got, want)
		}
	}
	for _, srcs := range [][]string{
		{"import log\nimport trace\ntype A {}\n", "import trace\nimport log\ntype A {}\n", "func main() {}\n"},
		{"type A {}\n", "type A {}\n", "func main() {}\n"},
	} {
		labels := map[string]string{srcs[0]: "first", srcs[1]: "second", srcs[2]: "main"}
		var want []string
		for _, order := range canonOrders(len(srcs)) {
			got := fileDiags(t, listed(srcs, order), labels)
			if want == nil {
				want = got
				if len(got) == 0 {
					t.Fatal("want the duplicate reported")
				}
			} else if !slices.Equal(got, want) {
				t.Fatalf("order %v: diagnostics\n%s\nwant\n%s", order, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	}
}
