package lower

import (
	"regexp"
	"strings"
	"testing"
)

// Stage E3a round 2 M2 (check/owners.go, lower/owners.go): a test owns the
// emission facts its body introduces, so only the test build reads them.
// The property: for every production base × every test that alone
// introduces one emission fact, the run build is the run build of the same
// source with the test (and an import only the test uses) blanked out, the
// run binary prints the base's output, and the test build passes. A type
// that only a test's error row makes an error type gets an Error method and
// a text helper in the test build and neither in the run build.

// ownerBase is a production program: declarations and a main that prints
// out. The bases cover an ordinary program, a generic one, one with a
// context and one with a failure of its own.
type ownerBase struct{ name, decls, main, out string }

var ownerBases = []ownerBase{
	{"ordinary", "", "func main() needs Console {\n\tConsole.Println(\"run\")\n}\n", "run"},
	{"generic",
		"type Cell[T] { V T }\n\nfunc first[T](xs []T, fallback T) T {\n\tfor x in xs {\n\t\treturn x\n\t}\n\tfallback\n}\n",
		"func main() needs Console {\n\tConsole.Println(first([]string{\"g\"}, Cell{V: \"z\"}.V))\n}\n", "g"},
	{"context",
		"type Watch {\n\tNow() string,\n}\n\ntype Sys {}\n\nfunc Now(_ Sys) string {\n\t\"now\"\n}\n\nlet Run = context {\n\tWatch: Sys,\n}\n",
		"func main() needs Console {\n\tConsole.Println(with Run { Watch.Now() })\n}\n", "now"},
	{"failure",
		"type Oops { Code int }\n\nfunc risky(n int) int fails Oops {\n\tif n < 0 {\n\t\treturn Oops{Code: n}\n\t}\n\tn\n}\n",
		"func main() needs Console {\n\tlet v = risky(-4) catch {\n\tcase Oops as e:\n\t\te.Code\n\t}\n\tConsole.Println(\"\\(v)\")\n}\n", "-4"},
}

// ownerFact is a test that alone introduces one emission fact the checker
// records package-wide: imports and decls are production source the test
// needs (decls never introduce the fact themselves), test is the test.
// errType names the type the test alone makes an error type; marker is Go
// text the fact puts in the test build and not in the run build, which
// shows the fact reached the build that reads it.
type ownerFact struct{ name, imports, decls, test, errType, marker string }

var ownerFacts = []ownerFact{
	// An error row of a literal in a test makes a struct an error type.
	{name: "failure-record", errType: "Bang1",
		decls: "type Bang1 {}\n",
		test:  "test T {\n\tlet f = func() int fails Bang1 { return Bang1 }\n\tlet v = f() catch {\n\tcase Bang1:\n\t\t1\n\t}\n\tassert v == 1\n}\n"},
	// The error type is a defined type over another struct.
	{name: "defined-failure", errType: "Err2",
		decls: "type Base2 { Code int }\n\ntype Err2 Base2\n",
		test:  "test T {\n\tlet f = func() int fails Err2 { return Err2{Code: 2} }\n\tlet v = f() catch {\n\tcase Err2 as e:\n\t\te.Code\n\t}\n\tassert v == 2\n}\n"},
	// The program returns the struct as a value; a test fails with it.
	{name: "failure-and-value", errType: "Bang3",
		decls: "type Bang3 { Code int }\n\nfunc make3() Bang3 {\n\tBang3{Code: 3}\n}\n",
		test:  "test T {\n\tlet f = func() int fails Bang3 { return make3() }\n\tlet v = f() catch {\n\tcase Bang3 as e:\n\t\te.Code\n\t}\n\tassert v == 3 && make3().Code == 3\n}\n"},
	// An implicit conversion to an operation table needs an adapter.
	{name: "adapter", marker: "opsSq4AsShape4",
		decls: "type Shape4 {\n\tArea() int,\n}\n\ntype Sq4 {}\n\nfunc Area(_ Sq4) int {\n\t4\n}\n",
		test:  "test T {\n\tlet s Shape4 = Sq4\n\tassert s.Area() == 4\n}\n"},
	// A let without a value of a record holding a Map needs a default.
	{name: "default-map", marker: "ergoDefaultReg5",
		decls: "type Reg5 { M Map[string, int] }\n",
		test:  "test T {\n\tlet mut r Reg5\n\tr.M[\"a\"] = 5\n\tassert len(r.M) == 1\n}\n"},
	// A generic record and function instantiated only by a test.
	{name: "generic-instance",
		decls: "type Box6[T] { V T }\n\nfunc wrap6[T](v T) Box6[T] {\n\tBox6{V: v}\n}\n",
		test:  "test T {\n\tassert wrap6(\"six\").V == \"six\" && wrap6(6).V == 6\n}\n"},
	// A named context only a test opens.
	{name: "named-context", marker: "opsMem7AsStore7",
		decls: "type Store7 {\n\tPeek() int,\n}\n\ntype Mem7 {}\n\nfunc Peek(_ Mem7) int {\n\t7\n}\n\nlet Live7 = context {\n\tStore7: Mem7,\n}\n",
		test:  "test T {\n\tassert with Live7 { Store7.Peek() } == 7\n}\n"},
	// A context literal only a test opens.
	{name: "literal-context", marker: "opsMem8AsStore8",
		decls: "type Store8 {\n\tLook() int,\n}\n\ntype Mem8 {}\n\nfunc Look(_ Mem8) int {\n\t8\n}\n",
		test:  "test T {\n\tassert with context { Store8: Mem8 } { Store8.Look() } == 8\n}\n"},
	// A function generic over its needs, called only by a test.
	{name: "generic-needs", marker: "opsMem9AsStore9",
		decls: "type Store9 {\n\tGet9() int,\n}\n\ntype Mem9 {}\n\nfunc Get9(_ Mem9) int {\n\t9\n}\n\nfunc run9[F](f func() int needs F) int needs F {\n\tf()\n}\n",
		test:  "test T {\n\tassert with context { Store9: Mem9 } { run9() { Store9.Get9() } } == 9\n}\n"},
	// A library only a test uses; the run build's comparison blanks the
	// import too.
	{name: "library-import", marker: "ergo.Go[", imports: "import task\n\n",
		test: "test T {\n\tlet h = task.Go() { 10 }\n\tassert h.Wait() == 10\n}\n"},
	// A package let only a test reads.
	{name: "package-let",
		decls: "let Limit11 = 11\n",
		test:  "test T {\n\tassert Limit11 == 11\n}\n"},
	// A type that is an error type in the program too: the test adds
	// nothing.
	{name: "failure-also-production",
		decls: "type Bang13 {}\n\nfunc fail13() int fails Bang13 {\n\treturn Bang13\n}\n",
		test:  "test T {\n\tlet f = func() int fails Bang13 { return Bang13 }\n\tlet v = f() catch {\n\tcase Bang13:\n\t\t13\n\t}\n\tlet w = fail13() catch {\n\tcase Bang13:\n\t\t13\n\t}\n\tassert v == w\n}\n"},
}

// ownerProgram assembles base b with fact f's production source and test.
func ownerProgram(b ownerBase, f ownerFact) string {
	var s strings.Builder
	s.WriteString(f.imports)
	for _, d := range []string{b.decls, f.decls, b.main, f.test} {
		if d != "" {
			s.WriteString(d + "\n")
		}
	}
	return s.String()
}

func TestTestFactsStayInTests(t *testing.T) {
	needsBuild(t)
	type pair struct {
		name string
		base ownerBase
		fact ownerFact
	}
	var pairs []pair
	var builds []*lowered
	for _, b := range ownerBases {
		for _, f := range ownerFacts {
			name := "owner_" + strings.ReplaceAll(b.name+"_"+f.name, "-", "_")
			t.Run(b.name+"/"+f.name, func(t *testing.T) {
				src := ownerProgram(b, f)
				run := runBuildOmitsTests(t, name, src, f.imports != "")
				tb := lowerTestBuild(t, name+"_test", src)
				if f.errType != "" {
					errorShape(t, name, f.errType, run.mainGo(), tb.mainGo())
				}
				if f.marker != "" && (strings.Contains(run.mainGo(), f.marker) || !strings.Contains(tb.mainGo(), f.marker)) {
					t.Errorf("%s: %q belongs to the test build alone (run build has it: %t)", name, f.marker, strings.Contains(run.mainGo(), f.marker))
				}
				pairs = append(pairs, pair{name, b, f})
				builds = append(builds, run, tb)
			})
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
		stdout, stderr, code = runExe(t, exes[p.name+"_test"])
		if code != 0 || stdout != "ok T\nPASS (1 tests)\n" {
			t.Errorf("%s: test binary: exit %d, stdout %q, stderr %q", p.name, code, stdout, firstLines(stderr, 12))
		}
	}
}

// TestTestOnlyFailureProbe is the program of round 1's M2 finding
// (E3ar1-review/test-only-failure.ergo), whose test fails at run time: its
// run build gained textBang and Bang's Error method.
func TestTestOnlyFailureProbe(t *testing.T) {
	const src = "type Bang {}\nfunc main() {}\ntest T { let f = func() int fails Bang { return Bang }; assert try f() == 1 }\n"
	run := runBuildOmitsTests(t, "test_only_failure", src, false)
	errorShape(t, "test_only_failure", "Bang", run.mainGo(), lowerTestBuild(t, "test_only_failure_test", src).mainGo())
}

// errorShape: a type only a test makes an error type has an Error method
// and a text helper in the test build, and neither in the run build.
func errorShape(t *testing.T, name, typ, run, test string) {
	t.Helper()
	const directive = `(?:/\*line [^*]*\*/)?`
	method := regexp.MustCompile(`(?m)^` + directive + `func \(\w+ \*?` + typ + `\) Error\(\) string`)
	text := regexp.MustCompile(`(?m)^` + directive + `func text` + typ + `\(`)
	if method.MatchString(run) || text.MatchString(run) {
		t.Errorf("%s: the run build gives %s, an error type only in a test, an Error method or a text helper", name, typ)
	}
	if !method.MatchString(test) || !text.MatchString(test) {
		t.Errorf("%s: the test build gives %s, an error type of its test, no Error method or no text helper", name, typ)
	}
}
