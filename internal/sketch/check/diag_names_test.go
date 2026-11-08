package check_test

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// K4 (M9, M10): diagnostics name a package's own types as its source
// writes them, another package's by its import name, and the named
// messages say what happened and what to write.

// TestDiagnosticMessagesSayWhatHappened is one row per synthesis repro:
// the message holds the new text.
func TestDiagnosticMessagesSayWhatHappened(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"needs-param", "func count(needs []int) int { len(needs) }\n",
			`expected a type, found "needs": needs is a keyword (the capability row) and is not a name; choose another name`},
		{"needs-let", "func f() int {\n\tlet needs = 1\n\tneeds\n}\n",
			`expected a pattern, found "needs": needs is a keyword`},
		{"with-no-result", "type Oops {}\nfunc work(n int) fails Oops needs Console {\n\tif n < 0 {\n\t\treturn Oops\n\t}\n\tConsole.Println(\"w\")\n}\nfunc main() needs Console {\n\tlet _ = with context { } { work(1) } catch {\n\tcase Oops:\n\t\treturn\n\t}\n}\n",
			"work returns nothing, and the block's value is used: use the block as a statement, without let _ ="},
		{"extern-value", "import strings\nfunc apply(f func(s string) string, s string) string { f(s) }\nfunc up() string { apply(strings.ToUpper, \"a\") }\n",
			"strings.ToUpper is an extern and is not a value; pass func(s string) string { strings.ToUpper(s) }"},
		{"dot-op-type", "type Engine { Compile(p string) bool, law L(p string) { Compile(p) == Compile(p) } }\nfunc Holds(e Engine, p string) bool { e.Compile(p) }\nfunc All(e Engine) bool { e.Holds(\"a\") }\n",
			"Engine has no field or method Holds; Engine is an operation type, so a dot call on its value reaches only its operations, and Holds is a function on it: call Holds(e, …)"},
		{"dot-instance", "type Box[T] { v T }\nfunc Twice(b *Box[int]) { b.v = b.v * 2 }\nfunc main() needs Console {\n\tlet mut b = Box[int]{v: 1}\n\tb.Twice()\n\tConsole.Println(\"\\(b.v)\")\n}\n",
			"Twice takes *Box[int], one instance, and a dot call reaches a function on a generic type only when its first parameter takes the function's own type parameters: write func Twice[T](b *Box[T]), or call Twice(&b)"},
		{"extern-value-with-a-row", "import strconv\nfunc apply(f func(s string) int fails strconv.Malformed | strconv.OutOfRange, s string) int fails strconv.Malformed | strconv.OutOfRange { try f(s) }\nfunc n() int fails strconv.Malformed | strconv.OutOfRange { try apply(strconv.Atoi, \"1\") }\n",
			"strconv.Atoi is an extern and is not a value; pass func(s string) int fails strconv.Malformed | strconv.OutOfRange { strconv.Atoi(s) }"},
		{"duplicate-function", "type Engine {}\ntype Program { n int }\nfunc Compile(s string) Program { Program{n: 1} }\nfunc Compile(_ Engine, s string) Program { Program{n: 2} }\n",
			"Compile is already declared in this package; functions share a name only when each one's first parameter is a distinct named type declared in this package (types.md, Methods 3)"},
		{"law-hides-type", "type Item { N int }\ntype Store { Item(n int) Item, law ItemKeeps(n int) { Item(n) == Item{N: n} } }\n",
			"Item is the operation Store.Item here, which hides the type Item in the law; rename the operation or the type"},
		{"prelude-name", "func f() int {\n\tlet Task = 1\n\tTask\n}\n",
			"Task is a prelude name (the prelude type Task[T, E]), which cannot be redeclared"},
		{"prelude-constraint", "func f() int {\n\tlet number = 1\n\tnumber\n}\n",
			"number is a prelude name (the predeclared constraint number)"},
		{"duration-times-int", "func late(d duration) duration { d / 4 }\n",
			"a duration is not multiplied or divided by an integer (#175); write duration(int64(d) / 4)"},
		{"duration-times-local", "func late(d duration, n int) duration { d * n }\n",
			"write duration(int64(d) * int64(n))"},
		{"catch-of-a-failing-argument", "type Missing { Name string }\nfunc find(name string) string fails Missing {\n\tif name == \"\" {\n\t\treturn Missing{Name: name}\n\t}\n\tname\n}\nfunc shown(s string) bool { s != \"\" }\nfunc f() bool {\n\tshown(find(\"x\")) catch {\n\tcase Missing:\n\t\tfalse\n\t}\n}\n",
			"a catch handles the failures of shown(…), not of its argument find(…); catch find(…) first (rows.md, Catch 5)"},
		{"sub-slice-by-range", "func first(words []string, n int) []string { words[0..n] }\n",
			"[]string is indexed by an integer, not a range: Ergo has no sub-slice expression"},
		{"own-type-bare", "type Broken { Reason string }\nfunc get() string fails Broken { Broken{Reason: \"x\"} }\nfunc Use() string { get() }\n",
			"{Broken} escapes function Use"},
	} {
		_, d := check.CheckSource(tc.src)
		found := false
		for _, x := range d {
			found = found || strings.Contains(x.Message, tc.want)
		}
		if !found {
			t.Errorf("%s: want a message holding %q, got %s", tc.name, tc.want, describe(d))
		}
	}
}

// TestOneDiagnosticEach is the cascade half: synthesis repros g1 (a catch
// over a failing argument), cron's words[0..n] and f-5's dot call on an
// instance each give one diagnostic.
func TestOneDiagnosticEach(t *testing.T) {
	for _, src := range []string{
		"type Missing { Name string }\nfunc find(name string) string fails Missing {\n\tif name == \"\" {\n\t\treturn Missing{Name: name}\n\t}\n\tname\n}\nfunc shown(s string) bool { s != \"\" }\nfunc main() needs Console {\n\tlet ok = shown(find(\"x\")) catch {\n\tcase Missing:\n\t\tfalse\n\t}\n\tConsole.Println(\"\\(ok)\")\n}\n",
		"func first(words []string, n int) []string { words[0..n] }\n",
		"type Box[T] { v T }\nfunc Twice(b *Box[int]) { b.v = b.v * 2 }\nfunc main() needs Console {\n\tlet mut b = Box[int]{v: 1}\n\tb.Twice()\n\tConsole.Println(\"\\(b.v)\")\n}\n",
	} {
		if _, d := check.CheckSource(src); len(d) != 1 {
			t.Errorf("want one diagnostic, got %s\n%s", describe(d), src)
		}
	}
}

// The library of the type-name property: every type name starts with A,
// so a name tells which package declares it.
const namesLib = `type ARec { N int }
type AErr { Code int }
type AUnion = AOne{} | ATwo{}
func MakeRec() ARec { ARec{N: 1} }
func Fail(n int) int fails AErr { if n < 0 { return AErr{Code: n} } else { n } }
`

// The main package's own types start with M.
const namesMain = `type MRec { N int }
type MErr { Code int }
type MUnion = MOne{} | MTwo{}
func mfail(n int) int fails MErr { if n < 0 { return MErr{Code: n} } else { n } }
`

// faultTemplates are functions with one type fault each, over the
// placeholders REC, UNION, ONE, TWO and FAIL, which a package fills with
// its own types or the library's.
var faultTemplates = []string{
	"func fK() int { REC{N: 1} }\n",
	"func fK() int { FAIL(1) }\n",
	"func fK(u UNION) int {\n\tmatch u {\n\tcase ONE:\n\t\t1\n\t}\n}\n",
	"func fK() REC { 1 }\n",
	"func fK(r REC) int { r.Missing }\n",
	"func fK(c bool) int {\n\tlet x = if c { REC{N: 1} } else { 2 }\n\t_ = x\n\t0\n}\n",
	"func fK(u UNION) REC { u }\n",
	"func fK(r REC) bool { r == ONE }\n",
}

// faults writes n generated faults, each filled with the library's types
// (qualified by q, "" inside the library) or the main package's.
func faults(r *rand.Rand, n int, lib bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		f := faultTemplates[r.Intn(len(faultTemplates))]
		q := "a."
		names := map[string]string{"REC": "ARec", "UNION": "AUnion", "ONE": "AOne", "TWO": "ATwo", "FAIL": "Fail"}
		if lib {
			q = ""
		} else if r.Intn(2) == 0 {
			q = ""
			names = map[string]string{"REC": "MRec", "UNION": "MUnion", "ONE": "MOne", "TWO": "MTwo", "FAIL": "mfail"}
		}
		for _, k := range []string{"UNION", "REC", "ONE", "TWO", "FAIL"} {
			f = strings.ReplaceAll(f, k, q+names[k])
		}
		b.WriteString(strings.ReplaceAll(f, "fK", fmt.Sprintf("f%d", i)))
	}
	return b.String()
}

type namesImporter struct{ a *check.Package }

func (i namesImporter) Import(path string) (*check.Package, error) {
	if path == "a" {
		return i.a, nil
	}
	return check.Std.Import(path)
}

var typeToken = regexp.MustCompile(`([A-Za-z_]\w*\.)?\b([AM][A-Z][a-z]\w*)\b`)

// resolves reports the type names of msg that do not resolve in the
// package that reports it: own (A for the library, M for main) names
// bare, the library's names from main as a.Name, and nothing as main.Name.
func unresolved(msg string, lib bool) []string {
	var bad []string
	for _, m := range typeToken.FindAllStringSubmatch(msg, -1) {
		prefix, name := m[1], m[2]
		ownA := strings.HasPrefix(name, "A")
		switch {
		case prefix == "" && lib && ownA, prefix == "" && !lib && !ownA, prefix == "a." && !lib && ownA:
		case prefix != "" && prefix != "a." && prefix != "main.":
			// Another selector (a field read, a package function).
		default:
			bad = append(bad, m[0])
		}
	}
	return bad
}

// TestDiagnosticTypeNamesResolve is the type-name property: for generated
// two-package programs (a library a with faults, and a main package that
// imports a clean a and has faults over both packages' types), every type
// name in every message resolves in the package that reports it.
func TestDiagnosticTypeNamesResolve(t *testing.T) {
	parse := func(src string) *syntax.File {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, src)
		}
		return f
	}
	clean, ds := check.Check("a", []*syntax.File{parse(namesLib)}, nil)
	if len(ds) > 0 {
		t.Fatalf("library: %s", describe(ds))
	}
	r := rand.New(rand.NewSource(41))
	messages := 0
	for i := 0; i < 60; i++ {
		libSrc := namesLib + faults(r, 1+r.Intn(4), true)
		_, ld := check.Check("a", []*syntax.File{parse(libSrc)}, nil)
		mainSrc := "import a\n" + namesMain + "func use() int { a.MakeRec().N + a.Fail(1) catch { case a.AErr: 0 } }\n" + faults(r, 1+r.Intn(4), false)
		_, md := check.Check("main", []*syntax.File{parse(mainSrc)}, namesImporter{clean})
		for _, set := range []struct {
			lib bool
			ds  []check.Diagnostic
			src string
		}{{true, ld, libSrc}, {false, md, mainSrc}} {
			for _, d := range set.ds {
				messages++
				if bad := unresolved(d.Message, set.lib); len(bad) > 0 {
					t.Fatalf("program %d: %v do not resolve in the reporting package:\n%s\n%s", i, bad, d.String(), set.src)
				}
			}
		}
	}
	if messages < 200 {
		t.Errorf("only %d messages; the generator misses faults", messages)
	}
}

// TestDotCallHintAcrossPackages: a dot call on another package's type
// names that package's function through the package, offers no rewrite
// of a declaration the caller does not own, and never names an
// unexported function the caller cannot call.
func TestDotCallHintAcrossPackages(t *testing.T) {
	const lib = "type Box[T] { V T }\nfunc Twice(b *Box[int]) { b.V = b.V * 2 }\nfunc half(b Box[int]) int { b.V / 2 }\nfunc Half(b Box[int]) int { half(b) }\n"
	lf, err := syntax.Parse(lib)
	if err != nil {
		t.Fatal(err)
	}
	a, ds := check.Check("a", []*syntax.File{lf}, nil)
	if len(ds) > 0 {
		t.Fatalf("library: %s", describe(ds))
	}
	for _, tc := range []struct{ name, body, want, not string }{
		{"exported", "let mut b = a.Box[int]{V: 1}\n\tb.Twice()\n\tb.V", "Twice takes *a.Box[int], one instance, and a dot call reaches a function on a generic type only when its first parameter takes the function's own type parameters: call a.Twice(&b)", "write func"},
		{"unexported", "let b = a.Box[int]{V: 1}\n\tb.half()", "a.Box[int] has no field or method half", "half takes"},
	} {
		src := "import a\nfunc f() int {\n\t" + tc.body + "\n}\n"
		mf, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, md := check.Check("main", []*syntax.File{mf}, namesImporter{a})
		found := false
		for _, d := range md {
			found = found || strings.Contains(d.Message, tc.want)
			if strings.Contains(d.Message, tc.not) {
				t.Errorf("%s: %q holds %q", tc.name, d.Message, tc.not)
			}
		}
		if !found {
			t.Errorf("%s: want a message holding %q, got %s", tc.name, tc.want, describe(md))
		}
	}
}
