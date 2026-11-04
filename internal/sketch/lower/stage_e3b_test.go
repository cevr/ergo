package lower

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage E3b (196l; ledger row C20; spec D-E4): a law is checked and never
// run, so neither build lowers it. Its facts are OwnerLaw's
// (check/owners.go), and no build view sees that owner (lower/owners.go).
// Each program of testdata/e3b declares at least one law and states the
// stdout of ergo run (`// run> `); a program with tests states its test
// binary's stdout and exit (`// test> `, `// test exit N`) as testdata/e3
// does. TestLawSweep runs them; TestBuildsOmitLaws proves both builds of
// each are the builds of the same source with its laws blanked out.

// e3bProgram is one program of testdata/e3b and its expected outputs.
type e3bProgram struct {
	name, src string
	run       []string // ergo run's stdout lines
	test      []string // the test binary's stdout lines, if it has tests
	exit      int      // the test binary's exit status, -1 without tests
}

func e3bPrograms(t *testing.T) []e3bProgram {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "e3b", "*.ergo"))
	if err != nil || len(paths) < 12 {
		t.Fatalf("testdata/e3b holds %d programs (%v); the sweep needs 12 or more", len(paths), err)
	}
	var out []e3bProgram
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		prog := e3bProgram{name: "e3b_" + strings.TrimSuffix(filepath.Base(p), ".ergo"), src: string(data), exit: -1}
		for _, line := range strings.Split(prog.src, "\n") {
			switch {
			case strings.HasPrefix(line, "// run> "):
				prog.run = append(prog.run, strings.TrimPrefix(line, "// run> "))
			case strings.HasPrefix(line, "// test> "):
				prog.test = append(prog.test, strings.TrimPrefix(line, "// test> "))
			case strings.HasPrefix(line, "// test exit "):
				prog.exit, err = strconv.Atoi(strings.TrimPrefix(line, "// test exit "))
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
		}
		if len(prog.run) == 0 {
			t.Fatalf("%s states no run output", p)
		}
		if (len(prog.test) == 0) != (prog.exit < 0) {
			t.Fatalf("%s states test output without a test exit, or the reverse", p)
		}
		out = append(out, prog)
	}
	return out
}

// TestLawSweep builds the run build of every program of testdata/e3b and
// runs it: each prints exactly its run lines, so a law changes nothing
// ergo run does. A program with tests also runs its test build, which runs
// its tests and no law.
func TestLawSweep(t *testing.T) {
	progs := e3bPrograms(t)
	var builds []*lowered
	tested := 0
	for _, p := range progs {
		run := mustLower(t, p.name+"_run", p.src)
		if len(run.pkg.Laws) == 0 {
			t.Errorf("%s declares no law", p.name)
		}
		builds = append(builds, run)
		if p.exit >= 0 {
			if len(run.pkg.Tests) == 0 {
				t.Errorf("%s states test output and declares no test", p.name)
			}
			builds = append(builds, lowerTestBuild(t, p.name+"_test", p.src))
			tested++
		}
	}
	if tested == 0 {
		t.Errorf("no program of testdata/e3b has tests and laws (sweep program 11)")
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name+"_run"])
		if code != 0 || strings.Join(lines(stdout), "\n") != strings.Join(p.run, "\n") {
			t.Errorf("%s: ergo run: exit %d, stdout %q, stderr %q; want %q", p.name, code, stdout, firstLines(stderr, 8), p.run)
		}
		if p.exit < 0 {
			continue
		}
		stdout, stderr, code = runExe(t, exes[p.name+"_test"])
		if code != p.exit || strings.Join(lines(stdout), "\n") != strings.Join(p.test, "\n") {
			t.Errorf("%s: test binary: exit %d (want %d), stdout:\n%s\nwant:\n%s\nstderr:\n%s", p.name, code, p.exit, stdout, strings.Join(p.test, "\n"), firstLines(stderr, 12))
		}
	}
}

// lawFixture is the spec fixture with laws (C21); it lowers, so the
// erasure property covers it too.
const lawFixture = "../check/testdata/fixtures/spec/types-laws.ergo"

// TestBuildsOmitLaws proves D-E4's "checked, not run" on the emitted Go of
// every sweep program and of the laws fixture, for the run build (Lower)
// and the test build (LowerTests): no //line directive maps into a law,
// and each build is byte-identical to the same build of the source with
// its laws blanked out (blankLaws), with no exception (law-top: a type of
// laws and no operations is S108, so no build meets one).
func TestBuildsOmitLaws(t *testing.T) {
	type source struct{ name, src string }
	var srcs []source
	for _, p := range e3bPrograms(t) {
		srcs = append(srcs, source{p.name, p.src})
	}
	data, err := os.ReadFile(lawFixture)
	if err != nil {
		t.Fatal(err)
	}
	srcs = append(srcs, source{"types_laws", string(data)})
	for _, s := range srcs {
		t.Run(s.name, func(t *testing.T) { buildsOmitLaws(t, s.name, s.src) })
	}
}

// lawFact is a law that alone introduces one emission fact the checker
// records package-wide, as ownerFact is a test (stage_e3a_owners_test.go):
// imports and decls are production source the law needs (decls never
// introduce the fact themselves), law is the law, a member of an operation
// type of its own. errType names the type the law alone makes an error
// type. canary is the Go name a build would reserve at package scope if it
// read the law's fact: a production function binds a local of that name,
// which keeps its spelling only while no build reads the fact, since a
// local avoids every package name. The facts the lowering only reserves
// names for are therefore seen in the Go.
type lawFact struct{ name, imports, decls, law, errType, canary string }

var lawFacts = []lawFact{
	// An error row of a literal in a law makes a struct an error type.
	{name: "failure-record", errType: "Bang1", canary: "textBang1",
		decls: "type Bang1 {}\n",
		law:   "law L(n int) {\n\t\tlet f = func() int fails Bang1 { return Bang1 }\n\t\tlet v = f() catch {\n\t\tcase Bang1:\n\t\t\tn\n\t\t}\n\t\tv == n\n\t}"},
	// The error type is a defined type over another struct.
	{name: "defined-failure", errType: "Err2", canary: "textErr2",
		decls: "type Base2 { Code int }\n\ntype Err2 Base2\n",
		law:   "law L(n int) {\n\t\tlet f = func() int fails Err2 { return Err2{Code: n} }\n\t\tlet v = f() catch {\n\t\tcase Err2 as e:\n\t\t\te.Code\n\t\t}\n\t\tv == n\n\t}"},
	// The program returns the struct as a value; a law fails with it.
	{name: "failure-and-value", errType: "Bang3", canary: "textBang3",
		decls: "type Bang3 { Code int }\n\nfunc make3() Bang3 {\n\tBang3{Code: 3}\n}\n\nfunc keep3() int {\n\tmake3().Code\n}\n",
		law:   "law L() {\n\t\tlet f = func() int fails Bang3 { return make3() }\n\t\tlet v = f() catch {\n\t\tcase Bang3 as e:\n\t\t\te.Code\n\t\t}\n\t\tv == 3\n\t}"},
	// An implicit conversion to an operation table needs an adapter.
	{name: "adapter", canary: "opsSq4AsShape4",
		decls: "type Shape4 {\n\tArea() int,\n}\n\ntype Sq4 {}\n\nfunc Area(_ Sq4) int {\n\t4\n}\n",
		law:   "law L() {\n\t\tlet s Shape4 = Sq4\n\t\ts.Area() == 4\n\t}"},
	// A let without a value of a record holding a Map needs a default.
	{name: "default-map", canary: "ergoDefaultReg5",
		decls: "type Reg5 { M Map[string, int] }\n",
		law:   "law L(k string) {\n\t\tlet mut r Reg5\n\t\tr.M[k] = 5\n\t\tlen(r.M) == 1\n\t}"},
	// A generic record and function instantiated only by a law.
	{name: "generic-instance",
		decls: "type Box6[T] { V T }\n\nfunc wrap6[T](v T) Box6[T] {\n\tBox6{V: v}\n}\n",
		law:   "law L(s string, n int) { wrap6(s).V == s && wrap6(n).V == n }"},
	// A named context only a law opens.
	{name: "named-context", canary: "buildLive7",
		decls: "type Store7 {\n\tPeek() int,\n}\n\ntype Mem7 {}\n\nfunc Peek(_ Mem7) int {\n\t7\n}\n\nlet Live7 = context {\n\tStore7: Mem7,\n}\n",
		law:   "law L() { with Live7 { Store7.Peek() } == 7 }"},
	// A context literal only a law opens.
	{name: "literal-context", canary: "opsMem8AsStore8",
		decls: "type Store8 {\n\tLook() int,\n}\n\ntype Mem8 {}\n\nfunc Look(_ Mem8) int {\n\t8\n}\n",
		law:   "law L() { with context { Store8: Mem8 } { Store8.Look() } == 8 }"},
	// A function generic over its needs, called only by a law.
	{name: "generic-needs", canary: "opsMem9AsStore9",
		decls: "type Store9 {\n\tGet9() int,\n}\n\ntype Mem9 {}\n\nfunc Get9(_ Mem9) int {\n\t9\n}\n\nfunc run9[F](f func() int needs F) int needs F {\n\tf()\n}\n",
		law:   "law L() { with context { Store9: Mem9 } { run9() { Store9.Get9() } } == 9 }"},
	// A library only a law uses; the erasure property blanks the import
	// with the law.
	{name: "library-import", canary: "WithCapacity", imports: "import slice\n\n",
		law: "law L(n int) {\n\t\tlet xs = slice.WithCapacity[int](n)\n\t\tlen(xs) == 0\n\t}"},
	// A tuple type only a law uses, holding a record whose default holds a
	// Map: a build that walks the tuple reserves the default's name.
	{name: "tuple", canary: "ergoDefaultReg12",
		decls: "type Reg12 { M Map[string, int] }\n",
		law:   "law L(n int, s string) {\n\t\tlet p = (n, s, Reg12{})\n\t\tlet (a, b, r) = p\n\t\ta == n && b == s && len(r.M) == 0\n\t}"},
	// A package let only a law reads.
	{name: "package-let",
		decls: "let Limit13 = 13\n",
		law:   "law L() { Limit13 == 13 }"},
	// A type that is an error type in the program too: the law adds
	// nothing.
	{name: "failure-also-production",
		decls: "type Bang14 {}\n\nfunc fail14() int fails Bang14 {\n\treturn Bang14\n}\n\nfunc keep14() int {\n\tfail14() catch {\n\tcase Bang14:\n\t\t14\n\t}\n}\n",
		law:   "law L() {\n\t\tlet f = func() int fails Bang14 { return Bang14 }\n\t\tlet v = f() catch {\n\t\tcase Bang14:\n\t\t\t14\n\t\t}\n\t\tlet w = fail14() catch {\n\t\tcase Bang14:\n\t\t\t14\n\t\t}\n\t\tv == w\n\t}"},
}

// lawProgram assembles base b with fact f's production source, its canary
// and its law: a member of an operation type of its own, or, when top, a
// top-level law (law-top).
func lawProgram(b ownerBase, f lawFact, top bool) string {
	var s strings.Builder
	s.WriteString(f.imports)
	parts := []string{b.decls, f.decls}
	if f.canary != "" {
		parts = append(parts, "func canary() int {\n\tlet "+f.canary+" = 1\n\t"+f.canary+"\n}\n")
	}
	if top {
		parts = append(parts, f.law+"\n", b.main)
	} else {
		parts = append(parts, "type LawHost {\n\tHostPing() int,\n\n\t"+f.law+",\n}\n", b.main)
	}
	for _, d := range parts {
		if d != "" {
			s.WriteString(d + "\n")
		}
	}
	return s.String()
}

// TestLawFactsStayOut extends E3a's owner property (TestTestFactsStayInTests)
// with the law owner: for every production base × every law that alone
// introduces one emission fact × both placements of the law (a member and,
// since law-top, a top-level law), both builds are the builds of the same
// source with the law (and an import only the law uses) blanked out
// (buildsOmitLaws), neither build gives a type only the law makes an error
// type an Error method or a text helper, the canary local keeps its Go
// name, and the run binary prints the base's output.
func TestLawFactsStayOut(t *testing.T) {
	type pair struct {
		name string
		base ownerBase
	}
	var pairs []pair
	var builds []*lowered
	for _, b := range ownerBases {
		for _, f := range lawFacts {
			for _, top := range []bool{false, true} {
				name := "lawfact_" + strings.ReplaceAll(b.name+"_"+f.name, "-", "_")
				label := b.name + "/" + f.name + "/member"
				if top {
					name += "_top"
					label = b.name + "/" + f.name + "/top-level"
				}
				t.Run(label, func(t *testing.T) {
					src := lawProgram(b, f, top)
					run, test := buildsOmitLaws(t, name, src)
					for _, build := range []struct{ name, code string }{{"run", run.mainGo()}, {"test", test.mainGo()}} {
						if f.errType != "" {
							lawErrorShape(t, name, build.name, f.errType, build.code)
						}
						if f.canary != "" && !regexp.MustCompile(`(?m)^\tvar `+f.canary+` `).MatchString(build.code) {
							t.Errorf("%s: the %s build renamed the canary local %s: a build read the law's fact", name, build.name, f.canary)
						}
					}
					pairs = append(pairs, pair{name, b})
					builds = append(builds, run)
				})
			}
		}
	}
	if t.Failed() {
		return
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range pairs {
		stdout, stderr, code := runExe(t, exes[p.name])
		if code != 0 || strings.TrimSuffix(stdout, "\n") != p.base.out {
			t.Errorf("%s: ergo run: exit %d, stdout %q, stderr %q; want %q", p.name, code, stdout, firstLines(stderr, 8), p.base.out)
		}
	}
}

// lawErrorShape: a type only a law makes an error type has no Error method
// and no text helper in the named build.
func lawErrorShape(t *testing.T, name, build, typ, code string) {
	t.Helper()
	const directive = `(?:/\*line [^*]*\*/)?`
	method := regexp.MustCompile(`(?m)^` + directive + `func \(\w+ \*?` + typ + `\) Error\(\) string`)
	text := regexp.MustCompile(`(?m)^` + directive + `func text` + typ + `\(`)
	if method.MatchString(code) || text.MatchString(code) {
		t.Errorf("%s: the %s build gives %s, an error type only in a law, an Error method or a text helper", name, build, typ)
	}
}

// buildsOmitLaws checks both builds of src, which must declare a law, and
// returns them.
func buildsOmitLaws(t *testing.T, name, src string) (run, test *lowered) {
	t.Helper()
	run = mustLower(t, name, src)
	if len(run.pkg.Laws) == 0 {
		t.Fatalf("%s declares no law", name)
	}
	test = lowerTestBuild(t, name+"_test", src)
	spans := lawSpans(run.file, run.pkg)
	blanked := blankLaws(t, name, src)
	for _, b := range []struct {
		build      string
		code       string
		blankBuild func() string
	}{
		{"run", run.mainGo(), func() string { return mustLower(t, name+"_blank", blanked).mainGo() }},
		{"test", test.mainGo(), func() string { return lowerTestBuild(t, name+"_blank_test", blanked).mainGo() }},
	} {
		for _, m := range directiveRe.FindAllStringSubmatch(b.code, -1) {
			line, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			for _, s := range spans {
				if at := [2]int{line, col}; !before(at, s[0]) && before(at, s[1]) {
					t.Errorf("%s: the %s build has Go from the law at %d:%d (%s)", name, b.build, s[0][0], s[0][1], m[0])
				}
			}
		}
		if blank := b.blankBuild(); blank != b.code {
			t.Errorf("%s: the %s build differs from the %s build of the source without its laws:\n%s", name, b.build, b.build, goDiff(blank, b.code))
		}
	}
	return run, test
}

// lawSpans are the line and column ranges of the laws pkg records for f.
func lawSpans(f *syntax.File, pkg *check.Package) [][2][2]int {
	var spans [][2][2]int
	for _, law := range pkg.Laws {
		l0, c0 := f.Position(law.Decl.Pos())
		l1, c1 := f.Position(law.Decl.End())
		spans = append(spans, [2][2]int{{l0, c0}, {l1, c1}})
	}
	return spans
}

// blankLaws replaces every law declaration of src, with the comma that
// ends it, by spaces, keeping its line feeds, so every other position stays
// where it was. A package that only laws used is then imported and not
// used, so it blanks that import too, and repeats until the source checks
// clean. A blanked source that does not check clean fails the test: the
// comparison would otherwise be vacuous.
func blankLaws(t *testing.T, name, src string) string {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		law, ok := n.(*syntax.LawDecl)
		if !ok {
			return true
		}
		end := int(law.End())
		for j := end; j < len(b); j++ {
			if b[j] == ',' {
				end = j + 1
				break
			}
			if b[j] != ' ' && b[j] != '\t' && b[j] != '\n' {
				break
			}
		}
		blank(int(law.Pos()), end)
		return false
	})
	for {
		f, err := syntax.Parse(string(b))
		if err != nil {
			t.Fatalf("%s: the source without its laws does not parse: %v\n%s", name, err, b)
		}
		if hasLaw(f) {
			t.Fatalf("%s: blankLaws kept a law:\n%s", name, b)
		}
		_, diags := check.Check("main", []*syntax.File{f}, nil)
		if len(diags) == 0 {
			return string(b)
		}
		blanked := false
		for _, d := range diags {
			if !strings.HasPrefix(d.Message, "package ") || !strings.HasSuffix(d.Message, " is imported and not used") {
				continue
			}
			for _, decl := range f.Decls {
				imp, ok := decl.(*syntax.ImportDecl)
				if !ok {
					continue
				}
				for _, spec := range imp.Specs {
					if int(spec.Pos()) <= d.Offset && d.Offset < int(spec.End()) {
						if len(imp.Specs) == 1 {
							blank(int(imp.Pos()), int(imp.End()))
						} else {
							blank(int(spec.Pos()), int(spec.End()))
						}
						blanked = true
					}
				}
			}
		}
		if !blanked {
			t.Fatalf("%s: the source without its laws does not check clean: %v\n%s", name, diags, b)
		}
	}
}

// hasLaw reports whether f still declares a law.
func hasLaw(f *syntax.File) bool {
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.LawDecl); ok {
			found = true
		}
		return !found
	})
	return found
}
