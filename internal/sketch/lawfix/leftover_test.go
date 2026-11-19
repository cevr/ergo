package lawfix

import (
	"strings"
	"testing"
)

// TestLawsFileLeftWithTestsIsTests is the smallest repro of FIX-LEFTOVER
// (ld3 g11): a file of laws left holding only a test, once its law moves,
// was renamed after the Go type of the test's declaration
// ("*syntax.testdecl.ergo") and reported deleted ("no declaration is left
// in it") though the test was still in it. Such a file is a test file, so
// it takes the test files' name, tests.ergo, and the report names the
// rename alone. Both repro packages: the file starts with the law (a/), or
// with a file comment (b/). Mutants (mutants.json):
// fix-leftover-test-unnamed (a test names no concept: the file keeps its
// name) and fix-leftover-rename-reported-deleted.
func TestLawsFileLeftWithTestsIsTests(t *testing.T) {
	law := "// DoubleIsEven: twice anything is even.\nlaw DoubleIsEven(x int) { Double(x) % 2 == 0 }\n\n// DoubleOfTwo: twice two is four.\ntest DoubleOfTwo {\n\tassert Double(2) == 4\n}\n"
	for name, laws := range map[string]string{
		"a": law,
		"b": "// The laws of Double (a file comment, as dray's tlog_laws.ergo has).\n\n" + law,
	} {
		t.Run(name, func(t *testing.T) {
			srcs := map[string]string{
				"double.ergo":      "// Double is twice x.\nfunc Double(x int) int { x * 2 }\n",
				"double_laws.ergo": laws,
			}
			pkg, files, _ := checkSources(t, srcs)
			var r Report
			if err := r.apply("pkg", pkg, files, false); err != nil {
				t.Fatal(err)
			}
			text := r.Text()
			if want := "renamed double_laws.ergo -> tests.ergo (pkg)\n"; !strings.Contains(text, want) {
				t.Errorf("the report has no %q:\n%s", want, text)
			}
			if strings.Contains(text, "deleted") {
				t.Errorf("the report says a file was deleted, but the test is still in it:\n%s", text)
			}
			res, err := Plan(pkg, files)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]string{}
			for n, s := range srcs {
				out[n] = s
			}
			for _, n := range res.Deleted {
				delete(out, n)
			}
			for n, s := range res.Files {
				out[n] = s
			}
			_, after, ds := checkSources(t, out)
			if len(ds) > 0 {
				t.Fatalf("the fixed package has diagnostics: %v\n%s", ds, dump(out))
			}
			now := declsByFile(after)
			for decl, want := range map[string]string{"DoubleIsEven": "double.ergo", "DoubleOfTwo": "tests.ergo"} {
				if now[decl] != want {
					t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
				}
			}
		})
	}
}

// TestLeftoverTestsJoinTestsFile: when the package already has a
// tests.ergo (dray did), the tests a file of laws is left holding go into
// it, after its own, and the file of laws goes; before, the file kept its
// laws name, holding only tests. Mutant (mutants.json):
// fix-leftover-tests-file-ignored.
func TestLeftoverTestsJoinTestsFile(t *testing.T) {
	srcs := map[string]string{
		"double.ergo":      "// Double is twice x.\nfunc Double(x int) int { x * 2 }\n",
		"double_laws.ergo": "import strings\n\n// DoubleIsEven: twice anything is even.\nlaw DoubleIsEven(x int) { Double(x) % 2 == 0 }\n\n// DoubleOfTwo: twice two is four.\ntest DoubleOfTwo {\n\tassert Double(strings.Count(\"aa\", \"a\")) == 4\n}\n",
		"tests.ergo":       "// DoubleOfZero: twice zero is zero.\ntest DoubleOfZero {\n\tassert Double(0) == 0\n}\n",
	}
	pkg, files, _ := checkSources(t, srcs)
	var r Report
	if err := r.apply("pkg", pkg, files, false); err != nil {
		t.Fatal(err)
	}
	text := r.Text()
	if want := "moved tests DoubleOfTwo from double_laws.ergo to tests.ergo (pkg)\n"; !strings.Contains(text, want) {
		t.Errorf("the report has no %q:\n%s", want, text)
	}
	res, err := Plan(pkg, files)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for n, s := range srcs {
		out[n] = s
	}
	for _, n := range res.Deleted {
		delete(out, n)
	}
	for n, s := range res.Files {
		out[n] = s
	}
	if _, ok := out["double_laws.ergo"]; ok {
		t.Errorf("double_laws.ergo is left:\n%s", dump(out))
	}
	_, after, ds := checkSources(t, out)
	if len(ds) > 0 {
		t.Fatalf("the fixed package has diagnostics: %v\n%s", ds, dump(out))
	}
	now := declsByFile(after)
	for decl, want := range map[string]string{"DoubleIsEven": "double.ergo", "DoubleOfTwo": "tests.ergo", "DoubleOfZero": "tests.ergo"} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
		}
	}
	if i, j := strings.Index(out["tests.ergo"], "DoubleOfZero"), strings.Index(out["tests.ergo"], "DoubleOfTwo"); i < 0 || j < i {
		t.Errorf("the joined tests are not after tests.ergo's own:\n%s", out["tests.ergo"])
	}
	pkg2, files2, _ := checkSources(t, out)
	if again, err := Plan(pkg2, files2); err != nil || len(again.Files) > 0 || len(again.Deleted) > 0 {
		t.Errorf("a second run edits %v, deletes %v (%v)", again.Files, again.Deleted, err)
	}
}
