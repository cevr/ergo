package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The source-provenance guard (stages E2b to E2d). A diagnostic lies in the
// file that holds the node it is reported at; a node the checker makes is in
// no file's tree, so it records its origin (made, clone, owner.go) and lies
// in its origin's file. E2 round 3 asserted ownership at every report but
// left the made nodes out of its domain: a variant's type parameter, made
// from the union's, reported S108 at an identifier of no file, and the
// assertion turned the diagnostic into an internal compiler error (E2 round
// 3 M1). The tests missed it because no program reported at a made node: a
// clean program made ownerless nodes and nothing looked at them.
//
// Five checks hold the class, none of them waiting for a diagnostic:
//   - the provenance analyzer (tools/provenance, gate step "provenance
//     analyzer") reads the checker's SSA, per function and per generic
//     instance: each node the checker makes must be a fresh node passed
//     straight to made, or a copy through clone, at a site
//     testdata/provenance-manifest.txt lists with a program that reaches it;
//     no conversion and no type with a node's layout can make a node; and
//     the parser is called only from the functions that parse and check a
//     whole file. Its controls are analysistest packages over a pristine and
//     a mutant copy of this package (tools/provenance/testdata). It replaces
//     the go/types inventory this file kept through E2c, whose gaps were
//     flows (E2c round 1 review M1, m2, m3, o2);
//   - TestSyntheticNodesHaveSourceOwners runs the manifest's programs, clean,
//     in both file slots and with an early use, and resolves every made node
//     against an index of the trees the test builds itself;
//   - TestOwnerResolution checks the same on two files whose nodes share
//     their offsets, for every tree node and every made node, and holds the
//     fault controls: no origin, a cycle, two origins, a nil node, a node
//     made of two files, a tree node recorded as made, the wrong file;
//   - TestInlineVariantParameterDiagnostics holds E2 round 3 M1's four
//     programs and a matrix of the parameter rules over inline variants,
//     each diagnostic expected at the union's own parameter, computed from
//     the source text, with the union's Info.Defs intact.
// TestErrorInSpan holds errorIn's whole-span bound with direct controls
// and the tail and catch programs whose spans end at a brace.
//
// Known gaps of the analyzer (E2d round 1):
//   - SSA folds an identity conversion of a node pointer ((*syntax.Ident)(p)
//     with p already one) away, so it is not reported; it makes no node.
//   - A package outside check is seen only at its import: the allow-list
//     holds packages reviewed as unable to build a caller's node.
//   - A listed parser site can still parse text other than its input; the
//     analyzer pins the call to the site, not its argument to the input.
//   - An exported generic function is checked per instance the package
//     builds, not for instances another package could build.

// constructionSite is one place the checker makes a syntax node: the
// function and the node's type.
type constructionSite struct{ fn, kind string }

// The variant fixtures: a union whose inline variant uses its parameter,
// unconstrained and constrained, and one used before the union.
const (
	variantFixture            = "type U[T] = | V{X T} | W{}\n"
	variantConstrainedFixture = "type U[T number] = | V{X T} | W{}\n"
	variantEarlyFixture       = "type A { S V[int] }\ntype U[T, S] = | V{X T} | Y{Z S, Q T} | W{}\n"
	compoundFixture           = "func f() int {\n\tlet mut x = 1\n\tx += 2\n\tx\n}\n"
	patternFixture            = "type P { A int }\nfunc f(p P) int {\n\tlet P{A: a} = p\n\ta\n}\n"
)

// manifestSites reads the construction sites of the checker's provenance
// manifest, the one the analyzer enforces: each "site <function> <node type>
// <fixture>" line, with the program of the fixture constant it names.
func manifestSites(t *testing.T) map[constructionSite]string {
	t.Helper()
	fixtures := map[string]string{
		"variantFixture":            variantFixture,
		"variantConstrainedFixture": variantConstrainedFixture,
		"variantEarlyFixture":       variantEarlyFixture,
		"compoundFixture":           compoundFixture,
		"patternFixture":            patternFixture,
	}
	content, err := os.ReadFile(filepath.Join("testdata", "provenance-manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sites := map[constructionSite]string{}
	for i, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "site" {
			continue
		}
		if len(fields) != 4 {
			t.Fatalf("provenance manifest line %d: want site <function> <node type> <fixture>: %q", i+1, line)
		}
		src, ok := fixtures[fields[3]]
		if !ok {
			t.Fatalf("provenance manifest line %d: no fixture constant named %s", i+1, fields[3])
		}
		sites[constructionSite{fields[1], fields[2]}] = src
	}
	if len(sites) == 0 {
		t.Fatal("the provenance manifest lists no construction site")
	}
	return sites
}

// provenanceFixtures are the clean programs the sweep runs: the manifest's
// fixtures and the remaining shapes of the variant rules.
func provenanceFixtures() []string {
	out := []string{
		variantFixture, variantConstrainedFixture, variantEarlyFixture, compoundFixture, patternFixture,
		"type U[T, S] = | V{X T} | W{Y S} | Z{}\n",
		"type U[T, S number] = | V{X S} | W{}\n",
		"type U[R, T] = | V{X T} | W{}\ntype A { S V[int] }\n",
		"type A { S V[int, string] }\ntype U[T, S] = | V{X T, Y S} | W{}\nfunc f() int {\n\tlet mut n = 1\n\tn *= 3\n\tlet (a, b) = (n, 2)\n\ta + b\n}\n",
	}
	sort.Strings(out)
	return out
}

// treeIndex is the test's own index from each node of the files' trees to
// the file's slot, built without the checker.
func treeIndex(files []*syntax.File) map[syntax.Node]int {
	index := map[syntax.Node]int{}
	for i, f := range files {
		syntax.Inspect(f, func(n syntax.Node) bool {
			index[n] = i
			return true
		})
	}
	return index
}

// expectedOwner follows a made node's origins in the checker's registry to
// a tree node of the test's index and returns its slot.
func expectedOwner(index map[syntax.Node]int, origins map[syntax.Node]syntax.Node, n syntax.Node) (int, error) {
	m := n
	for range len(origins) + 1 {
		origin, ok := origins[m]
		if !ok || origin == nil {
			return -1, fmt.Errorf("a %T with no recorded origin", m)
		}
		if slot, ok := index[origin]; ok {
			return slot, nil
		}
		m = origin
	}
	return -1, fmt.Errorf("a %T whose origins form a cycle", n)
}

func parseOrFatal(t testing.TB, src string) *syntax.File {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f
}

const unrelatedFile = "func unrelated() {}\n"

func TestSyntheticNodesHaveSourceOwners(t *testing.T) {
	sites := manifestSites(t)
	reached := map[string]map[string]bool{}
	for _, src := range provenanceFixtures() {
		for slot := range 2 {
			t.Run(fmt.Sprintf("%q/slot=%d", src, slot), func(t *testing.T) {
				files := []*syntax.File{parseOrFatal(t, unrelatedFile), parseOrFatal(t, unrelatedFile)}
				files[slot] = parseOrFatal(t, src)
				c := newChecker("main", Std, prelude().scope)
				c.run(files)
				if ds := c.sorted(); len(ds) != 0 {
					t.Fatalf("a provenance fixture must be clean: %v", ds)
				}
				index := treeIndex(files)
				origins := c.madeNodes()
				if reached[src] == nil {
					reached[src] = map[string]bool{}
				}
				for n := range origins {
					kind := strings.TrimPrefix(fmt.Sprintf("%T", n), "*syntax.")
					reached[src][kind] = true
					if _, ok := index[n]; ok {
						t.Errorf("a %s of the tree is recorded as made", kind)
						continue
					}
					want, err := expectedOwner(index, origins, n)
					if err != nil {
						t.Errorf("%v", err)
						continue
					}
					if want != slot {
						t.Errorf("a made %s comes from file %d, not the fixture's file %d", kind, want, slot)
					}
					if got := c.fileOf(n); got == nil || got.index != want {
						t.Errorf("a made %s at %d..%d: the checker gives %s, its origin is in file %d", kind, n.Pos(), n.End(), fileName(got, "no file"), want)
					}
				}
				for id := range c.synthetic {
					if _, ok := origins[id]; !ok {
						t.Errorf("synthetic identifier %s at %d has no recorded origin", id.Name, id.Pos())
					}
				}
			})
		}
	}
	for site, src := range sites {
		if !reached[src][site.kind] {
			t.Errorf("the program of %s %s makes no %s: %q", site.fn, site.kind, site.kind, src)
		}
	}
}

// icePanic runs f and returns its panic, or "".
func icePanic(f func()) (ice string) {
	defer func() {
		if r := recover(); r != nil {
			ice = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func TestOwnerResolution(t *testing.T) {
	// Two files whose nodes share their offsets: an owner found by
	// position, or by the current file, names the wrong one.
	src := func(i int) string {
		return fmt.Sprintf("type U%d[T] = | V%d{X T} | W%d{}\nfunc f%d() int {\n\tlet mut x = 1\n\tx += 2\n\tlet P%d{A: a} = P%d{A: x}\n\ta\n}\ntype P%d { A int }\n", i, i, i, i, i, i, i)
	}
	files := []*syntax.File{parseOrFatal(t, src(0)), parseOrFatal(t, src(1))}
	c := newChecker("main", Std, prelude().scope)
	c.run(files)
	if ds := c.sorted(); len(ds) != 0 {
		t.Fatalf("the two files must be clean: %v", ds)
	}
	index := treeIndex(files)
	origins := c.madeNodes()
	kinds := map[string]int{}
	check := func(n syntax.Node, want int) {
		t.Helper()
		if got := c.fileOf(n); got == nil || got.index != want {
			t.Errorf("a %T at %d..%d: the checker gives %s, want file %d", n, n.Pos(), n.End(), fileName(got, "no file"), want)
		}
		for slot, fi := range c.files {
			c.cur = fi
			ice := icePanic(func() { c.assertOwner(n, "S108", "control", nil) })
			if (ice == "") != (slot == want) {
				t.Errorf("a %T of file %d with file %d current: ice %q", n, want, slot, ice)
			}
		}
	}
	for n, slot := range index {
		check(n, slot)
	}
	for n := range origins {
		want, err := expectedOwner(index, origins, n)
		if err != nil {
			t.Fatal(err)
		}
		kinds[fmt.Sprintf("%T", n)]++
		check(n, want)
	}
	if len(kinds) != 7 {
		t.Errorf("the two files make %d kinds of node, want all 7: %v", len(kinds), kinds)
	}

	// An origin chain resolves to the tree node at its end.
	f0, f1 := c.files[0], c.files[1]
	d0 := files[0].Decls[1].(*syntax.FuncDecl)
	d1 := files[1].Decls[1].(*syntax.FuncDecl)
	x0 := d0.Body.List[1].(*syntax.AssignStmt)
	x1 := d1.Body.List[1].(*syntax.AssignStmt)
	inner := made(c, &syntax.Ident{NamePos: x1.Pos(), Name: "x"}, x1)
	outer := made(c, &syntax.Ident{NamePos: x1.Pos(), Name: "x"}, inner)
	if got := c.fileOf(outer); got != f1 {
		t.Errorf("an origin chain into file 1 gives %s", fileName(got, "no file"))
	}

	// The fault controls: each is an internal compiler error with the
	// current file selected, in every build.
	c.cur = f0
	report := func(n syntax.Node) func() { return func() { c.assertOwner(n, "S108", "control", nil) } }
	cycleA := &syntax.Ident{NamePos: x0.Pos(), Name: "a"}
	cycleB := &syntax.Ident{NamePos: x0.Pos(), Name: "b"}
	made(c, cycleA, cycleB)
	made(c, cycleB, cycleA)
	conflict := made(c, &syntax.Ident{NamePos: x0.Pos(), Name: "c"}, x0)
	treeMade := d0.Body.List[0]
	made(c, treeMade, x0)
	viaTreeMade := made(c, &syntax.Ident{NamePos: treeMade.Pos(), Name: "d"}, treeMade)
	mixed := made(c, &syntax.BinaryExpr{X: x0.Lhs[0], OpPos: x0.TokPos, Op: syntax.ADD, Y: x1.Rhs[0]}, x0)
	mixedMade := made(c, &syntax.BinaryExpr{X: x0.Lhs[0], OpPos: x0.TokPos, Op: syntax.ADD, Y: outer}, x0)
	var typedNil *syntax.Ident
	for _, ctl := range []struct {
		name, want string
		f          func()
	}{
		{"no origin", "with no recorded origin", report(&syntax.Ident{NamePos: x0.Pos(), Name: "x"})},
		{"wrong file at the same offset", "file 1", report(outer)},
		{"cycle", "cycle", report(cycleA)},
		{"two origins", "two origins", func() { made(c, conflict, x1) }},
		{"tree node recorded as made", "recorded as made", report(treeMade)},
		{"made from a tree node recorded as made", "recorded as made", report(viaTreeMade)},
		{"made of two files", "holds a *syntax.BasicLit of file 1", report(mixed)},
		{"made of two files through a made child", "made from file 1", report(mixedMade)},
		{"nil", "a nil node", report(nil)},
		{"typed nil", "a nil node", report(typedNil)},
		{"made nil", "is nil", func() { made(c, typedNil, x0) }},
		{"made from nil", "has no origin", func() { made(c, &syntax.Ident{}, syntax.Node(typedNil)) }},
		{"made from itself", "its own origin", func() { id := &syntax.Ident{}; made(c, id, id) }},
	} {
		ice := icePanic(ctl.f)
		if !strings.Contains(ice, "internal compiler error") || !strings.Contains(ice, ctl.want) {
			t.Errorf("control %s: got %q, want an internal compiler error with %q", ctl.name, ice, ctl.want)
		}
	}
}

// ownerDiag is an expected diagnostic: the source text it underlines, by
// its byte offset in the file at slot File.
type ownerDiag struct {
	File, Offset, Length int
	Code, Message        string
}

// inlineVariantCase is one program of the parameter matrix: its files and
// the one diagnostic each rule gives at the union's own parameter.
type inlineVariantCase struct {
	name  string
	files []string
	want  []ownerDiag
}

// inlineVariantCases builds E2 round 3 M1's four programs and the matrix:
// the three parameter rules (declared twice, a prelude name, a type and a
// row) over unconstrained and constrained parameters, a parameter the
// variant uses or not, one or two variants that use it, no use, an early
// use before or after the union in its file, the use in another file in
// either order, and the union beside an unrelated file in either slot.
// Each expected diagnostic is the rule's message at the offending
// parameter of the union, found in the source text.
func inlineVariantCases() []inlineVariantCase {
	// after is the offset just after the first marker in src.
	after := func(src, marker string) int { return strings.Index(src, marker) + len(marker) }
	diag := func(off, length int, msg string) []ownerDiag {
		return []ownerDiag{{File: 0, Offset: off, Length: length, Code: CodeType, Message: msg}}
	}
	dupMsg := "type parameter T is declared twice"
	preludeMsg := "string is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"
	conflictMsg := "type parameter T is used both as a type and as a row (types.md, Generics 1)"
	m1A := "type U[T, T] = | V{X T} | W{}\n"
	m1B := "type A { S V[int, int] }\ntype U[T, T] = | V{X T} | W{}\n"
	m1C := "type A { S V[int] }\ntype U[string] = | V{X string} | W{}\n"
	m1D := "type U[T] = | V{X T, F func() needs T} | W{}\n"
	out := []inlineVariantCase{
		{"M1 duplicate", []string{m1A}, diag(after(m1A, "U[T, "), 1, dupMsg)},
		{"M1 duplicate, early use", []string{m1B}, diag(after(m1B, "U[T, "), 1, dupMsg)},
		{"M1 prelude, early use", []string{m1C}, diag(after(m1C, "U["), 6, preludeMsg)},
		{"M1 type and row", []string{m1D}, diag(after(m1D, "U["), 1, conflictMsg)},
	}
	type rule struct {
		name, params, consParams, arg, msg, marker string
		markerSkip, length                         int
		conflict                                   bool
	}
	rules := []rule{
		{name: "duplicate", params: "T, T", consParams: "T, T number", arg: "int, int", msg: dupMsg, marker: "T", markerSkip: 2, length: 1},
		{name: "prelude", params: "string", consParams: "string number", arg: "int", msg: preludeMsg, marker: "string", markerSkip: 1, length: 6},
		{name: "type and row", params: "T", arg: "int", msg: conflictMsg, marker: "T", markerSkip: 1, length: 1, conflict: true},
	}
	for _, r := range rules {
		param := "T"
		if r.name == "prelude" {
			param = "string"
		}
		for _, cons := range []bool{false, true} {
			if cons && r.conflict {
				continue // a constrained parameter has its constraint's kind
			}
			params := r.params
			if cons {
				params = r.consParams
			}
			for _, shape := range []string{"used", "unused", "two variants"} {
				if shape == "unused" && r.conflict {
					continue // a parameter no variant uses has no kind to conflict
				}
				var variants string
				switch shape {
				case "used":
					variants = "| V{X " + param + "} | W{}"
				case "unused":
					variants = "| V{X int} | W{}"
				case "two variants":
					variants = "| V{X " + param + "} | Y{Z " + param + "} | W{}"
				}
				if r.conflict {
					variants = strings.Replace(variants, "V{X T}", "V{X T, F func() needs T}", 1)
				}
				union := "type U[" + params + "] = " + variants + "\n"
				use := "type A { S V[" + r.arg + "] }\n"
				uses := shape != "unused" && !r.conflict
				name := fmt.Sprintf("%s/constrained=%v/%s", r.name, cons, shape)
				expect := func(file int, src string) []ownerDiag {
					off := strings.Index(src, "type U[") + len("type U[")
					list := src[off:]
					rel := -1
					for range r.markerSkip {
						rel += 1 + strings.Index(list[rel+1:], r.marker)
					}
					return []ownerDiag{{File: file, Offset: off + rel, Length: r.length, Code: CodeType, Message: r.msg}}
				}
				for slot := range 2 {
					files := []string{unrelatedFile, unrelatedFile}
					files[slot] = union
					out = append(out, inlineVariantCase{fmt.Sprintf("%s/alone/slot=%d", name, slot), files, expect(slot, union)})
				}
				if !uses {
					continue
				}
				before, after := use+union, union+use
				out = append(out,
					inlineVariantCase{name + "/use before", []string{before}, expect(0, before)},
					inlineVariantCase{name + "/use after", []string{after}, expect(0, after)},
					inlineVariantCase{name + "/use in file 0", []string{use, union}, expect(1, union)},
					inlineVariantCase{name + "/use in file 1", []string{union, use}, expect(0, union)},
				)
			}
		}
	}
	return out
}

// OwnerGuardPrograms returns this guard's one-file programs by name, for
// the file-placement guard (filesplit_e2_test.go): the provenance fixtures,
// the parameter matrix and the tail programs.
func OwnerGuardPrograms() map[string]string {
	out := map[string]string{}
	for i, src := range provenanceFixtures() {
		out[fmt.Sprintf("provenance fixture %02d", i)] = src
	}
	for _, tc := range inlineVariantCases() {
		var own []string
		for _, f := range tc.files {
			if f != unrelatedFile {
				own = append(own, f)
			}
		}
		if len(own) == 1 {
			out["inline variant: "+tc.name] = own[0]
		}
	}
	for _, p := range tailPrograms {
		out["tail: "+p.name] = p.src
	}
	return out
}

func lineCol(src string, off int) (int, int) {
	line := 1 + strings.Count(src[:off], "\n")
	return line, off - strings.LastIndex(src[:off], "\n")
}

func TestInlineVariantParameterDiagnostics(t *testing.T) {
	cases := inlineVariantCases()
	if len(cases) < 40 {
		t.Fatalf("the matrix has %d programs", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]*syntax.File, len(tc.files))
			for i, src := range tc.files {
				files[i] = parseOrFatal(t, src)
			}
			c := newChecker("main", Std, prelude().scope)
			var got []Diagnostic
			if ice := icePanic(func() { c.run(files); got = c.sorted() }); ice != "" {
				t.Fatalf("internal compiler error: %s", ice)
			}
			var want []Diagnostic
			for _, w := range tc.want {
				line, col := lineCol(tc.files[w.File], w.Offset)
				want = append(want, Diagnostic{Code: w.Code, Message: w.Message, File: w.File, Offset: w.Offset, Length: w.Length, Line: line, Column: col})
			}
			if fmt.Sprint(got) != fmt.Sprint(want) || len(got) != len(want) {
				t.Fatalf("diagnostics\n\tgot  %+v\n\twant %+v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("diagnostic %d\n\tgot  %+v\n\twant %+v", i, got[i], want[i])
				}
			}
			// The union's parameters keep their Defs; no made identifier
			// has one.
			var union *Named
			for _, tn := range c.pkg.Types {
				if tn.Name == "U" {
					union = tn.Type.(*Named)
				}
			}
			if union == nil {
				t.Fatal("no union U")
			}
			var declared []*syntax.Ident
			for _, f := range union.Obj.Decl.TypeParams.List {
				declared = append(declared, f.Names...)
			}
			if len(declared) != len(union.TParams) {
				t.Fatalf("U has %d parameters for %d names", len(union.TParams), len(declared))
			}
			for i, id := range declared {
				if c.pkg.Info.Defs[id] != Object(union.TParams[i]) {
					t.Errorf("Defs of U's parameter %s at %d is %v, not U's parameter %d", id.Name, id.Pos(), c.pkg.Info.Defs[id], i)
				}
			}
			for id := range c.synthetic {
				if _, ok := c.pkg.Info.Defs[id]; ok {
					t.Errorf("made identifier %s at %d has a Defs entry", id.Name, id.Pos())
				}
			}
		})
	}
}

// tailPrograms are the programs whose missing value reports at a brace or
// at the end of a branch, through errorIn: nested value if, match and
// catch, empty arms and else bodies, outer missing branches. Each wants
// its diagnostics alone, as file 0 and as file 1 beside an unrelated file
// (L:C+N CODE message, measured at 2ae616d8 before the whole-span bound).
const (
	tailFunc  = "this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"
	tailValue = "this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"
	tailArm   = "this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"
)

var tailPrograms = []struct{ name, src, want string }{
	{"if-empty", "func f(b bool) int { if b {} else {1} }\n", "1:28+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"if-value-empty", "func f(b bool) int { let x = if b {} else {1}; x }\n", "1:36+1 S107 this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"},
	{"if-value-no-else", "func f(b bool) int { let x = if b {1}; x }\n", "1:30+8 S108 a value if needs an else: without one it has no value (D-C2)"},
	{"if-nested-missing", "func f(b bool) int { if b { if b {1} } else {1} }\n", "1:38+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"match-empty", "func f(b bool) int { match b { case true: case false: 1 } }\n", "1:57+1 S107 this branch can reach its end without a value, and function f needs one: end it with an expression, return or an else (rows.md, Totality 3)"},
	{"match-value-empty", "func f(b bool) int { let x = match b { case true: case false: 1 }; x }\n", "1:65+1 S107 this branch can reach its end without a value; a value if or match yields a value from every branch (D-C1)"},
	{"catch-empty", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f() int { boom() catch { case Fault: } }\n", "3:41+1 S107 this arm yields no value, and the catch yields a value of type int (rows.md, Catch 3)"},
	{"catch-if", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b {1} } }\n", "3:49+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"catch-match", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: match b { case true: case false: 1 } } }\n", "3:84+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"catch-void", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc nop() {}\nfunc f() int { boom() catch { case Fault: nop() } }\n", "4:43+1 S107 this path of the arm can reach its end without a value, and the catch yields a value of type int: end it with an expression, return or an else (rows.md, Catch 7)"},
	{"if-else-empty", "func f(b bool) int { if b {1} else {} }\n", "1:37+1 S107 " + tailFunc},
	{"if-value-else-empty", "func f(b bool) int { let x = if b {1} else {}; x }\n", "1:45+1 S107 " + tailValue},
	{"if-value-nested", "func f(b bool) int { let x = if b { if b {1} else {} } else {1}; x }\n", "1:52+1 S107 " + tailValue},
	{"match-nested-if", "func f(b bool) int { match b { case true: if b {1}; case false: 1 } }\n", "1:67+1 S107 " + tailFunc},
	{"match-value-nested-if", "func f(b bool) int { let x = match b { case true: if b {1} else {}; case false: 1 }; x }\n", "1:66+1 S107 " + tailValue},
	{"match-value-nested-match", "func f(b bool) int { let x = match b { case true: match b { case true: 1; case false: }; case false: 1 }; x }\n", "1:87+1 S107 " + tailValue},
	{"catch-match-empty-arm", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: match b { case true: 1; case false: } } }\n", "3:85+1 S107 " + tailArm},
	{"catch-if-else-empty", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b {1} else {} } }\n", "3:64+1 S107 " + tailArm},
	{"catch-nested-if", "type Fault {}\nfunc boom() int fails Fault { Fault }\nfunc f(b bool) int { boom() catch { case Fault: if b { if b {1} } else {1} } }\n", "3:65+1 S107 " + tailArm},
}

func TestErrorInSpan(t *testing.T) {
	file := parseOrFatal(t, "func f() int {1}\nfunc g() int {2}\n")
	c := newChecker("main", Std, prelude().scope)
	c.run([]*syntax.File{file})
	c.cur = c.files[0]
	body := file.Decls[0].(*syntax.FuncDecl).Body
	in := func(pos, end syntax.Pos) string {
		return icePanic(func() { c.errorIn(body, pos, end, CodeType, "span control") })
	}
	for _, ok := range []struct {
		name     string
		pos, end syntax.Pos
	}{
		{"whole", body.Pos(), body.End()},
		{"brace", body.Rbrace, body.Rbrace + 1},
		{"empty at the start", body.Pos(), body.Pos()},
		{"empty at the end", body.End(), body.End()},
	} {
		if ice := in(ok.pos, ok.end); ice != "" {
			t.Errorf("%s: %s", ok.name, ice)
		}
	}
	for _, bad := range []struct {
		name     string
		pos, end syntax.Pos
	}{
		{"end past the node", body.Rbrace, body.End() + 1},
		{"end far past the node", body.Rbrace, body.End() + 50},
		{"start before the node", body.Pos() - 1, body.Pos() + 1},
		{"start past the node", body.End() + 1, body.End() + 2},
		{"end before start", body.Rbrace, body.Rbrace - 1},
		{"no start", syntax.NoPos, body.End()},
		{"no end", body.Rbrace, syntax.NoPos},
	} {
		if ice := in(bad.pos, bad.end); !strings.Contains(ice, "outside its *syntax.BlockStmt") {
			t.Errorf("%s: got %q, want an internal compiler error", bad.name, ice)
		}
	}

	for _, p := range tailPrograms {
		for _, layout := range []int{-1, 0, 1} {
			files := []*syntax.File{parseOrFatal(t, p.src)}
			slot := 0
			if layout >= 0 {
				slot = layout
				files = []*syntax.File{parseOrFatal(t, unrelatedFile), parseOrFatal(t, unrelatedFile)}
				files[slot] = parseOrFatal(t, p.src)
			}
			var ds []Diagnostic
			if ice := icePanic(func() { ds, _ = checkFiles(files) }); ice != "" {
				t.Errorf("%s in layout %d: %s", p.name, layout, ice)
				continue
			}
			var got []string
			for _, d := range ds {
				if d.File != slot {
					t.Errorf("%s in layout %d: a diagnostic in file %d: %v", p.name, layout, d.File, d)
				}
				got = append(got, fmt.Sprintf("%d:%d+%d %s %s", d.Line, d.Column, d.Length, d.Code, d.Message))
			}
			if strings.Join(got, "\n") != p.want {
				t.Errorf("%s in layout %d:\n\tgot  %s\n\twant %s", p.name, layout, strings.Join(got, "\n\t     "), p.want)
			}
		}
	}
}

func checkFiles(files []*syntax.File) ([]Diagnostic, *Package) {
	pkg, ds := Check("main", files, nil)
	return ds, pkg
}
