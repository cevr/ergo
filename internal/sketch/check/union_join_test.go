package check_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// K3 (M8): without an expected type, the branches of an if or match that
// are variants of one union (or that union) join to the union (rows.md,
// Totality 3); branches of one variant keep the variant.

// letType checks src and returns the type of its local x, or the
// diagnostics when it does not check.
func letType(t *testing.T, src string) (string, []check.Diagnostic) {
	t.Helper()
	pkg, d := check.CheckSource(src)
	if len(d) > 0 {
		return "", d
	}
	for id, o := range pkg.Info.Defs {
		if v, ok := o.(*check.Var); ok && id.Name == "x" {
			return v.Type.String(), nil
		}
	}
	t.Fatalf("no local x in\n%s", src)
	return "", nil
}

// TestUnionJoinRepro is synthesis repro c10: line 8 checks, and line 18
// names the let to write.
func TestUnionJoinRepro(t *testing.T) {
	src := `type State = Open{} | Closed{}

func parse(s string) Option[State] {
	let state = match s {
	case "open":
		Open
	case "closed":
		Closed
	case _:
		return None
	}
	Some[State]{Value: state}
}

func best(xs []int) Option[int] {
	let mut b = None
	for x in xs {
		b = Some[int]{Value: x}
	}
	b
}
`
	_, d := check.CheckSource(src)
	if len(d) != 1 || d[0].Line != 18 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, "b is typed None by its value; write let mut b Option[int] = None") {
		t.Fatalf("want one S108 at line 18 naming the let, got %s", describe(d))
	}
}

// TestUnionJoinForms covers the generic union, a nest of a match in an if,
// two unions that list both branches (no join), and a single variant.
func TestUnionJoinForms(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"generic", "func f(n int) bool {\n\tlet x = if n > 0 { Some{Value: n} } else { None }\n\tx == None\n}\n", "Option[int]"},
		{"generic-none-first", "func f(n int) bool {\n\tlet x = if n > 0 { None } else { Some{Value: n} }\n\tx == None\n}\n", "Option[int]"},
		{"only-none", "func f(n int) bool {\n\tlet x = if n > 0 { None } else { None }\n\tx == None\n}\n", "None"},
		{"nested", "type U = A{} | B{} | C{}\nfunc f(n int, c bool) bool {\n\tlet x = if c {\n\t\tmatch n {\n\t\tcase 0:\n\t\t\tA\n\t\tcase _:\n\t\t\tB\n\t\t}\n\t} else {\n\t\tC\n\t}\n\tx == A\n}\n", "main.U"},
		{"union-first", "type U = A{} | B{}\nfunc f(u U, c bool) bool {\n\tlet x = if c { u } else { B }\n\tx == u\n}\n", "main.U"},
		{"one-variant", "type U = A{} | B{}\nfunc f(c bool) bool {\n\tlet x = if c { A } else { A }\n\tx == A\n}\n", "main.A"},
		{"ambiguous", "type U = A{} | B{}\ntype V = A | B | C{}\nfunc f(c bool) bool {\n\tlet x = if c { A } else { B }\n\tx == A\n}\n", ""},
		{"written-type", "type U = A{} | B{}\ntype V = A | B | C{}\nfunc f(c bool) bool {\n\tlet x V = if c { A } else { B }\n\tx == A\n}\n", "main.V"},
	} {
		got, d := letType(t, tc.src)
		switch {
		case tc.want == "" && len(d) == 0:
			t.Errorf("%s: want a diagnostic, got type %s", tc.name, got)
		case tc.want != "" && got != tc.want:
			t.Errorf("%s: want %s, got %q %s", tc.name, tc.want, got, describe(d))
		}
	}
}

// joinBranch is one branch kind of the property: a variant A or B of U,
// a value of U, a variant C of another union V, or an unrelated record R.
var joinBranches = map[string]struct{ text, typ string }{
	"A": {"A", "main.A"},
	"B": {"B", "main.B"},
	"U": {"u", "main.U"},
	"C": {"C", "main.C"},
	"R": {"R{N: 1}", "main.R"},
}

// refJoin is the reference join table: the branches have the first
// branch's type, except that variants of U and U join to U.
func refJoin(kinds []string) (string, bool) {
	inU := func(k string) bool { return k == "A" || k == "B" || k == "U" }
	t := kinds[0]
	for _, k := range kinds[1:] {
		switch {
		case k == t:
		case t == "U" && inU(k):
		case inU(t) && inU(k):
			t = "U"
		default:
			return "", false
		}
	}
	return joinBranches[t].typ, true
}

// TestUnionJoinTable is the join property: for generated lists of 2 to 4
// branch kinds, as an if chain and as a match, the verdict and the type of
// let x = … are the reference table's.
func TestUnionJoinTable(t *testing.T) {
	const decls = "type U = A{} | B{}\ntype V = C{} | D{}\ntype R { N int }\n"
	r := rand.New(rand.NewSource(31))
	names := []string{"A", "B", "U", "C", "R"}
	seen := map[string]int{}
	for i := 0; i < 300; i++ {
		kinds := make([]string, 2+r.Intn(3))
		for j := range kinds {
			kinds[j] = names[r.Intn(len(names))]
		}
		want, ok := refJoin(kinds)
		for _, form := range []string{"if", "match"} {
			var b strings.Builder
			b.WriteString(decls + "func f(n int, u U) int {\n\tlet x = ")
			for j, k := range kinds {
				text := joinBranches[k].text
				switch {
				case form == "if" && j == 0:
					fmt.Fprintf(&b, "if n == 0 { %s }", text)
				case form == "if" && j == len(kinds)-1:
					fmt.Fprintf(&b, " else { %s }", text)
				case form == "if":
					fmt.Fprintf(&b, " else if n == %d { %s }", j, text)
				case j == 0:
					fmt.Fprintf(&b, "match n {\n\tcase 0:\n\t\t%s\n", text)
				case j == len(kinds)-1:
					fmt.Fprintf(&b, "\tcase _:\n\t\t%s\n\t}", text)
				default:
					fmt.Fprintf(&b, "\tcase %d:\n\t\t%s\n", j, text)
				}
			}
			b.WriteString("\n\t_ = x\n\tn\n}\n")
			src := b.String()
			got, d := letType(t, src)
			switch {
			case ok && got != want:
				t.Fatalf("case %d %v (%s): want %s, got %q %s\n%s", i, kinds, form, want, got, describe(d), src)
			case !ok && len(d) == 0:
				t.Fatalf("case %d %v (%s): want a diagnostic, got type %s\n%s", i, kinds, form, got, src)
			}
		}
		if ok {
			seen[want]++
		} else {
			seen["refused"]++
		}
	}
	for _, k := range []string{"main.U", "main.A", "main.C", "main.R", "refused"} {
		if seen[k] == 0 {
			t.Errorf("the generator never reached the verdict %s: %v", k, seen)
		}
	}
}
