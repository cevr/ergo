package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestTopLevelLawRules: spec A2. A top-level law follows types.md Laws
// rules 2 and 4 to 10 as a member law does, with the scope "its
// parameters, then the package". Each program of sketchtest.LawTopCases
// gets exactly its want lines, no cascade.
func TestTopLevelLawRules(t *testing.T) {
	for _, tc := range sketchtest.LawTopCases {
		t.Run(tc.Name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.Src)
			want, have := expected(tc.Src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
		})
	}
}

// TestTopLevelLawMessages pins the text of the new diagnostics (the fix
// text the S108 gives, the namespace messages and the S001 at the bracket).
func TestTopLevelLawMessages(t *testing.T) {
	for _, tc := range []struct{ src, msg string }{
		{sketchtest.LawWrapperProbe, "MoneyLaws has laws and no operations; write it as a top-level law: law AddCommutes(…) { … }"},
		{"law A() { true }\n\nlaw A() { true }\n", "law A is declared twice"},
		{"law A() { true }\n\ntest A {\n\tassert true\n}\n", "test A is named like law A"},
		{"test A {\n\tassert true\n}\n\nlaw A() { true }\n", "law A is named like test A"},
		{"law L[T](x T) { true }\n", "a law has no type parameters; state it at concrete types, or as a member of the protocol's operation type"},
	} {
		_, diags := check.CheckSource(tc.src)
		if len(diags) != 1 || diags[0].Message != tc.msg {
			t.Errorf("%q: want one diagnostic %q, got\n\t%s", tc.src, tc.msg, describe(diags))
		}
	}
	// The S001 stands at the bracket.
	_, diags := check.CheckSource("law L[T](x T) { true }\n")
	if len(diags) != 1 || diags[0].Column != 6 || diags[0].Length != 1 {
		t.Errorf("want the S001 at column 6, length 1, got\n\t%s", describe(diags))
	}
}

// TestLawsOnlyTypeShape: spec A4. Every member sequence of length 0 to 3
// over {field, operation, law}, for a plain and a generic type, gets the
// verdict of its shape: no members, fields only, or operations with or
// without laws are clean; fields beside an operation or a law are the
// record rule (grammar.md Declarations 5, its message unchanged); laws and
// nothing else are the new S108, once, at the type's name.
func TestLawsOnlyTypeShape(t *testing.T) {
	const mixed = "a record holds data; declare the operations in their own type"
	kinds := []string{"field", "operation", "law"}
	var seqs [][]string
	var grow func(prefix []string)
	grow = func(prefix []string) {
		seqs = append(seqs, prefix)
		if len(prefix) == 3 {
			return
		}
		for _, k := range kinds {
			grow(append(append([]string{}, prefix...), k))
		}
	}
	grow(nil)
	if len(seqs) != 40 {
		t.Fatalf("%d member sequences, want 40", len(seqs))
	}
	for _, generic := range []bool{false, true} {
		head, elem := "type W", "int"
		if generic {
			head, elem = "type W[A]", "A"
		}
		for _, seq := range seqs {
			var members []string
			count := map[string]int{}
			for i, k := range seq {
				count[k]++
				switch k {
				case "field":
					members = append(members, fmt.Sprintf("\tF%d %s,", i, elem))
				case "operation":
					members = append(members, fmt.Sprintf("\tO%d() %s,", i, elem))
				case "law":
					members = append(members, fmt.Sprintf("\tlaw L%d(x %s) { true },", i, elem))
				}
			}
			src := head + " {\n" + strings.Join(members, "\n") + "\n}\n"
			if len(seq) == 0 {
				src = head + " {}\n"
			}
			var want string
			switch {
			case count["field"] > 0 && count["operation"]+count["law"] > 0:
				want = "S108@" + mixedLine(seq) + " " + mixed
			case count["law"] > 0 && count["operation"] == 0:
				first := 0
				for i, k := range seq {
					if k == "law" {
						first = i
						break
					}
				}
				want = fmt.Sprintf("S108@1 W has laws and no operations; write it as a top-level law: law L%d(…) { … }", first)
			}
			_, diags := check.CheckSource(src)
			var have []string
			for _, d := range diags {
				have = append(have, fmt.Sprintf("%s@%d %s", d.Code, d.Line, d.Message))
			}
			if got := strings.Join(have, "; "); got != want {
				t.Errorf("%v (generic %v): want %q, got %q\n%s", seq, generic, want, got, src)
			}
		}
	}
}

// mixedLine is the line of the record rule's diagnostic: the first member
// whose kind differs, field or not, from the first member's.
func mixedLine(seq []string) string {
	for i, k := range seq {
		if (k == "field") != (seq[0] == "field") {
			return fmt.Sprint(i + 2)
		}
	}
	return "?"
}
