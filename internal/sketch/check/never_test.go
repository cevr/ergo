package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// FKG2 (owner, 2026-11-13; types.md, Never): the prelude's Never has no
// value. A match leaves out what holds one, and needs no arm on a Never;
// an arm that names one, a literal of Never and `fails Never` are refused.
// Each row is a totality answer or a diagnostic's text; the mutants they
// kill are in testdata/mutants.json (fkg2-*).

const neverDecl = "type S = A{N int} | B{N int}\n"

func TestNeverTotality(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		// fkg2-empty-not-marked, fkg2-record-field-inhabited: Emit[S, Never]
		// and Done[Never] hold a Never, so no arm is missing.
		{"decision-without-emit-and-done", neverDecl + "func f(d Decision[S, Never]) int {\n\tmatch d {\n\tcase Enter[S]:\n\t\t1\n\tcase Stay[S]:\n\t\t2\n\tcase Ignore, Reject:\n\t\t3\n\t}\n}\n"},
		// fkg2-no-empty-match: a match on a Never has no arm, and is a
		// value of any type.
		{"empty-match", "func f(n Never) int {\n\tmatch n {}\n}\n"},
		// fkg2-all-empty-subject: a union whose every variant holds a
		// Never has no value either.
		{"union-of-empty-variants", "type U = X{N Never} | Y{M Never}\nfunc f(u U) string {\n\tmatch u {}\n}\n"},
		{"absurd", "func f(n Never) string { n.Absurd[string]() }\n"},
	} {
		if _, d := check.CheckSource(tc.src); len(d) > 0 {
			t.Errorf("%s: %s\n%s", tc.name, describe(d), tc.src)
		}
	}
}

func TestNeverRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// fkg2-emit-arm-admitted
		{"emit-arm", neverDecl + "func f(d Decision[S, Never]) int {\n\tmatch d {\n\tcase Emit[S, Never]:\n\t\t0\n\tcase _:\n\t\t1\n\t}\n}\n",
			"Emit[S, Never] has no value: it holds a Never, so this case never runs; leave it out (types.md, Never)"},
		// fkg2-empty-picked: a missing arm with a value is still named,
		// never one with none (Emit comes before Ignore).
		{"missing-ignore", neverDecl + "func f(d Decision[S, Never]) int {\n\tmatch d {\n\tcase Enter[S]:\n\t\t1\n\tcase Stay[S]:\n\t\t2\n\tcase Reject:\n\t\t3\n\t}\n}\n",
			"Ignore"},
		// fkg2-empty-not-covered: with Emit and Done left out, the arms
		// of the other variants are still split, so a missing B under
		// Enter is found.
		{"missing-nested", neverDecl + "func f(d Decision[S, Never]) int {\n\tmatch d {\n\tcase Enter[S]{Next: A}:\n\t\t1\n\tcase Stay[S]:\n\t\t2\n\tcase Ignore, Reject:\n\t\t3\n\t}\n}\n",
			"Next: B"},
		// fkg2-never-a-record: Never is no record, so no literal makes one.
		{"literal", "func f() Never { Never{} }\n", "cannot write a composite literal of Never"},
		// fkg2-fails-never-admitted
		{"fails-never", "func f() int fails Never { 1 }\n",
			"Never is no error row: a function that never fails writes no fails (types.md, Never)"},
		// Never is no bottom type: it is no int.
		{"no-subtyping", "func f(n Never) int { n }\n", "cannot use Never as int"},
	} {
		_, d := check.CheckSource(tc.src)
		found := false
		for _, x := range d {
			found = found || strings.Contains(x.Message, tc.want)
		}
		if !found {
			t.Errorf("%s: want a message holding %q, got %s\n%s", tc.name, tc.want, describe(d), tc.src)
		}
	}
}
