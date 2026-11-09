package check

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// namedRowDecls declares five error types and the union U of a subset of
// them; the subset is the generator's.
func namedRowDecls(members []int) string {
	var b strings.Builder
	for i := range 5 {
		fmt.Fprintf(&b, "type E%d { N int }\n", i)
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = fmt.Sprintf("E%d", m)
	}
	fmt.Fprintf(&b, "type U = %s\n", strings.Join(names, " | "))
	return b.String()
}

func spelled(members []int) string {
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = fmt.Sprintf("E%d", m)
	}
	return strings.Join(names, " | ")
}

// genNamedRowProgram writes a program whose function f fails ROW, with
// callers that try, catch (total or under try, with arms of the five
// types in random order and subsets, some outside ROW), return f's call,
// pass f where a function type with ROW is expected, and pass f to a
// function generic in its row (the row is inferred). ROW is the one
// placeholder: the property fills it with `U` and with U's members
// spelled, so the two programs have the same lines.
func genNamedRowProgram(r *rand.Rand, members []int) string {
	var b strings.Builder
	b.WriteString(namedRowDecls(members))
	b.WriteString("func f(n int) int fails ROW {\n")
	for _, m := range members {
		fmt.Fprintf(&b, "\tif n == %d {\n\t\treturn E%d{N: n}\n\t}\n", m, m)
	}
	b.WriteString("\tn\n}\n")
	b.WriteString("func run[E](g func(int) int fails E) int fails E {\n\ttry g(0)\n}\n")
	callerRow := func() string {
		if r.IntN(2) == 0 {
			return " fails " + spelled(members)
		}
		var picked []string
		for i := range 5 {
			if r.IntN(2) == 0 {
				picked = append(picked, fmt.Sprintf("E%d", i))
			}
		}
		if len(picked) == 0 || r.IntN(4) == 0 {
			return ""
		}
		return " fails " + strings.Join(picked, " | ")
	}
	arms := func() string {
		perm := r.Perm(5)[:1+r.IntN(5)]
		var parts []string
		for _, m := range perm {
			parts = append(parts, fmt.Sprintf("case E%d: %d", m, m))
		}
		if r.IntN(4) == 0 {
			parts = append(parts, "case _: 9")
		}
		return strings.Join(parts, "; ")
	}
	for k := range 3 {
		fmt.Fprintf(&b, "func c%d(n int) int%s {\n", k, callerRow())
		switch r.IntN(6) {
		case 0:
			b.WriteString("\ttry f(n)\n")
		case 1:
			fmt.Fprintf(&b, "\tf(n) catch { %s }\n", arms())
		case 2:
			fmt.Fprintf(&b, "\ttry f(n) catch { %s }\n", arms())
		case 3:
			b.WriteString("\treturn f(n)\n")
		case 4:
			b.WriteString("\tlet g func(int) int fails ROW = f\n\ttry g(n)\n")
		case 5:
			b.WriteString("\ttry run(f)\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func verdicts(src string) []string {
	_, diags := CheckSource(src)
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%s@%d", d.Code, d.Line))
	}
	slices.Sort(out)
	return out
}

// TestNamedRowAgreesWithSpelledRow (L2 acceptance): for random unions of
// error types, a program that writes `fails U` (and U as a function
// type's row and as a row argument) has the same diagnostics, code and
// line, as the program that spells U's variants. A failing seed prints
// both programs.
func TestNamedRowAgreesWithSpelledRow(t *testing.T) {
	clean, refused := 0, 0
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x4c32))
		members := r.Perm(5)[:1+r.IntN(4)]
		slices.Sort(members)
		src := genNamedRowProgram(r, members)
		named := strings.ReplaceAll(src, "ROW", "U")
		spelt := strings.ReplaceAll(src, "ROW", spelled(members))
		a, b := verdicts(named), verdicts(spelt)
		if !slices.Equal(a, b) {
			t.Fatalf("seed %d: fails U gives %v, the spelled row gives %v\n--- named\n%s\n--- spelled\n%s", seed, a, b, named, spelt)
		}
		if len(a) == 0 {
			clean++
		} else {
			refused++
		}
	}
	// Both verdicts occur, so the property compares more than one kind.
	if clean < 10 || refused < 10 {
		t.Fatalf("the generator gave %d clean and %d refused programs; want at least 10 of each", clean, refused)
	}
}

// TestNamedRowCatch: `case U:` and `case U as u:` in a catch handle each
// variant of U the caught row holds and bind the union; a union with no
// variant in the row, or whose variants the arms before it handle, is
// S108; a variant after the union is caught twice.
func TestNamedRowCatch(t *testing.T) {
	const decls = "type Bad { At int }\ntype Oops { At int }\ntype Other {}\ntype Fail = Bad | Oops\n" +
		"func f(n int) int fails Fail | Other {\n\tif n == 1 {\n\t\treturn Other\n\t}\n\tif n == 2 {\n\t\treturn Bad{At: 2}\n\t}\n\tn\n}\n" +
		"func only(n int) int fails Bad {\n\tif n == 1 {\n\t\treturn Bad{At: 1}\n\t}\n\tn\n}\n" +
		"func describe(x Fail) int {\n\tmatch x {\n\tcase Bad{At: a}:\n\t\ta\n\tcase Oops{At: a}:\n\t\t-a\n\t}\n}\n"
	for _, tc := range []struct{ body, want string }{
		{"f(n) catch { case Fail as x: describe(x); case Other: 0 }", ""},
		{"f(n) catch { case Fail: 1; case Other: 0 }", ""},
		{"try f(n) catch { case Fail: 1 }", "S101"}, // Other escapes into a function with no row
		{"only(n) catch { case Fail as x: describe(x) }", ""},
		{"f(n) catch { case Bad: 1; case Fail: 2; case Other: 0 }", ""},
		{"f(n) catch { case Bad: 1; case Oops: 3; case Fail: 2; case Other: 0 }", "caught twice"},
		{"f(n) catch { case Fail: 2; case Bad: 1; case Other: 0 }", "caught twice"},
		{"f(n) catch { case Other: 0 }", "does not handle"},
		{"only(n) catch { case Other: 0; case _: 1 }", "not in the caught row"},
	} {
		src := decls + "func G(n int) int {\n\t" + tc.body + "\n}\n"
		_, diags := CheckSource(src)
		got := describeDiags(diags)
		if tc.want == "" && len(diags) > 0 || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %s, want %q", tc.body, got, tc.want)
		}
	}
	// A union with no variant in the row.
	src := "type Bad {}\ntype Oops {}\ntype Fail = Bad | Oops\ntype Other {}\nfunc h() int fails Other { return Other }\nfunc G() int {\n\th() catch { case Other: 0; case Fail: 1 }\n}\n"
	if _, diags := CheckSource(src); !strings.Contains(describeDiags(diags), "has no variant in the caught row") {
		t.Errorf("a union outside the row: got %s", describeDiags(diags))
	}
}
