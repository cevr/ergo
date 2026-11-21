package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

const skipMessage = "skip statements are only allowed in a test or law body"

// TestSkipPlacement is the skip placement rule (tests.go rule 6) over the
// assert rule's domain: every statement position the parser produces ×
// every lexical owner (tests_assert_domain_test.go). Each program puts a
// skip before each of the position's asserts, so a skip stands where an
// assert may, and its verdict is the assert's: the owner's label, which
// TestAssertPlacement holds to the parsed tree. It catches
// tskip-admit-anywhere and tskip-literal-admits, which TestAssertPlacement
// does not (the skip's verdict is apart from the assert's).
func TestSkipPlacement(t *testing.T) {
	for oname, owner := range assertOwners {
		for pname, pos := range assertPositions {
			if owner.noParams && pos.params != "" {
				continue
			}
			name := pname + " in " + oname
			stmts := strings.ReplaceAll(pos.stmts, "$assert", "$skip \"no host\"\nassert")
			full := assertDecls + owner.program(pos.params, stmts)
			var marked []string
			for i := strings.Index(full, "$"); i >= 0; i = strings.Index(full, "$") {
				marked = append(marked, fmt.Sprintf("S108:%d", strings.Count(full[:i], "\n")+1))
				full = full[:i] + full[i+1:]
			}
			var want []string
			if !owner.admits {
				want = marked
			}
			_, diags := check.CheckSource(full)
			var got []string
			for _, d := range diags {
				if strings.Contains(d.Message, skipMessage) {
					got = append(got, fmt.Sprintf("%s:%d", d.Code, d.Line))
				}
			}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("%s: want misplaced skips %v, got %v\n%s", name, want, diags, full)
			}
		}
	}
}

// TestSkipNamesItsReason holds the diagnostics of skip that are not its
// placement in a test, each a diagnostic's text and line: the reason is a
// string literal that is not blank and not interpolated, a rewrite law's
// sides and a function's body are no place for it, and skip is a
// contextual keyword, so the name skip stays an ordinary name, in a test
// too. It catches tskip-any-reason, tskip-rewrite-admits and the parser
// mutant tskip-interpolated-reason.
func TestSkipNamesItsReason(t *testing.T) {
	const reason = "skip names its reason: a string literal that is not blank"
	for _, row := range []struct {
		name, src, message string
		line               int
	}{
		{"blank", "test T {\n\tskip \" \t\"\n}\n", reason, 2},
		{"interpolated", "test T {\n\tlet n = 1\n\tskip \"host \\(n)\"\n}\n", "skip takes a string literal with no interpolation", 3},
		{"rewrite law", "type C {\n\tGet() int,\n\n\tlaw Same() {\n\t\tskip \"no host\"\n\t\tGet()\n\t} == {\n\t\tGet()\n\t},\n}\n", skipMessage, 5},
		{"function body", "func f() {\n\tskip \"no host\"\n}\n", skipMessage, 2},
		{"literal reason", "test T {\n\tskip \"no host\"\n}\n", "", 0},
		{"names", "type R { skip bool }\n\nfunc skip(n int) int { n }\n\ntest T {\n\tlet r = R{skip: true}\n\tassert r.skip\n\tassert skip(1) == 1\n\tskip \"after the names\"\n}\n", "", 0},
	} {
		_, diags := check.CheckSource(row.src)
		var got []string
		for _, d := range diags {
			if strings.Contains(d.Message, "skip") {
				got = append(got, fmt.Sprintf("%d: %s", d.Line, d.Message))
			}
		}
		switch {
		case row.message == "" && len(diags) > 0:
			t.Errorf("%s: want a clean check, got %v\n%s", row.name, diags, row.src)
		case row.message != "" && (len(got) == 0 || !strings.Contains(got[0], row.message) || !strings.HasPrefix(got[0], fmt.Sprintf("%d: ", row.line))):
			t.Errorf("%s: want line %d %q first, got %v (all: %v)\n%s", row.name, row.line, row.message, got, diags, row.src)
		}
	}
}
