package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// exprInventory derives the expression node types of the syntax package
// from its source (as syntaxInventory does for every node type): each type
// with an exprNode method.
func exprInventory(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "syntax")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "exprNode" || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			rt := fd.Recv.List[0].Type
			if s, ok := rt.(*ast.StarExpr); ok {
				rt = s.X
			}
			if id, ok := rt.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
		}
	}
	sort.Strings(out)
	if len(out) < 25 {
		t.Fatalf("found only %d expression node types", len(out))
	}
	return out
}

// A route carries the refused task.Go, an invalid value, to a callee
// through one value-producing form: decls go at the package level, params
// and args are a's parameters and b's arguments, local is a statement of a
// before the call, callee is the callee as written. staged is the
// diagnostic the routing form adds when the form is itself S109-staged
// ("" when it is not).
type route struct {
	name, decls, params, args, local, callee, staged string
}

// invalidRoutes covers each value-producing expression family of the
// syntax package (exprInventory) and each binding that carries a value: a
// local let, a package let, a parameter, a record field, a for binding.
var invalidRoutes = map[string][]route{
	"SelectorExpr": {
		{name: "pkg.F", callee: "task.Go"},
		{name: "a field of a refused type", decls: "type S { run task.Fn }\n", params: "s S", args: "S{}", callee: "s.run"},
	},
	"Ident": {
		{name: "a local let", local: "let launch = task.Go", callee: "launch"},
		{name: "a local let of a local let", local: "let l1 = task.Go; let launch = l1", callee: "launch"},
		{name: "a package let", decls: "let launch = task.Go\n", callee: "launch"},
		{name: "a parameter of a refused type", params: "launch task.Fn", args: "task.Go", callee: "launch"},
	},
	"CallExpr": {
		{name: "a call result of a refused type", decls: "func mk() task.Fn { return task.Go }\n", callee: "mk()"},
		{name: "a generic call", decls: "func id[T](x T) T { return x }\n", callee: "id(task.Go)"},
		{name: "a local bound to a generic call", decls: "func id[T](x T) T { return x }\n", local: "let launch = id(task.Go)", callee: "launch"},
	},
	"IndexExpr": {
		{name: "a slice element", local: "let fs = []task.Fn{task.Go}", callee: "fs[0]"},
	},
	"SliceExpr": {
		{name: "an element of a slice of a slice", local: "let fs = []task.Fn{task.Go}", callee: "fs[0:1][0]"},
	},
	"CompositeLit": {
		{name: "a record literal's field", decls: "type S { run task.Fn }\n", local: "let s = S{run: task.Go}", callee: "s.run"},
	},
	"KeyValueExpr": {
		// A Map read is refused (stage D2 design ruling): the callee is
		// invalid for that reason too, and the callback still checks.
		{name: "a map element", local: `let ms = Map[string, task.Fn]{"go": task.Go}`, callee: `ms["go"]`, staged: "S108"},
	},
	"FuncLit": {
		{name: "a function literal's result", local: "let mk = func() task.Fn { return task.Go }", callee: "mk()"},
	},
	"ParenExpr": {
		{name: "a parenthesised value", callee: "(task.Go)"},
	},
	"TupleExpr": {
		{name: "a tuple binding", local: "let (launch, _) = (task.Go, 1)", callee: "launch"},
	},
	"TryExpr": {
		{name: "a value through try", decls: "func mkf() task.Fn fails Bang { return task.Go }\n", local: "let launch = try mkf()", callee: "launch"},
	},
	"CatchExpr": {
		{name: "a value through catch", decls: "func mkf() task.Fn fails Bang { return task.Go }\n", local: "let launch = mkf() catch {\n\tcase Bang:\n\t\ttask.Go\n\t}", callee: "launch"},
	},
	"IfStmt": {
		{name: "an if value", local: "let launch = if true { task.Go } else { task.Go }", callee: "launch"},
	},
	"SwitchStmt": {
		{name: "a match value", local: "let launch = match 1 {\n\tcase 1:\n\t\ttask.Go\n\tcase _:\n\t\ttask.Go\n\t}", callee: "launch"},
	},
	"TypeSwitchStmt": {
		{name: "a type match value", decls: "type A {}\ntype B {}\ntype U = A | B\n", params: "u U", args: "A", local: "let launch = match u {\n\tcase A:\n\t\ttask.Go\n\tcase B:\n\t\ttask.Go\n\t}", callee: "launch"},
	},
	"UnaryExpr": {
		{name: "a pointer to a local", local: "let l1 = task.Go; let p = &l1", callee: "(*p)"},
	},
	"StarExpr": {
		{name: "a dereferenced pointer", params: "p *task.Fn", args: "task.Go", callee: "(*p)"},
	},
}

// cannotCarry lists the expression families that cannot carry a function
// value to a callee, each with its reason.
var cannotCarry = map[string]string{
	"BasicLit":           "a literal is a number, a rune, a string or a duration",
	"BinaryExpr":         "no binary operator yields a function",
	"InterpolatedString": "an interpolation is a string",
	"TypeGuard":          "a type test is a bool",
	"ContextLit":         "a context literal is no function (withArg reads it)",
	"ArrayType":          "a type, not a value",
	"MapType":            "a type, not a value",
	"FuncType":           "a type, not a value",
	"StructType":         "a type, not a value",
	"InterfaceType":      "a type, not a value",
	"ErrorRow":           "a type, not a value",
	"MachineType":        "a type, not a value",
	"AsPattern":          "an as binding takes its case's type: an error type or a variant, a record (a defined function type is S109, 196p), never a function value",
	"VariantSpec":        "a variant written in place in a union declaration: a type, not a value",
}

// TestInvalidCalleeDomain (round 8 review M2): a callee typing finds
// invalid checks none of the literals and blocks passed to it, wherever the
// invalid value flows. The routes come from the syntax package's expression
// families: each family routes the refused task.Go to a callee, or is
// listed in cannotCarry with its reason, or the test fails. Each route is
// called with a trailing block and with a function literal that call b,
// and with a block that names b as a value; b and a are one recursive
// group, F returns b(). The one diagnostic is the S109 at the import (plus
// the routing form's own S109 where it is staged): b is no class G value
// use of the group, and F does not "return no value". RefWalkProblems
// checks both directions: every callback typing leaves unchecked is dropped
// by the walk, and every callback the walk drops is unchecked in typing.
func TestInvalidCalleeDomain(t *testing.T) {
	for _, name := range exprInventory(t) {
		_, routed := invalidRoutes[name]
		_, listed := cannotCarry[name]
		switch {
		case routed && listed:
			t.Errorf("%s is both routed and listed in cannotCarry", name)
		case !routed && !listed:
			t.Errorf("the expression family %s has no route for an invalid value and no reason in cannotCarry", name)
		}
	}
	callbacks := []struct{ name, cb string }{
		{"trailing block", "@() { b() }"},
		{"function literal", "@(func() { b() })"},
		{"value in a block", "@() { _ = b }"},
	}
	var families []string
	for f := range invalidRoutes {
		families = append(families, f)
	}
	sort.Strings(families)
	for _, family := range families {
		for _, r := range invalidRoutes[family] {
			for _, cb := range callbacks {
				t.Run(family+", "+r.name+", "+cb.name, func(t *testing.T) {
					local := ""
					if r.local != "" {
						local = r.local + "\n\t"
					}
					src := "import go task\n" + r.decls +
						"func a(" + r.params + ") {\n\t" + local + strings.ReplaceAll(cb.cb, "@", r.callee) + "\n\treturn Bang\n}\n" +
						"func b() { return a(" + r.args + ") }\n" +
						"type Bang {}\nfunc F() fails Bang { return b() }\nfunc main() {}\n"
					_, diags := check.CheckSource(src)
					var got []string
					refused := false
					for _, d := range diags {
						if d.Code == "S109" && d.Line == 1 && d.Column == 8 {
							refused = true
							continue
						}
						got = append(got, d.Code)
					}
					if !refused || strings.Join(got, " ") != r.staged {
						t.Errorf("want the S109 at 1:8 and %q; got %v\n%s", r.staged, diags, src)
					}
					if p := check.RefWalkProblems(src); len(p) > 0 {
						t.Errorf("references: %s\n%s", strings.Join(p, "; "), src)
					}
					if p := check.OrderProblems(src, 5); len(p) > 0 {
						t.Errorf("order: %s", strings.Join(p, "; "))
					}
				})
			}
		}
	}
}
