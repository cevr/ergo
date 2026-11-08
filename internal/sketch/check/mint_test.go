package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestMintFormNamesEveryKind is the totality guard of Mints
// (fu-ambient-ctor r6). It reads the syntax package's source: every type
// with an exprNode method is a kind of expression and every type with a
// declNode method a kind of declaration. mintForm's type switch names each
// kind of expression exactly once, minter.decl's names each kind of
// declaration exactly once, and neither has a default case. So a new kind
// of expression, which may build a value, is red here until mintForm
// decides whether it does.
func TestMintFormNamesEveryKind(t *testing.T) {
	fset := token.NewFileSet()
	kinds := map[string][]string{} // marker method -> kinds
	files, err := filepath.Glob(filepath.Join("..", "syntax", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f := parseGo(t, fset, path)
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if name := fn.Name.Name; name == "exprNode" || name == "declNode" {
				kinds[name] = append(kinds[name], recvName(fn))
			}
		}
	}
	if len(kinds["exprNode"]) < 25 || len(kinds["declNode"]) < 5 {
		t.Fatalf("found expression kinds %v and declaration kinds %v: the scan is broken", kinds["exprNode"], kinds["declNode"])
	}
	mint := parseGo(t, fset, "mint.go")
	switches := map[string][]string{}
	for _, d := range mint.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var key string
		switch {
		case fn.Recv == nil && fn.Name.Name == "mintForm":
			key = "exprNode"
		case fn.Recv != nil && recvName(fn) == "minter" && fn.Name.Name == "decl":
			key = "declNode"
		default:
			continue
		}
		n := 0
		ast.Inspect(fn.Body, func(x ast.Node) bool {
			sw, ok := x.(*ast.TypeSwitchStmt)
			if !ok {
				return true
			}
			n++
			for _, s := range sw.Body.List {
				cc := s.(*ast.CaseClause)
				if cc.List == nil {
					t.Errorf("%s's type switch has a default case: every kind is named, so a new one is red", fn.Name.Name)
				}
				for _, e := range cc.List {
					sel, ok := e.(*ast.StarExpr)
					var name string
					if ok {
						if s, ok := sel.X.(*ast.SelectorExpr); ok {
							name = s.Sel.Name
						}
					}
					if name == "" {
						t.Errorf("%s's case at %s is not *syntax.Kind", fn.Name.Name, fset.Position(e.Pos()))
						continue
					}
					switches[key] = append(switches[key], name)
				}
			}
			return false
		})
		if n != 1 {
			t.Errorf("%s has %d type switches, want 1", fn.Name.Name, n)
		}
	}
	for _, key := range []string{"exprNode", "declNode"} {
		want := slices.Sorted(slices.Values(kinds[key]))
		got := slices.Sorted(slices.Values(switches[key]))
		if !slices.Equal(got, want) {
			t.Errorf("the switch over the %s kinds names %v, the kinds are %v: name each kind once", key, got, want)
		}
	}
}

func parseGo(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func recvName(fn *ast.FuncDecl) string {
	rt := fn.Recv.List[0].Type
	if star, ok := rt.(*ast.StarExpr); ok {
		rt = star.X
	}
	if id, ok := rt.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// mintText is a mint as a row of TestMintsEachForm: form, the type made,
// the place and declaration, and the frames (F a declared function, L a
// closure, w when its needs row is written).
func mintText(m Mint) string {
	var frames []string
	for _, f := range m.Frames {
		s := "L"
		if f.Func != nil {
			s = "F"
		}
		if f.Written {
			s += "w"
		}
		frames = append(frames, s)
	}
	return fmt.Sprintf("%s %s @%s %s [%s]", mintFormNames[m.Form], m.Type, mintPlaceNames[m.Place], m.Name, strings.Join(frames, " "))
}

var mintFormNames = map[MintForm]string{
	MintLiteral: "literal", MintUpdate: "update", MintZeroField: "zero-field", MintZeroLet: "zero-let",
	MintZeroRecord: "bare", MintConversion: "conversion", MintConstant: "constant", MintFunc: "func",
	MintTuple: "tuple", MintString: "string", MintGenerated: "generated",
}

var mintPlaceNames = map[MintPlace]string{
	MintInFunc: "func", MintInLet: "let", MintInContext: "context", MintInType: "type", MintInTest: "test", MintInLaw: "law",
}

// TestMintsEachForm gives each form of mint, each place and each frame
// one row that only it decides: a mutant that drops a form, a place, a
// frame or a pattern skip changes its row. Each row's source declares H,
// a record with a hidden field, and the mints of H (and of what holds it)
// are compared; other types' mints are left out.
func TestMintsEachForm(t *testing.T) {
	const base = `type H { root string }

type U {}

type N int

type W { X H, Y int }

type Rd { Get() int }

func Get(_ H) int { 1 }

`
	held := []string{"fixture.H", "fixture.U", "fixture.N", "fixture.W", "fixture.S", "fixture.Flag", "func() int"}
	for _, row := range []struct {
		name, src string
		want      []string
	}{
		{"literal", "func f() H { H{root: \"/\"} }\n", []string{"literal fixture.H @func f [F]"}},
		{"elided literal in a slice", "func f() []H { []H{{root: \"/\"}} }\n", []string{"literal []fixture.H @func f [F]", "literal fixture.H @func f [F]"}},
		{"record update", "func f(h H) H { H{..h, root: \"x\"} }\n", []string{"update fixture.H @func f [F]"}},
		{"omitted field's zero value", "func f() W { W{Y: 1} }\n", []string{"literal fixture.W @func f [F]", "zero-field fixture.H @func f [F]"}},
		{"positional literal sets every field", "func f(h H) W { W{h, 1} }\n", []string{"literal fixture.W @func f [F]"}},
		{"let's zero value", "func f() int {\n\tlet w W\n\tw.Y\n}\n", []string{"zero-let fixture.W @func f [F]", "zero-let fixture.H @func f [F]"}},
		{"package let's zero value", "let Z W\n", []string{"zero-let fixture.W @let Z []", "zero-let fixture.H @let Z []"}},
		{"bare record", "func f() U { U }\n", []string{"bare fixture.U @func f [F]"}},
		{"conversion", "func f(h H) H { H(h) }\n", []string{"conversion fixture.H @func f [F]"}},
		{"constant", "func f() N { 3 }\n", []string{"constant fixture.N @func f [F]"}},
		{"constant expression", "func f() N { -3 }\n", []string{"constant fixture.N @func f [F]"}},
		{"constant pattern is no mint", "func f(n N) int {\n\tmatch n {\n\tcase 3: 1\n\tcase _: 0\n\t}\n}\n", nil},
		{"callee expression", "func pick(h H) func() int { func() int { Get(h) } }\n\nfunc f() int { pick(H{root: \"/\"})() }\n", []string{"func func() int @func pick [F]", "literal fixture.H @func f [F]"}},
		{"constant name", "type Flag bool\n\nfunc f() Flag { true }\n", []string{"constant fixture.Flag @func f [F]"}},
		{"if branch", "func f(b bool) int {\n\tif b { Get(H{root: \"/\"}) } else { 0 }\n}\n", []string{"literal fixture.H @func f [F]"}},
		{"match arm", "func f(w W) int {\n\tmatch w {\n\tcase W{X: _, Y: y}: Get(H{root: \"/\"}) + y\n\t}\n}\n", []string{"literal fixture.H @func f [F]"}},
		{"catch arm", "type Oops {}\n\nfunc g() int fails Oops { 1 }\n\nfunc f() int {\n\tg() catch {\n\tcase Oops:\n\t\tGet(H{root: \"/\"})\n\t}\n}\n", []string{"literal fixture.H @func f [F]"}},
		{"variable is no mint", "func f(u U) U { u }\n", nil},
		{"field read is no mint", "type Z { Q U }\n\nfunc f(z Z) U { z.Q }\n", nil},
		{"call is no mint", "func g() H needs Ambient { H{root: \"/\"} }\n\nfunc f() H needs Ambient { g() }\n", []string{"literal fixture.H @func g [Fw]"}},
		{"generic record with no fields", "type E[T] {}\n\nfunc f() E[H] { E[H] }\n", []string{"bare fixture.E[fixture.H] @func f [F]"}},
		{"tuple", "func f(h H) (int, H) { (1, h) }\n", []string{"tuple (int, fixture.H) @func f [F]"}},
		// An interpolated string is a string; a defined string type takes
		// it through a conversion.
		{"interpolated string", "type S string\n\nfunc f(n int) S { S(\"n \\(n)\") }\n", []string{"conversion fixture.S @func f [F]", "string string @func f [F]"}},
		{"function literal", "func f() func() int { func() int { 1 } }\n", []string{"func func() int @func f [F]"}},
		{"closure", "func f() func() H { func() H { H{root: \"/\"} } }\n", []string{"func func() fixture.H @func f [F]", "literal fixture.H @func f [F L]"}},
		{"closure with a written row", "func f() (func() H needs Ambient) { func() H needs Ambient { H{root: \"/\"} } }\n", []string{"func func() fixture.H needs Ambient @func f [F]", "literal fixture.H @func f [F Lw]"}},
		{"written row", "func f() H needs Ambient { H{root: \"/\"} }\n", []string{"literal fixture.H @func f [Fw]"}},
		{"call's argument", "func f() int { Get(H{root: \"/\"}) }\n", []string{"literal fixture.H @func f [F]"}},
		{"open operation type", "func f() Rd { H{root: \"/\"} }\n", []string{"literal fixture.H @func f [F]"}},
		{"field of another record", "func f() W { W{X: H{root: \"/\"}, Y: 1} }\n", []string{"literal fixture.W @func f [F]", "literal fixture.H @func f [F]"}},
		{"Option", "func f() Option[H] { Some[H]{Value: H{root: \"/\"}} }\n", []string{"literal Some[fixture.H] @func f [F]", "literal fixture.H @func f [F]"}},
		{"trailing block", "func run(f func() int) int { f() }\n\nfunc f() int {\n\trun() {\n\t\tGet(H{root: \"/\"})\n\t}\n}\n", []string{"func func() int @func f [F]", "literal fixture.H @func f [F L]"}},
		{"pattern is no mint", "func f(w W) int {\n\tmatch w {\n\tcase W{X: H{root: r}, Y: y}: y\n\t}\n}\n", nil},
		{"let", "let L = H{root: \"/\"}\n", []string{"literal fixture.H @let L []"}},
		{"named context", "let C = context { Rd: H{root: \"/\"} }\n", []string{"literal fixture.H @context C []"}},
		{"machine's initial state", "type MS = Idle | Busy\n\ntype Idle { h H }\n\ntype Busy {}\n\ntype E = Go | Stop\n\ntype Go {}\n\ntype Stop {}\n\nfunc step(s MS, e E) Decision[MS, int] { Ignore }\n\ntype M machine[MS, E] { initial: Idle{h: H{root: \"/\"}}, step: step }\n", []string{"literal fixture.H @type M []"}},
		{"test body", "test T {\n\tassert Get(H{root: \"/\"}) == 1\n}\n", []string{"literal fixture.H @test T []"}},
		{"test parameter", "test T(w W) {\n\tassert w.Y == w.Y\n}\n", []string{"generated fixture.W @test T []", "generated fixture.H @test T []"}},
		{"law", "law L(n int) { Get(H{root: \"/\"}) == n }\n", []string{"literal fixture.H @law L []"}},
		{"law of an operation type with a parameter", "type Q { Peek() int, law L(w W) { w.Y == w.Y } }\n", []string{"generated fixture.W @type Q []", "generated fixture.H @type Q []"}},
		{"law of an operation type", "type Q { Peek() int, law L() { Get(H{root: \"/\"}) == 1 } }\n", []string{"literal fixture.H @type Q []"}},
	} {
		f, err := syntax.Parse(base + row.src)
		if err != nil {
			t.Errorf("%s: parse: %v", row.name, err)
			continue
		}
		pkg, ds := Check("fixture", []*syntax.File{f}, nil)
		var real []Diagnostic
		for _, d := range ds {
			if d.Code != CodeUnused {
				real = append(real, d)
			}
		}
		if len(real) > 0 {
			t.Errorf("%s: %v", row.name, real)
			continue
		}
		var got []string
		for _, m := range Mints(pkg) {
			// Every frame carries its signature: the guard reads its
			// parameters and its row.
			for _, fr := range m.Frames {
				if fr.Sig == nil {
					t.Errorf("%s: a frame of %s has no signature", row.name, mintText(m))
				}
			}
			if slices.Contains(held, m.Type.String()) || strings.Contains(m.Type.String(), "fixture.H") || m.Form == MintString {
				got = append(got, mintText(m))
			}
		}
		if !slices.Equal(got, row.want) {
			t.Errorf("%s: Mints are\n\t%q\nwant\n\t%q", row.name, got, row.want)
		}
	}
	// A qualified record with no fields, of another package, written bare.
	pkg, ds := CheckSource("import queue\n\nfunc f() queue.Closed { queue.Closed }\n")
	if len(ds) > 0 {
		t.Fatalf("qualified bare record: %v", ds)
	}
	var got []string
	for _, m := range Mints(pkg) {
		got = append(got, mintText(m))
	}
	if want := []string{"bare queue.Closed @func f [F]"}; !slices.Equal(got, want) {
		t.Errorf("qualified bare record: Mints are %q, want %q", got, want)
	}
}
