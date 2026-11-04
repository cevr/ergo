package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// nodePrelude declares what the matrix programs share. Its names are
// exported, so none of them is unused.
const nodePrelude = `type A {}
type B {}
type U = A | B
type S { X int }
func G(x int) {}
func Work(x int) int { return x }
func Fail(x int) fails A { return A }
`

// nodeMatrix gives every expression, statement and declaration node kind of
// the syntax package that the decided parser builds a program in which a
// mistake sits inside a node of that kind, and the one diagnostic that
// mistake must produce: its code and the source text of its span. A checker
// that skipped the kind, or passed it to the host unchecked, would lose that
// diagnostic (spec Risk 1; round-1 review T1). A kind the 196a checker does
// not check yet gives S109 at the node, never silence. The programs are
// decided sources (#196).
var nodeMatrix = []struct {
	kind, src, code, span string
}{
	// Expressions.
	{"ArrayType", `func F(x []Nope) {}`, "S104", "Nope"},
	{"AsPattern", `func F(x U) {
	match x {
	case A as a:
		_ = a
		G(nope)
	case B:
	}
}`, "S104", "nope"},
	{"BasicLit", `func F() int8 { return 300 }`, "S108", "300"},
	{"BinaryExpr", `func F() int { return 1 + nope }`, "S104", "nope"},
	{"CallExpr", `func F() { G(nope) }`, "S104", "nope"},
	{"CatchExpr", `func F() {
	Fail(1) catch {
	case A:
		G(nope)
	}
}`, "S104", "nope"},
	{"CompositeLit", `func F() S { return S{nope} }`, "S104", "nope"},
	{"ErrorRow", `func F() fails Nope {}`, "S104", "Nope"},
	{"FuncLit", `func F() { _ = func() { G(nope) } }`, "S104", "nope"},
	{"FuncType", `func F(f func(Nope)) {}`, "S104", "Nope"},
	{"Ident", `func F() int { return nope }`, "S104", "nope"},
	{"IndexExpr", `func F(xs []int) int { return xs[nope] }`, "S104", "nope"},
	{"InterfaceType", `type I { M(Nope) }`, "S104", "Nope"},
	{"InterpolatedString", `func F() string { return "a \(nope) b" }`, "S104", "nope"},
	{"KeyValueExpr", `func F() S { return S{X: nope} }`, "S104", "nope"},
	{"MachineType", `type M machine[Nope, U] { initial: A, step: Step }
func Step(s U, e U) Decision[U, int] { return Ignore }`, "S104", "Nope"},
	{"MapType", `func F(m Map[string, Nope]) {}`, "S104", "Nope"},
	{"ParenExpr", `func F() int { return (nope) }`, "S104", "nope"},
	{"SelectorExpr", `func F(s S) int { return s.Nope }`, "S104", "Nope"},
	{"SliceExpr", `func F(xs []int) []int { return xs[nope:] }`, "S104", "nope"},
	{"StarExpr", `func F(p *Nope) {}`, "S104", "Nope"},
	{"StructType", `type T { X Nope }`, "S104", "Nope"},
	{"TryExpr", `func F() fails A {
	try Fail(nope)
	return A
}`, "S104", "nope"},
	{"TupleExpr", `func F() { _ = (1, nope) }`, "S104", "nope"},
	{"TypeGuard", `func F() {
	match nope {
	case _:
	}
}`, "S104", "nope"},
	{"UnaryExpr", `func F() int { return -nope }`, "S104", "nope"},
	{"VariantSpec", `type V = A | Paid{ID Nope}`, "S104", "Nope"},
	{"ContextLit", `func F() { with context { Nope: S{} } { G(1) } }`, "S104", "Nope"},
	// Statements.
	{"AssertStmt", `test T {
	assert nope
}`, "S104", "nope"},
	{"AssignStmt", `func F() int {
	let mut x = 1
	x = nope
	return x
}`, "S104", "nope"},
	{"BlockStmt", `func F() { if true { G(nope) } }`, "S104", "nope"},
	{"BranchStmt", `func F() { break }`, "S108", "break"},
	{"DeclStmt", `func F() int {
	let x int = nope
	return x
}`, "S104", "nope"},
	{"ExprStmt", `func F() { G(nope) }`, "S104", "nope"},
	{"ForStmt", `func F() {
	let mut i = 0
	for i < nope {
		G(i)
		i += 1
	}
}`, "S104", "nope"},
	{"IfStmt", `func F() { if nope { G(1) } }`, "S104", "nope"},
	{"RangeStmt", `func F() {
	for x in nope {
		G(x)
	}
}`, "S104", "nope"},
	{"ReturnStmt", `func F() int { return nope }`, "S104", "nope"},
	{"SwitchStmt", `func F(x int) {
	match x {
	case 1:
		G(nope)
	case _:
	}
}`, "S104", "nope"},
	{"TypeSwitchStmt", `func F(x U) {
	match x {
	case A:
		G(nope)
	case B:
		G(1)
	}
}`, "S104", "nope"},
	// Declarations.
	{"FuncDecl", `func F(x Nope) {}`, "S104", "Nope"},
	{"ImportDecl", `import nope`, "S104", "nope"},
	{"TestDecl", `test T {
	G(nope)
}`, "S104", "nope"},
	{"LawDecl", `law L(x int) {
	G(nope)
	x == x
}`, "S104", "nope"},
	{"TypeDecl", `type T = Nope`, "S104", "Nope"},
	{"VarDecl", `let v int = nope`, "S104", "nope"},
	{"ContextDecl", `let W = context { Nope: S{} }`, "S104", "Nope"},
}

// notBuilt are the node kinds the decided parser never builds (the
// syntax package's TestDecidedOnly). Channels, select, go, defer, labels,
// ++ and --, the empty statement and variadic parameters left the language
// (#196), and their node types left the syntax package in lower-v1 stage
// 5 (D10), so the set is empty.
var notBuilt = map[string]bool{}

// TestNodeMatrix: each node kind's program contains a node of that kind,
// and checking it yields exactly the expected diagnostic, at the mistake
// inside that node. The matrix covers every node kind ast.go and
// ast_decided.go declare that the decided parser builds.
func TestNodeMatrix(t *testing.T) {
	covered := map[string]bool{}
	for _, m := range nodeMatrix {
		covered[m.kind] = true
		t.Run(m.kind, func(t *testing.T) {
			src := nodePrelude + m.src + "\n"
			if strings.HasPrefix(m.src, "import ") {
				// Imports come before every other declaration.
				src = m.src + "\n" + nodePrelude
			}
			file, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			kinds := map[string]bool{}
			collectNodeKinds(reflect.ValueOf(file), kinds)
			if !kinds[m.kind] {
				t.Fatalf("the program has no %s node", m.kind)
			}
			_, diags := check.CheckSource(src)
			if len(diags) != 1 {
				t.Fatalf("want one %s at %q, got %s", m.code, m.span, describe(diags))
			}
			d := diags[0]
			if got := src[d.Offset : d.Offset+d.Length]; d.Code != m.code || got != m.span {
				t.Fatalf("want %s at %q, got %s at %q", m.code, m.span, describe(diags), got)
			}
		})
	}
	var missing []string
	for _, k := range syntaxNodeKinds(t) {
		if !covered[k] && !notBuilt[k] {
			missing = append(missing, k)
		}
		if covered[k] && notBuilt[k] {
			t.Errorf("%s has a matrix row but is listed as never built", k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("node kinds without a matrix row: %s", strings.Join(missing, ", "))
	}
}

// syntaxNodeKinds lists the expression, statement and declaration node
// types of the syntax package, from ast.go and ast_decided.go.
func syntaxNodeKinds(t *testing.T) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{"ast.go", "ast_decided.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join("..", "syntax", name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			switch fn.Name.Name {
			case "exprNode", "stmtNode", "declNode":
			default:
				continue
			}
			if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if k := star.X.(*ast.Ident).Name; !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		}
	}
	if len(out) < 40 {
		t.Fatalf("found only %d node kinds", len(out))
	}
	return out
}

// collectNodeKinds records the type names of the syntax nodes reachable
// from v.
func collectNodeKinds(v reflect.Value, out map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if v.Elem().Kind() == reflect.Struct {
			name := v.Elem().Type().Name()
			if out[name+"@"+itoa(v.Pointer())] {
				return
			}
			out[name+"@"+itoa(v.Pointer())] = true
			out[name] = true
		}
		collectNodeKinds(v.Elem(), out)
	case reflect.Interface:
		if !v.IsNil() {
			collectNodeKinds(v.Elem(), out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				collectNodeKinds(v.Field(i), out)
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			collectNodeKinds(v.Index(i), out)
		}
	}
}

func itoa(p uintptr) string {
	const digits = "0123456789abcdef"
	if p == 0 {
		return "0"
	}
	var b []byte
	for p > 0 {
		b = append([]byte{digits[p%16]}, b...)
		p /= 16
	}
	return string(b)
}
