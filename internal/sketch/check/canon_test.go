package check

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The checker hands out its lists in canonical order (canon.go): a fresh
// check of a program's files, listed in any order and with each file's
// import lines in any order, gives every list in the same order, by the
// canonical key of each element.

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
		out[cp.path] = canonKeys(p)
	}
	return out
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
					w, g := want[cp.path][kind], got[cp.path][kind]
					if kind == "InitOrder" {
						// Lets of different files that do not read each
						// other run in the order of the files (canon.go,
						// kept orders): only the dependency chain and the
						// order inside one file are the program's.
						w, g = []string{initOrderParts(w)}, []string{initOrderParts(g)}
					}
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
