package check

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// deriveImporter serves the test's packages and the standard library.
type deriveImporter map[string]*Package

func (m deriveImporter) Import(path string) (*Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return Std.Import(path)
}

func checkDerive(t *testing.T, imp deriveImporter, path, src string) (*Package, []Diagnostic, *syntax.File) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s does not parse: %v\n%s", path, err, src)
	}
	f.Name = path + ".ergo"
	pkg, diags := Check(path, []*syntax.File{f}, imp)
	if imp != nil && pkg != nil {
		imp[path] = pkg
	}
	return pkg, diags, f
}

// dump prints a declaration's tree without positions and without the
// links to its derived type: two bundles are equal when their dumps are.
func dump(x any) string {
	var b strings.Builder
	var visit func(v reflect.Value)
	visit = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				b.WriteString("nil")
				return
			}
			visit(v.Elem())
		case reflect.Struct:
			b.WriteString(v.Type().Name() + "{")
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if !f.IsExported() || f.Type == reflect.TypeOf(syntax.Pos(0)) || f.Name == "Derived" || f.Name == "Origin" || f.Name == "Doc" {
					continue
				}
				b.WriteString(f.Name + ":")
				visit(v.Field(i))
				b.WriteString(" ")
			}
			b.WriteString("}")
		case reflect.Slice:
			b.WriteString("[")
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i))
				b.WriteString(",")
			}
			b.WriteString("]")
		default:
			fmt.Fprint(&b, v.Interface())
		}
	}
	visit(reflect.ValueOf(x))
	return b.String()
}

// bundleOf dumps the type T's spec and where and every declaration
// derived from it.
func bundleOf(f *syntax.File, name string) string {
	var parts []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.TypeDecl:
			if d.Name.Name == name {
				parts = append(parts, dump(d.Spec), dump(d.Where))
			}
		case *syntax.FuncDecl:
			if d.Derived != nil && d.Derived.Name.Name == name {
				parts = append(parts, dump(d))
			}
		case *syntax.LawDecl:
			if d.Derived != nil && d.Derived.Name.Name == name {
				parts = append(parts, dump(d))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func messages(ds []Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code+" "+d.Message)
	}
	return strings.Join(out, "\n")
}

// TestDeriveModel (patterns.md, Derivation, Type plans, Plan functions,
// Budgets 1, Coherence 1): a Refine plan derives exactly the bundle and the
// diagnostics of the written where; a pattern built through a user
// package's plan function, through a wrapper in a second package, and
// inline derives one plan and one matcher; a declaration that hides a
// constructor is never the constructor; evaluation stops at its budgets.
func TestDeriveModel(t *testing.T) {
	r := rand.New(rand.NewSource(1111))
	bases := []string{"int", "int64", "string", "[]int"}
	for trial := 0; trial < 60; trial++ {
		base := bases[r.Intn(len(bases))]
		lo := int64(r.Intn(200) - 50)
		hi := lo + int64(r.Intn(100)-10)
		it := "it"
		if base == "string" || base == "[]int" {
			it = "len(it)"
		}
		plan := fmt.Sprintf("type N derive Refine(Of[%s](), %d, %d)\nfunc main() {}\n", base, lo, hi)
		where := fmt.Sprintf("type N %s where %d <= %s && %s <= %d\nfunc main() {}\n", base, lo, it, it, hi)
		_, d1, f1 := checkDerive(t, nil, "main", plan)
		_, d2, f2 := checkDerive(t, nil, "main", where)
		if messages(d1) != messages(d2) {
			t.Fatalf("seed 1111 trial %d: diagnostics differ\nplan:  %s\nwhere: %s\n%s", trial, messages(d1), messages(d2), plan)
		}
		if a, b := bundleOf(f1, "N"), bundleOf(f2, "N"); a != b {
			t.Fatalf("seed 1111 trial %d: bundles differ\n%s\nplan:\n%s\nwhere:\n%s", trial, plan, a, b)
		}
	}
	for trial := 0; trial < 12; trial++ {
		width := 1 + r.Intn(8)
		imp := deriveImporter{}
		checkDerive(t, imp, "ids", "func Fixed(prefix string, width int) TypePlan {\n\tPattern(\"\\(prefix)-(?<Num>[0-9]{\\(width)})\")\n}\n")
		checkDerive(t, imp, "wrap", "import ids\n\nfunc Ticket(width int) TypePlan {\n\tlet p = \"T\" + \"K\"\n\tif width > 0 {\n\t\tids.Fixed(p, width)\n\t} else {\n\t\tids.Fixed(p, 1)\n\t}\n}\n")
		src := fmt.Sprintf("import ids\nimport wrap\n\ntype A derive ids.Fixed(\"TK\", %d)\ntype B derive wrap.Ticket(%d)\ntype C derive Pattern(\"TK-(?<Num>[0-9]{%d})\")\nfunc main() {}\n", width, width, width)
		pkg, diags, _ := checkDerive(t, imp, "app", src)
		if len(diags) > 0 {
			t.Fatalf("trial %d: %s\n%s", trial, messages(diags), src)
		}
		got := pkg.Derive.Derived
		if len(got) != 3 {
			t.Fatalf("trial %d: %d derives", trial, len(got))
		}
		for _, d := range got[1:] {
			if d.Pattern != got[0].Pattern || d.Parse.Key() != got[0].Parse.Key() || d.Find.Key() != got[0].Find.Key() {
				t.Fatalf("trial %d: %s derives %q, %s derives %q", trial, d.Type, d.Pattern, got[0].Type, got[0].Pattern)
			}
		}
	}
	// A package's own Pattern hides the constructor: its plan is the one used.
	_, diags, f := checkDerive(t, nil, "main", "func Pattern(text string) TypePlan {\n\tRefine(Of[int](), 0, 9)\n}\n\ntype T derive Pattern(\"a\")\nfunc main() {}\n")
	if len(diags) > 0 || !strings.Contains(bundleOf(f, "T"), "BinaryExpr") {
		t.Fatalf("a shadowing Pattern was not used: %s\n%s", messages(diags), bundleOf(f, "T"))
	}
	_, diags, _ = checkDerive(t, nil, "main", "type Pattern {}\n\ntype T derive Pattern(\"a\")\nfunc main() {}\n")
	if !strings.Contains(messages(diags), "not a plan function or a plan constructor") {
		t.Fatalf("a type named Pattern was treated as the constructor: %s", messages(diags))
	}
	// Budgets 1: the step limit and the call depth, at and past each.
	// steps(n) is an integer expression of exactly n evaluation steps, nested
	// about log2(n) deep: a parenthesised sum is two steps over its operands.
	var steps func(n int) string
	steps = func(n int) string {
		switch n {
		case 1:
			return "1"
		case 2:
			return "-1"
		}
		half := (n - 2) / 2
		return "(" + steps(half) + "+" + steps(n-2-half) + ")"
	}
	// The derive's Refine and Of calls and its low bound are three more
	// steps (the low bound, -100000, is a sign and a literal): a bound of n steps makes 4+n.
	for _, row := range []struct {
		src  string
		want string
	}{
		{"type T derive Refine(Of[int](), -100000, " + steps(99996) + ")\nfunc main() {}\n", ""},
		{"type T derive Refine(Of[int](), -100000, " + steps(99997) + ")\nfunc main() {}\n", "stopped at 100000 evaluation steps"},
		{"func R(n int) TypePlan {\n\tif n == 0 {\n\t\tPattern(\"a\")\n\t} else {\n\t\tR(n - 1)\n\t}\n}\n\ntype T derive R(63)\nfunc main() {}\n", ""},
		{"func R(n int) TypePlan {\n\tif n == 0 {\n\t\tPattern(\"a\")\n\t} else {\n\t\tR(n - 1)\n\t}\n}\n\ntype T derive R(64)\nfunc main() {}\n", "call depth of 64 in R"},
		{"type Date derive Pattern(\"a\")\nfunc ParseDate() int { 1 }\nfunc main() {}\n", "ParseDate is already declared in this package: the derive of Date generates it"},
		{"func f() int { 1 }\n\ntype T derive Pattern(\"a\" + \"\\(f())\")\nfunc main() {}\n", "not a plan function"},
		{"type T derive Field(\"X\", Of[int]())\nfunc main() {}\n", "needs a TypePlan"},
	} {
		_, diags, _ := checkDerive(t, nil, "main", row.src)
		got := messages(diags)
		if row.want == "" && got != "" || row.want != "" && !strings.Contains(got, row.want) {
			t.Errorf("%.80s…: got %q, want %q", row.src, got, row.want)
		}
	}
}

// TestPatternDiagnostics (patterns.md, Diagnostics 1 and 2): an error at a
// chosen byte of a generated pattern, spelled as a raw string, an
// interpreted string with escapes, a concatenation and a plan function's
// parameter, is reported at the source byte that spelled it.
func TestPatternDiagnostics(t *testing.T) {
	r := rand.New(rand.NewSource(4242))
	raw := []string{"a", `\.`, "b"}
	interpreted := []string{"a", `\\.`, `\x61`, "b", `\\t`}
	for trial := 0; trial < 120; trial++ {
		form := trial % 4
		tokens := raw
		if form > 0 {
			tokens = interpreted
		}
		var before, after strings.Builder
		for range r.Intn(6) {
			before.WriteString(tokens[r.Intn(len(tokens))])
		}
		for range r.Intn(4) {
			after.WriteString(tokens[r.Intn(len(tokens))])
		}
		var lit string
		switch form {
		case 0:
			lit = "`" + before.String() + "^" + after.String() + "`"
		case 1, 3:
			lit = `"` + before.String() + "^" + after.String() + `"`
		case 2:
			lit = `"` + before.String() + `" + "^` + after.String() + `"`
		}
		head := "type T derive Pattern("
		if form == 3 {
			head = "func P(s string) TypePlan {\n\tPattern(s)\n}\n\ntype T derive P("
		}
		src := head + lit + ")\nfunc main() {}\n"
		want := strings.Index(src, "^")
		_, diags, _ := checkDerive(t, nil, "main", src)
		if len(diags) != 1 || diags[0].Offset != want || !strings.Contains(diags[0].Message, "drop ^") {
			t.Fatalf("seed 4242 trial %d: want one diagnostic at %d\n%s\ngot %v", trial, want, src, diags)
		}
	}
}
