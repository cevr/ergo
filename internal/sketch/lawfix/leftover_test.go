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
