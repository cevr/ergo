package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
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

// TestTopLevelLawFormat: spec A6. ergo fmt of every clean A2 program and
// every clean A3 top-level program is idempotent, keeps every token and
// comment in order, and the formatted program checks clean.
func TestTopLevelLawFormat(t *testing.T) {
	var srcs []string
	for _, tc := range sketchtest.LawTopCases {
		if len(expected(tc.Src)) == 0 {
			srcs = append(srcs, tc.Src)
		}
	}
	for _, l := range sketchtest.GenLaws(sketchtest.LawDomainSeed, sketchtest.LawDomainSize) {
		src, _ := sketchtest.PlaceLaw(false, l)
		if _, diags := check.CheckSource(src); len(diags) == 0 {
			srcs = append(srcs, src)
		}
	}
	if len(srcs) < 300 {
		t.Fatalf("%d clean programs, want at least 300", len(srcs))
	}
	for _, src := range srcs {
		once, err := format.Source(src)
		if err != nil {
			t.Fatalf("format: %v\n%s", err, src)
		}
		twice, err := format.Source(once)
		if err != nil || twice != once {
			t.Fatalf("not idempotent (%v):\n%s\n--- then\n%s", err, once, twice)
		}
		// Every token and comment keeps its order: a doc comment stays
		// before its law's keyword.
		before, _ := sketchtest.Interleaved(src)
		after, _ := sketchtest.Interleaved(once)
		if i, x, y := sketchtest.FirstDiff(before, after); i >= 0 {
			t.Fatalf("token or comment %d changed: %q became %q\n%s\n--- formatted\n%s", i, x, y, src, once)
		}
		if _, diags := check.CheckSource(once); len(diags) != 0 {
			t.Fatalf("the formatted program is refused:\n\t%s\n%s", describe(diags), once)
		}
	}
}

// TestTopLevelLawInfo: Law.Owner is nil for a top-level law, Package.Laws
// lists every law in source order, Defs maps a top-level law's name to its
// Law, and its facts are OwnerLaw's.
func TestTopLevelLawInfo(t *testing.T) {
	src := `law First(n int) { n == n }

type Codec {
	Encode(v int) string,

	law Second(v int) { Encode(v) == Encode(v) },
}

law Third(s string) { s == s }
`
	pkg, diags := check.CheckSource(src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n\t%s", describe(diags))
	}
	var names []string
	for _, l := range pkg.Laws {
		owner := "-"
		if l.Owner != nil {
			owner = l.Owner.Name
		}
		names = append(names, owner+"."+l.Name)
		if pkg.Info.Defs[l.Decl.Name] != check.Object(l) {
			t.Errorf("law %s: Defs does not map its name to the Law", l.Name)
		}
		if got := pkg.OwnerOf(l.Decl.Body); got != check.OwnerLaw {
			t.Errorf("law %s: body owner %v, want OwnerLaw", l.Name, got)
		}
		if !l.Fails.Empty() {
			t.Errorf("law %s: failure row %v, want empty", l.Name, l.Fails.Elems())
		}
	}
	if got, want := strings.Join(names, " "), "-.First Codec.Second -.Third"; got != want {
		t.Errorf("Package.Laws = %s, want %s", got, want)
	}
}
