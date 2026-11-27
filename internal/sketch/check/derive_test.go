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

func bundleOf(pkg *Package, name string) string {
	if pkg == nil {
		return ""
	}
	var parts []string
	parseName := "Parse" + name
	findName := "Find" + name
	for _, f := range pkg.Files() {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.TypeDecl:
				if d.Name != nil && d.Name.Name == name && d.Derive == nil {
					parts = append(parts, dump(d.Spec))
				}
			case *syntax.FuncDecl:
				if d.Recv == nil && d.Name != nil && (d.Name.Name == parseName || d.Name.Name == findName) {
					parts = append(parts, dump(d))
				}
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

// TestDeriveModel holds the content-mapper inline derive contract: pure plans
// built through user packages, wrappers, and inline regex.Pattern derive the
// same typed declarations; local declarations shadow imported helpers; budgets,
// purity, name collisions, and cycles are enforced cleanly.
func TestDeriveModel(t *testing.T) {
	r := rand.New(rand.NewSource(1111))

	// 1. User wrapper packages vs inline regex.Pattern produce identical declarations.
	for range 8 {
		width := 1 + r.Intn(8)
		imp := deriveImporter{}
		pkgIds, d1, _ := checkDerive(t, imp, "ids", "import content\nimport regex\nimport strconv\n\nfunc Fixed(prefix string, width int) content.Plan {\n\tregex.Pattern(\"\\(prefix)-(?<Num>[0-9]{\\(strconv.Itoa(width))})\")\n}\n")
		if len(d1) > 0 || pkgIds == nil {
			t.Fatalf("ids check failed: %s", messages(d1))
		}
		pkgWrap, d2, _ := checkDerive(t, imp, "wrap", "import content\nimport ids\n\nfunc Ticket(width int) content.Plan {\n\tlet p = \"T\" + \"K\"\n\tif width > 0 {\n\t\tids.Fixed(p, width)\n\t} else {\n\t\tids.Fixed(p, 1)\n\t}\n}\n")
		if len(d2) > 0 || pkgWrap == nil {
			t.Fatalf("wrap check failed: %s", messages(d2))
		}
		src := fmt.Sprintf("import ids\nimport regex\nimport wrap\n\ntype A derive ids.Fixed(\"TK\", %d)\ntype B derive wrap.Ticket(%d)\ntype C derive regex.Pattern(\"TK-(?<Num>[0-9]{%d})\")\nfunc main() {}\n", width, width, width)
		pkg, ds, _ := checkDerive(t, imp, "main", src)
		if len(ds) > 0 {
			t.Fatalf("width %d: %s", width, messages(ds))
		}
		got := pkg.Derive.Records
		if len(got) != 3 {
			t.Fatalf("width %d: got %d mapper records, want 3", width, len(got))
		}
		norm := func(name string) string {
			s := bundleOf(pkg, name)
			for _, pair := range [][2]string{
				{"Parse" + name, "ParseX"},
				{"Find" + name, "FindX"},
				{"_rxExec_" + name, "_rxExec_X"},
				{"_rxBuild_" + name + "_" + name, "_rxBuild_X_X"},
				{"_rxProject_" + name, "_rxProject_X"},
				{`"` + name + `"`, `"X"`},
				{"Ident{Name:" + name + " ", "Ident{Name:X "},
			} {
				s = strings.ReplaceAll(s, pair[0], pair[1])
			}
			return s
		}
		ba, bb, bc := norm("A"), norm("B"), norm("C")
		if ba == "" || ba != bb || ba != bc {
			t.Fatalf("width %d: derived bundles differ:\nA:\n%s\nB:\n%s\nC:\n%s", width, ba, bb, bc)
		}
	}

	// 2. Shadowing: local declarations take precedence cleanly.
	_, diags, pkg := func() (*Package, []Diagnostic, *Package) {
		p, d, _ := checkDerive(t, nil, "main", "import content\nimport regex\n\nfunc Pattern(text string) content.Plan {\n\tregex.Pattern(\"(?<Local>[a-z]+)\")\n}\n\ntype T derive Pattern(\"a\")\nfunc main() fails NoMatch { let _ = (try ParseT(\"abc\")).Local }\n")
		return p, d, p
	}()
	if len(diags) > 0 || pkg == nil {
		t.Fatalf("local shadowing derive failed: %s", messages(diags))
	}

	// 3. Budgets, purity, collisions, and wrong-type derive expressions.
	loopMapper := `import content
import regex

func spin() content.Plan {
	let mut n = 0
	for i in 0..2000000 {
		n = n + i
	}
	regex.Pattern("a")
}

type T derive spin()
func main() {}
`
	deepMapper := `import content
import regex

func deep(n int) content.Plan {
	if n == 0 {
		regex.Pattern("a")
	} else {
		deep(n - 1)
	}
}

type T derive deep(130)
func main() {}
`
	impureMapper := `import content
import regex

func dirty(input content.ContentInput) content.MappedErgo {
	Console.Println("no io")
	regex.MapPattern(input)
}

type T derive content.Plan{Mapper: dirty, MediaKind: "regex", Bytes: "a".Bytes(), Companions: []content.CompanionAsset{}}
func main() {}
`
	badReturn := `type T derive 123
func main() {}
`
	collision := `import regex

type Date derive regex.Pattern("a")
func ParseDate() int { 1 }
func main() {}
`
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"step budget", loopMapper, "step budget"},
		{"call depth budget", deepMapper, "depth"},
		{"impure capability", impureMapper, "Console"},
		{"wrong derive type", badReturn, "expected a content Plan"},
		{"name collision", collision, "ParseDate is already declared in this package: the derive of Date generates it"},
	} {
		_, ds, _ := checkDerive(t, nil, "main", tc.src)
		if len(ds) == 0 || !strings.Contains(messages(ds), tc.want) {
			t.Errorf("%s: got %q, want substring %q", tc.name, messages(ds), tc.want)
		}
	}
}

// TestPatternDiagnostics checks exact literal span attribution for regex
// diagnostics (including escaped/concatenated literals) and One mistake, one
// diagnostic (zero cascade errors on callers of the refused derived type).
func TestPatternDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expr     string
		wantCol  int
		wantText string
	}{
		{"anchor caret", "`a^b`", 34, "drop ^"},
		{"anchor dollar", "`ab$`", 35, "drop $"},
		{"python group", "`(?P<X>a)`", 33, "(?<Name>"},
		{"noncap group", "`(?:a)`", 33, "(?:"},
		{"concat caret", "\"a\" + \"^b\"", 39, "drop ^"},
		{"escape caret", `"a\x5eb"`, 34, "drop ^"},
		{"dup capture", "`(?<X>a)(?<X>b)`", 40, "duplicate capture"},
	} {
		src := fmt.Sprintf("import regex\n\ntype Date derive regex.Pattern(%s)\nfunc use(x Date) string fails NoMatch { try ParseDate(\"a\"); let _ = FindDate(\"a\", 0); \"ok\" }\n", tc.expr)
		_, ds, _ := checkDerive(t, nil, "main", src)
		if len(ds) != 1 {
			t.Fatalf("%s: expected 1 diagnostic (One mistake, one diagnostic), got %d:\n%s", tc.name, len(ds), messages(ds))
		}
		d := ds[0]
		if !strings.Contains(d.Message, tc.wantText) {
			t.Errorf("%s: message %q lacks %q", tc.name, d.Message, tc.wantText)
		}
		if d.Line != 3 || d.Column != tc.wantCol {
			t.Errorf("%s: got %d:%d, want 3:%d (line=%q)", tc.name, d.Line, d.Column, tc.wantCol, strings.Split(src, "\n")[2])
		}
	}
}
