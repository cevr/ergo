package check_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The file-placement guard (stage E2 rounds 2 and 3). A diagnostic names
// the file it is reported in (Diagnostic.File), and the checker takes that
// file from the file the running pass selected. A pass that reports without
// first selecting the file it inspects reports against whichever file an
// earlier pass visited last, at an offset of the wrong source: the editor
// and the CLI then point at a construct that is not there (E1 round 2 M2,
// E2 round 1 M1).
//
// Three checks hold the class. The checker asserts at every report that the
// reported node lies in the selected file, and panics with an internal
// compiler error otherwise (owner.go). The driver clears the selection
// before each pass, so a pass that reports without selecting fails that
// assertion in every test that reaches the report, one file or many. A pass
// that selects the wrong file still passes it in a one-file package, where
// every selection is the right file: this guard supplies the second file,
// beside each program (TestDiagnosticsStayInTheirFile) and between a
// program's own declarations (TestDeclarationSplitStaysInItsFile).
//
// The invariant: adding a neutral file to a package moves no diagnostic.
// Every program the checker has is checked alone, then as file 0 and as
// file 1 of a two-file package beside neutralFile:
//   - the corpus (the acceptance rule mirror, the fixture mirror, the
//     prototype's fixtures and the parser's corpus);
//   - every program written inside the checker's own tests (testPrograms);
//   - testdata/filesplit, one negative program for each package-level pass
//     that no other program reaches;
//   - the frame-need matrix and the multi-file regression of E2 round 1 M1;
//   - the provenance guard's fixtures, inline-variant parameter matrix and
//     tail programs (owner_e2_test.go, E2b).
//
// No placement may panic. Each placement must give exactly the one-file
// diagnostics, each naming the program's file, at the same line, column,
// offset and length, with the same code and message. A positive program
// must stay clean, so the neutral file adds no diagnostic of its own either.

// neutralFile declares a type, a package let, a function and a method-like
// use of each, under names no program uses, so every declaration pass
// visits it, and it interacts with no program.
const neutralFile = `type SplitNeutral { V int }

let splitNeutralZero = SplitNeutral{V: 0}

func splitNeutral(n SplitNeutral) int { n.V + splitNeutralZero.V }
`

// m1Program is E2 round 1 M1's reproduction: a context with in a package
// let, refused by checkFrames (S108 at 2:9).
const m1Program = "import log\nlet x = with log.Annotate(\"k\", \"v\") { 1 }\n"

// splitSource is one program of the guard and where it comes from.
type splitSource struct {
	src, kind string
}

// splitSources returns every program of the guard by name.
func splitSources(t testing.TB) map[string]splitSource {
	out := map[string]splitSource{}
	seen := map[string]bool{}
	add := func(name, kind, src string) {
		if seen[src] || strings.Contains(src, "plitNeutral") {
			return // a program once; the neutral file itself is no program
		}
		seen[src] = true
		out[name] = splitSource{src, kind}
	}
	for name, src := range corpus(t) {
		add(name, "corpus", src)
	}
	for name, src := range splitFixtures(t) {
		add(name, "filesplit", src)
	}
	for name, src := range check.FrameMatrixNegatives() {
		add("frame matrix: "+name, "frames", src)
	}
	add("E2 r1 M1", "frames", m1Program)
	// The provenance guard's programs come through an explicit seam, not
	// the literal harvest: its matrix is built, not written (owner_e2_test.go).
	for name, src := range check.OwnerGuardPrograms() {
		add("owner: "+name, "owner", src)
	}
	for i, src := range testPrograms(t) {
		add(fmt.Sprintf("test literal %04d", i), "test", src)
	}
	return out
}

// splitFixtures returns testdata/filesplit by path.
func splitFixtures(t testing.TB) map[string]string {
	paths, err := filepath.Glob("testdata/filesplit/*.ergo")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[p] = string(data)
	}
	return out
}

// testPrograms returns every program written inside the checker's tests:
// each Go string literal of check/*_test.go, and each string const or var
// built from literals, that parses as Ergo and has a declaration of any
// kind (a function, a type, a let, a context, a machine, a test...). No
// spelling filter narrows it: a string is a program when the parser says
// so. This is the extraction of lower's checkCorpus, without its
// sampling. Programs built at run time (fmt.Sprintf, loops) are not
// strings of the source; a guard that needs them supplies them through an
// explicit seam (check.OwnerGuardPrograms, check.FrameMatrixNegatives).
func testPrograms(t testing.TB) []string {
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var out []string
	seen := map[string]bool{}
	skipped := map[string]int{}
	keep := func(s string) {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if seen[s] {
			return
		}
		seen[s] = true
		f, err := syntax.Parse(s)
		switch {
		case err != nil:
			skipped["not Ergo"]++
		case len(f.Decls) == 0:
			skipped["no declaration"]++
		default:
			out = append(out, s)
		}
	}
	for _, p := range paths {
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					keep(s)
				}
			}
			return true
		})
		for _, s := range builtStrings(f) {
			keep(s)
		}
	}
	t.Logf("test literals: %d strings, %d programs; skipped %d not Ergo, %d with no declaration", len(seen), len(out), skipped["not Ergo"], skipped["no declaration"])
	return out
}

// builtStrings returns the values of the file's string consts and vars
// whose values are sums of literals and earlier such names.
func builtStrings(f *ast.File) []string {
	known := map[string]string{}
	var out []string
	var eval func(e ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		case *ast.ParenExpr:
			return eval(e.X)
		case *ast.Ident:
			s, ok := known[e.Name]
			return s, ok
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return "", false
			}
			a, ok1 := eval(e.X)
			b, ok2 := eval(e.Y)
			return a + b, ok1 && ok2
		}
		return "", false
	}
	record := func(name string, value ast.Expr) {
		if s, ok := eval(value); ok {
			known[name] = s
			if _, lit := value.(*ast.BasicLit); !lit {
				out = append(out, s)
			}
		}
	}
	ast.Inspect(f, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.GenDecl:
			if x.Tok != token.CONST && x.Tok != token.VAR {
				return true
			}
			for _, spec := range x.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						record(name.Name, vs.Values[i])
					}
				}
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE && len(x.Lhs) == len(x.Rhs) {
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						record(id.Name, x.Rhs[i])
					}
				}
			}
		}
		return true
	})
	return out
}

// checkPlaced checks files as one package and returns the diagnostics, or
// the checker's internal compiler error.
func checkPlaced(files []*syntax.File) (ds []check.Diagnostic, ice string) {
	defer func() {
		if r := recover(); r != nil {
			ice = fmt.Sprint(r)
		}
	}()
	_, ds = check.Check("main", files, nil)
	return ds, ""
}

// R2' groups can span files, and one declaration can belong to both an
// intrinsic and an orphan group. Each report must select its own first
// declaration's file, including when consecutive reports change files.
// The messages below are explicit rule examples, independent of the
// checker's cycle graph and the generated cycle oracle.
func TestRecursiveGroupsNameTheirFile(t *testing.T) {
	type report struct {
		file          int
		name, message string
	}
	for _, tc := range []struct {
		name  string
		files [2]string
		want  []report
	}{
		{
			"intrinsic-record",
			[2]string{"type B { Next A }\n", "\n// The first name is in this file.\ntype A { Next B }\n"},
			[]report{{1, "A", "A contains itself by value with B, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"}},
		},
		{
			"intrinsic-defined",
			[2]string{"type Z A\n", "\ntype A Z\n"},
			[]report{{1, "A", "A is defined over itself with Z (types.md, Defined types)"}},
		},
		{
			"mixed-tuple-and-definition",
			[2]string{"type Tuple (Link, int)\n", "\ntype Link Tuple\n"},
			[]report{{1, "Link", "Link contains itself by value with Tuple, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"}},
		},
		{
			"intrinsic-mutual-generics",
			[2]string{"type H[T] { Next G[T] }\n", "\ntype G[T] { Next H[T] }\n"},
			[]report{{1, "G", "G contains itself by value with H, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"}},
		},
		{
			"orphan-through-finite-wrapper",
			[2]string{"type Wrapper[T] { V T }\ntype Zed { W Wrapper[Alpha] }\n", "\ntype Alpha { W Zed }\n"},
			[]report{{1, "Alpha", "Alpha contains itself by value with Zed, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"}},
		},
		{
			"orphan-concrete-generics",
			[2]string{"type G[T] { Next H[int] }\n", "\ntype H[U] { Next G[int] }\n"},
			[]report{{0, "G", "G contains itself by value with H, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"}},
		},
		{
			"intrinsic-and-orphan-share-a-declaration",
			[2]string{"type D2[T] { F0 D0[T], F1 D1[int] }\ntype D1[T] { F0 D2[int] }\n", "\ntype D0[T] { F0 D2[T] }\n"},
			[]report{
				{1, "D0", "D0 contains itself by value with D2, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"},
				{0, "D1", "D1 contains itself by value with D2, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"},
			},
		},
		{
			"independent-groups-change-files",
			[2]string{"type B A\ntype C { Next D }\n", "\ntype A B\ntype D { Next C }\n"},
			[]report{
				{1, "A", "A is defined over itself with B (types.md, Defined types)"},
				{0, "C", "C contains itself by value with D, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"},
			},
		},
	} {
		for order := range 2 {
			t.Run(fmt.Sprintf("%s/order=%d", tc.name, order), func(t *testing.T) {
				texts := [2]string{tc.files[order], tc.files[1-order]}
				files := make([]*syntax.File, len(texts))
				for i, text := range texts {
					var err error
					files[i], err = syntax.Parse(text)
					if err != nil {
						t.Fatal(err)
					}
				}
				got, ice := checkPlaced(files)
				if ice != "" {
					t.Fatal(ice)
				}
				var want []check.Diagnostic
				for _, r := range tc.want {
					file := r.file ^ order
					text := texts[file]
					decl := strings.Index(text, "type "+r.name)
					if decl < 0 {
						t.Fatalf("expected declaration %s in file %d", r.name, file)
					}
					offset := decl + len("type ")
					before := text[:offset]
					want = append(want, check.Diagnostic{
						Code: "S108", Message: r.message, File: file,
						Offset: offset, Length: len(r.name),
						Line: strings.Count(before, "\n") + 1, Column: offset - strings.LastIndexByte(before, '\n'),
					})
				}
				// Check lists them in the canonical order of their files:
				// for unnamed files with no imports, by text.
				sort.Slice(want, func(i, j int) bool {
					if want[i].File != want[j].File {
						return texts[want[i].File] < texts[want[j].File]
					}
					return want[i].Offset < want[j].Offset
				})
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("cross-file recursive groups:\n got: %+v\nwant: %+v", got, want)
				}
			})
		}
	}
}

// The package-pattern loop temporarily selects a later duplicate context,
// then must restore the pattern's file before declaring its next name.
// Unit types also share the context namespace, with the later file blamed.
// The later file is the later by name (check/canon.go; pkg-lower round 9),
// as in Go, not the later as listed, so both listings blame the same file
// at the same place. files[i] is named fi.ergo; want names each file by
// its index in files.
func TestPackageNameReportsNameTheirFile(t *testing.T) {
	const duplicate = "W is already declared in this package"
	const boundTwice = "W is bound twice in this pattern (rows.md, Totality 1)"
	type report struct {
		file, offset int
		message      string
	}
	for _, tc := range []struct {
		name  string
		files [2]string
		want  []report
	}{
		{
			"pattern-restore", [2]string{"let (W, W) = (1, 2)\n", "let W = context {}\n"},
			[]report{{0, 8, duplicate}, {0, 8, boundTwice}, {1, 4, duplicate}},
		},
		{
			"unit-context", [2]string{"type W {}\n", "let W = context {}\n"},
			[]report{{1, 4, duplicate}},
		},
	} {
		for order := range 2 {
			t.Run(fmt.Sprintf("%s/order=%d", tc.name, order), func(t *testing.T) {
				texts := [2]string{tc.files[order], tc.files[1-order]}
				var files []*syntax.File
				for i, src := range texts {
					f, err := syntax.Parse(src)
					if err != nil {
						t.Fatal(err)
					}
					f.Name = fmt.Sprintf("f%d.ergo", i^order)
					files = append(files, f)
				}
				got, ice := checkPlaced(files)
				if ice != "" {
					t.Fatal(ice)
				}
				var want []check.Diagnostic
				for _, r := range tc.want {
					file := r.file // the file's index as listed
					if order == 1 {
						file = 1 - file
					}
					want = append(want, check.Diagnostic{
						Code: "S108", Message: r.message, File: file,
						Offset: r.offset, Length: 1, Line: 1, Column: r.offset + 1,
					})
				}
				// Check lists them in the canonical order of their files:
				// by name, and files[i] is named f(i^order).ergo.
				sort.SliceStable(want, func(i, j int) bool {
					a, b := want[i], want[j]
					if a.File != b.File {
						return a.File^order < b.File^order
					}
					if a.Offset != b.Offset {
						return a.Offset < b.Offset
					}
					return a.Message < b.Message
				})
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("package names across files:\n got: %+v\nwant: %+v", got, want)
				}
			})
		}
	}
}

func TestDiagnosticsStayInTheirFile(t *testing.T) {
	neutral, err := syntax.Parse(neutralFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ds := check.Check("main", []*syntax.File{neutral}, nil); len(ds) != 0 {
		t.Fatalf("the neutral file alone: %v", ds)
	}
	sources := splitSources(t)
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	programs := map[string]int{}
	negatives := map[string]int{}
	for _, name := range names {
		src, kind := sources[name].src, sources[name].kind
		file, err := syntax.Parse(src)
		if err != nil {
			continue // a parse error is the parser's, reported before any pass
		}
		programs[kind]++
		alone, ice := checkPlaced([]*syntax.File{file})
		if ice != "" {
			t.Errorf("%s alone: %s", name, ice)
			continue
		}
		if len(alone) > 0 {
			negatives[kind]++
		} else if kind == "filesplit" {
			t.Errorf("%s: a filesplit fixture must be refused, and it is clean", name)
		}
		for at := range 2 {
			// Each check parses afresh, so no check sees another's tree.
			own, _ := syntax.Parse(src)
			other, _ := syntax.Parse(neutralFile)
			files := []*syntax.File{other, other}
			files[at] = own
			split, ice := checkPlaced(files)
			if ice != "" {
				t.Errorf("%s as file %d of two: %s", name, at, ice)
				continue
			}
			want := make([]check.Diagnostic, len(alone))
			for i, d := range alone {
				d.File = at
				want[i] = d
			}
			if len(split) != len(want) || len(want) > 0 && !reflect.DeepEqual(split, want) {
				t.Errorf("%s as file %d of two: the diagnostics move\n\talone: %s\n\tsplit: %s", name, at, placed(alone), placed(split))
			}
		}
	}
	var parts []string
	for _, kind := range []string{"corpus", "test", "filesplit", "frames", "owner"} {
		parts = append(parts, fmt.Sprintf("%s %d (%d negative)", kind, programs[kind], negatives[kind]))
	}
	t.Logf("programs: %s; each checked alone, as file 0 and as file 1 of two", strings.Join(parts, ", "))
	// The floors fail when a source of programs shrinks or stops being read
	// (measured at E2 round 3: corpus 193, test 1279, frames 29 negatives;
	// at E2b, with no spelling filter on test literals and the provenance
	// guard's programs: test 1301, owner 49 negatives).
	if negatives["corpus"] < 150 || negatives["test"] < 1200 || negatives["frames"] < 21 || negatives["owner"] < 45 || negatives["filesplit"] < len(splitFixtures(t)) {
		t.Fatalf("the guard's sources shrank: %v negative of %v", negatives, programs)
	}
}

// TestDeclarationSplitStaysInItsFile spreads each program of the guard over
// two files: its imports in both, its other declarations alternately in
// file 0 and file 1, once from each side. A check of one declaration then
// meets the nodes of declarations in the other file (a callee's parameters,
// a type's fields, a context's provider), and a pass that reports at such a
// node while the other file is selected stops at the checker's ownership
// assertion. The diagnostics differ from the one-file check (an import
// unused in one file, a cycle reported at its first member), so this test
// requires only that no split panics and that every diagnostic lies inside
// the source of the file it names.
func TestDeclarationSplitStaysInItsFile(t *testing.T) {
	sources := splitSources(t)
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	splits, refused := 0, 0
	for _, name := range names {
		src := sources[name].src
		whole, err := syntax.Parse(src)
		if err != nil {
			continue
		}
		var imports string
		var decls []string
		for i, d := range whole.Decls {
			end := len(src)
			if i+1 < len(whole.Decls) {
				end = int(whole.Decls[i+1].Pos())
			}
			text := src[d.Pos():end]
			if _, ok := d.(*syntax.ImportDecl); ok {
				imports += text
			} else {
				decls = append(decls, text)
			}
		}
		if len(decls) < 2 {
			continue // one declaration has no second file to meet
		}
		for side := range 2 {
			texts := []string{imports, imports}
			for i, d := range decls {
				texts[(i+side)%2] += d + "\n"
			}
			files := make([]*syntax.File, 2)
			parsed := true
			for i, text := range texts {
				if files[i], err = syntax.Parse(text); err != nil {
					parsed = false // a declaration whose text needs its neighbour
				}
			}
			if !parsed {
				continue
			}
			splits++
			ds, ice := checkPlaced(files)
			if ice != "" {
				t.Errorf("%s split from side %d: %s", name, side, ice)
				continue
			}
			if len(ds) > 0 {
				refused++
			}
			for _, d := range ds {
				if d.File < 0 || d.File > 1 || d.Offset < 0 || d.Length <= 0 || d.Offset+d.Length > len(texts[d.File]) {
					t.Errorf("%s split from side %d: a diagnostic outside its file: %s", name, side, placed([]check.Diagnostic{d}))
				}
			}
		}
	}
	t.Logf("declaration splits: %d checked, %d refused", splits, refused)
	// Measured at E2 round 3: 2120 splits, 1350 refused. The floor fails
	// when the sources shrink or the split stops parsing.
	if splits < 1800 || refused < 1100 {
		t.Fatalf("the declaration split shrank: %d splits, %d refused", splits, refused)
	}
}

// TestParameterDefsAcrossFiles: two files declare a parameter at the same
// offset, and Info.Defs binds each parameter's own identifier. The checker
// once found a parameter's identifier by its offset alone, so the second
// file's parameter took the first's place (E2 round 3).
func TestParameterDefsAcrossFiles(t *testing.T) {
	a, _ := syntax.Parse("func a(x int) int { x }\n")
	b, _ := syntax.Parse("func b(y int) int { y }\n")
	pkg, ds := check.Check("main", []*syntax.File{a, b}, nil)
	if len(ds) != 0 {
		t.Fatal(placed(ds))
	}
	params := map[string]bool{}
	for id, obj := range pkg.Info.Defs {
		if v, ok := obj.(*check.Var); ok {
			if v.Name != id.Name {
				t.Errorf("Defs binds the identifier %s to the variable %s", id.Name, v.Name)
			}
			params[id.Name] = true
		}
	}
	if !params["x"] || !params["y"] {
		t.Errorf("Defs lacks a parameter: %v", params)
	}
}

// TestFrameRefusalNamesItsFile is E2 round 1 M1's regression as the review
// wrote it: the invalid initializer in each position of a two-file package
// whose other file is `func main() {}`, refused at file, 2:9.
func TestFrameRefusalNamesItsFile(t *testing.T) {
	for at := range 2 {
		texts := []string{"func main() {}\n", "func main() {}\n"}
		texts[at] = m1Program
		var files []*syntax.File
		for _, src := range texts {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		_, ds := check.Check("main", files, nil)
		if len(ds) != 1 || ds[0].Code != check.CodeType || ds[0].File != at || ds[0].Line != 2 || ds[0].Column != 9 ||
			ds[0].Offset+ds[0].Length > len(m1Program) ||
			m1Program[ds[0].Offset:ds[0].Offset+ds[0].Length] != `with log.Annotate("k", "v") { 1 }` {
			t.Errorf("the initializer as file %d: want one S108 at file %d, 2:9; got %s", at, at, placed(ds))
		}
	}
}

func placed(ds []check.Diagnostic) string {
	var parts []string
	for _, d := range ds {
		parts = append(parts, fmt.Sprintf("file %d %d:%d+%d %s %s", d.File, d.Line, d.Column, d.Length, d.Code, d.Message))
	}
	return strings.Join(parts, "; ")
}
