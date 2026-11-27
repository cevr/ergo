package lawfix

import (
	"strings"
	"testing"
)

// TestLawsFileLeftWithTestsIsTests: when a file of laws holds laws and tests
// whose subjects are in double.ergo plus an unplaced test (names nothing in
// the package), the placed laws and tests move beside their subject, and the
// file left holding only the unplaced test is renamed tests.ergo and never
// reported deleted. Mutants (mutants.json): fix-leftover-test-unnamed and
// fix-leftover-rename-reported-deleted.
func TestLawsFileLeftWithTestsIsTests(t *testing.T) {
	law := "// DoubleIsEven: twice anything is even.\nlaw DoubleIsEven(x int) { Double(x) % 2 == 0 }\n\ntest DoubleOfTwo {\n\tassert Double(2) == 4\n}\n\ntest AlwaysTrue {\n\tassert 1 + 1 == 2\n}\n"
	for name, laws := range map[string]string{
		"a": law,
		"b": "// The laws of Double.\n\n" + law,
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
				t.Errorf("the report says a file was deleted, but the unplaced test is still in it:\n%s", text)
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
			for decl, want := range map[string]string{"DoubleIsEven": "double.ergo", "DoubleOfTwo": "double.ergo", "AlwaysTrue": "tests.ergo"} {
				if now[decl] != want {
					t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
				}
			}
		})
	}
}

// TestLeftoverTestsJoinTestsFile: placed laws and tests move beside Double in
// double.ergo; unplaced tests left in a laws file join an existing tests.ergo
// that still holds declarations, and the laws file goes. Mutant
// (mutants.json): fix-leftover-tests-file-ignored.
func TestLeftoverTestsJoinTestsFile(t *testing.T) {
	srcs := map[string]string{
		"double.ergo":      "// Double is twice x.\nfunc Double(x int) int { x * 2 }\n",
		"double_laws.ergo": "import strings\n\n// DoubleIsEven: twice anything is even.\nlaw DoubleIsEven(x int) { Double(x) % 2 == 0 }\n\ntest DoubleOfTwo {\n\tassert Double(strings.Count(\"aa\", \"a\")) == 4\n}\n\ntest AlwaysTwo {\n\tassert strings.Count(\"aa\", \"a\") == 2\n}\n",
		"tests.ergo":       "test DoubleOfZero {\n\tassert Double(0) == 0\n}\n\ntest AlwaysZero {\n\tassert 0 == 0\n}\n",
	}
	pkg, files, _ := checkSources(t, srcs)
	var r Report
	if err := r.apply("pkg", pkg, files, false); err != nil {
		t.Fatal(err)
	}
	text := r.Text()
	if want := "moved tests AlwaysTwo from double_laws.ergo to tests.ergo (pkg)\n"; !strings.Contains(text, want) {
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
	for decl, want := range map[string]string{"DoubleIsEven": "double.ergo", "DoubleOfTwo": "double.ergo", "DoubleOfZero": "double.ergo", "AlwaysTwo": "tests.ergo", "AlwaysZero": "tests.ergo"} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
		}
	}
	if i, j := strings.Index(out["tests.ergo"], "AlwaysZero"), strings.Index(out["tests.ergo"], "AlwaysTwo"); i < 0 || j < i {
		t.Errorf("the joined tests are not after tests.ergo's own:\n%s", out["tests.ergo"])
	}
	pkg2, files2, _ := checkSources(t, out)
	if again, err := Plan(pkg2, files2); err != nil || len(again.Files) > 0 || len(again.Deleted) > 0 {
		t.Errorf("a second run edits %v, deletes %v (%v)", again.Files, again.Deleted, err)
	}
}

// TestLawsAndTestsFilesWithAllPlacedAreDeleted verifies that when all laws and
// tests in double_laws.ergo and tests.ergo have package subjects, every one
// relocates beside its subject and both source files are deleted.
func TestLawsAndTestsFilesWithAllPlacedAreDeleted(t *testing.T) {
	srcs := map[string]string{
		"double.ergo":      "// Double is twice x.\nfunc Double(x int) int { x * 2 }\n",
		"double_laws.ergo": "import strings\n\n// DoubleIsEven: twice anything is even.\nlaw DoubleIsEven(x int) { Double(x) % 2 == 0 }\n\ntest DoubleOfTwo {\n\tassert Double(strings.Count(\"aa\", \"a\")) == 4\n}\n",
		"tests.ergo":       "test DoubleOfZero {\n\tassert Double(0) == 0\n}\n",
	}
	pkg, files, _ := checkSources(t, srcs)
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
	for _, gone := range []string{"double_laws.ergo", "tests.ergo"} {
		if _, ok := out[gone]; ok {
			t.Errorf("%s was not deleted:\n%s", gone, dump(out))
		}
	}
	_, after, ds := checkSources(t, out)
	if len(ds) > 0 {
		t.Fatalf("the fixed package has diagnostics: %v\n%s", ds, dump(out))
	}
	now := declsByFile(after)
	for decl, want := range map[string]string{"DoubleIsEven": "double.ergo", "DoubleOfTwo": "double.ergo", "DoubleOfZero": "double.ergo"} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
		}
	}
}
