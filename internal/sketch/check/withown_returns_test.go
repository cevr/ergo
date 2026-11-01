package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Cross the statement-bearing expression forms, expression slots and frame
// boundaries. These are grammar productions, not individual bug programs.
// TestWireOwnershipReturnCatalog checks the forms/slots against the syntax
// declarations, so adding a production requires an explicit coverage choice.
type ownReturnForm struct{ name, node, text string }

var ownReturnForms = []ownReturnForm{
	{"catch", "CatchExpr", "risk[TYPE]() catch { case Fault: return VALUE }"},
	{"if", "IfStmt", "if true { return VALUE } else { FALLBACK }"},
	{"value-match", "SwitchStmt", "match 0 { case 0: return VALUE; case _: FALLBACK }"},
	{"type-match", "TypeSwitchStmt", "match choice { case Left: return VALUE; case Right: FALLBACK }"},
}

type ownReturnSlot struct{ field, typ, fallback, text string }

var ownReturnSlots = []ownReturnSlot{
	{"root", "int", "0", "EXPR"},
	{"ParenExpr.X", "int", "0", "(EXPR)"},
	{"CallExpr.Args", "int", "0", "integer(EXPR)"},
	{"CallExpr.Args/conversion", "int", "0", "int(EXPR)"},
	{"CallExpr.Fun", "func(int) int", "integer", "(EXPR)(0)"},
	{"CallExpr.Args/trailing", "int", "0", "runInt(EXPR) {}"},
	{"CallExpr.Fun/trailing", "func(int, func())", "runInt", "(EXPR)(0) {}"},
	{"UnaryExpr.X", "int", "0", "-(EXPR)"},
	{"BinaryExpr.X", "int", "0", "(EXPR) + 1"},
	{"BinaryExpr.Y", "int", "0", "1 + (EXPR)"},
	{"CompositeLit.Elts", "int", "0", "[]int{EXPR}"},
	{"KeyValueExpr.Value", "int", "0", "Box{n: EXPR}"},
	{"KeyValueExpr.Key", "int", "0", "Map[int, int]{EXPR: 0}"},
	{"SelectorExpr.X", "Box", "Box{n: 0}", "(EXPR).n"},
	{"IndexExpr.X", "[]int", "[]int{0}", "(EXPR)[0]"},
	{"IndexExpr.Indices", "int", "0", "[]int{0}[EXPR]"},
	{"SliceExpr.X", "[]int", "[]int{0}", "(EXPR)[:]"},
	{"SliceExpr.Low", "int", "0", "[]int{0}[EXPR:]"},
	{"SliceExpr.High", "int", "1", "[]int{0}[:EXPR]"},
	{"SliceExpr.Max", "int", "1", "[]int{0}[0:1:EXPR]"},
	{"TryExpr.X", "int", "0", "try risky(EXPR)"},
	{"CatchExpr.X", "int", "0", "risky(EXPR) catch { case Fault: 0 }"},
	{"TupleExpr.Elts", "int", "0", "(0, EXPR)"},
	{"IfStmt.Cond", "bool", "true", "if (EXPR) { 0 } else { 1 }"},
	{"TypeGuard.X/value", "bool", "true", "match (EXPR) { case true: 0; case _: 1 }"},
	{"SwitchStmt.Tag", "int", "0", "match (EXPR) { case 0: 0; case _: 1 }"},
	{"TypeGuard.X", "Choice", "choice", "match (EXPR) { case Left: 0; case Right: 1 }"},
	{"InterpolatedString.Exprs", "int", "0", `"value \(EXPR)"`},
}

var ownReturnBoundaries = map[string]string{
	"none":     "STMT",
	"FuncLit":  "let _ = func() Conn fails Fault { STMT; Conn{n: 2} }",
	"CallExpr": "try runConn() { STMT; Conn{n: 2} }",
	"with":     "with Plain { B.Use(); STMT }",
}

const ownReturnPrelude = `type Fault {}
type Conn { n int }
type Box { n int }
type Left {}
type Right {}
type Choice = Left | Right
type B { Use() }
type Silent {}
func Use(_ Silent) {}
let Plain = wire { B: Silent }
let choice Choice = Left
let shared = Conn{n: 1}
let counter = 0
func risk[T]() T fails Fault { return Fault }
func risky(n int) int fails Fault { n }
func integer(n int) int { n }
func runInt(n int, f func()) { f() }
func runConn(f func() Conn fails Fault) fails Fault { let _ = try f() }
`

func TestWireOwnershipReturnPositions(t *testing.T) {
	for _, form := range ownReturnForms {
		for _, slot := range ownReturnSlots {
			for boundary, wrap := range ownReturnBoundaries {
				for _, trailing := range []bool{false, true} {
					for _, borrowed := range []bool{false, true} {
						name := fmt.Sprintf("%s/%s/%s/trailing=%v/borrowed=%v", form.name, slot.field, boundary, trailing, borrowed)
						t.Run(name, func(t *testing.T) {
							value := "Conn{n: 3}"
							if borrowed {
								value = "shared"
							}
							carrier := strings.NewReplacer("TYPE", slot.typ, "FALLBACK", slot.fallback, "VALUE", value).Replace(form.text)
							expr := strings.ReplaceAll(slot.text, "EXPR", "("+carrier+")")
							stmt := "let _ = " + expr
							if strings.Contains(slot.field, "/trailing") {
								stmt = expr
							}
							if trailing {
								stmt = "runInt(0 + (if true { " + stmt + "; 0 } else { 1 })) {}"
							}
							src := ownReturnPrelude + "\nfunc get() Conn fails Fault { " + strings.ReplaceAll(wrap, "STMT", stmt) + "; Conn{n: 2} }\n"
							file, err := syntax.Parse(src)
							if err != nil {
								t.Fatalf("%v\n%s", err, src)
							}
							seenForm := false
							syntax.Inspect(file, func(n syntax.Node) bool {
								seenForm = seenForm || fmt.Sprintf("%T", n) == "*syntax."+form.node
								return true
							})
							if !seenForm {
								t.Fatalf("generated source did not exercise %s", form.node)
							}
							pkg, ds := Check("main", []*syntax.File{file}, nil)
							for _, f := range pkg.Funcs {
								if f.Name != "get" {
									continue
								}
								returns := 0
								inspectFrame(pkg.Info, f.Decl.Body, func(n syntax.Node, _ []*syntax.CallExpr) bool {
									if _, ok := n.(*syntax.ReturnStmt); ok {
										returns++
									}
									return true
								})
								want := 1
								if boundary == "FuncLit" || boundary == "CallExpr" {
									want = 0
								}
								if returns != want {
									t.Fatalf("frame sees %d returns, want %d\n%s", returns, want, src)
								}
							}
							if boundary == "with" || boundary == "CallExpr" {
								if len(ds) == 0 {
									t.Fatal("return crossed a with boundary without S108")
								}
								for _, d := range ds {
									if d.Code != "S108" || !strings.Contains(d.Message, "return cannot leave") {
										t.Fatalf("unexpected diagnostic: %v\n%s", ds, src)
									}
								}
								return
							}
							if len(ds) != 0 {
								t.Fatalf("invalid generated source: %v\n%s", ds, src)
							}
							fresh := providerFreshFunctions(pkg.Funcs)
							want := !borrowed || boundary != "none"
							found := false
							for _, f := range pkg.Funcs {
								if f.Name == "get" {
									found = true
									if fresh[f] != want {
										t.Fatalf("get fresh=%v, want %v\n%s", fresh[f], want, src)
									}
								}
							}
							if !found {
								t.Fatal("missing generated get")
							}
						})
					}
				}
			}
		}
	}
}

func TestWireOwnershipReturnCatalog(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "../syntax/ast.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	structs := map[string]*ast.StructType{}
	exprs := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					if st, ok := ts.Type.(*ast.StructType); ok {
						structs[ts.Name.Name] = st
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name == "exprNode" {
				p := d.Recv.List[0].Type.(*ast.StarExpr)
				exprs[p.X.(*ast.Ident).Name] = true
			}
		}
	}
	// Some expression nodes inherit exprNode through an embedded node.
	for changed := true; changed; {
		changed = false
		for name, st := range structs {
			for _, field := range st.Fields.List {
				if len(field.Names) != 0 {
					continue
				}
				typ := field.Type
				if p, ok := typ.(*ast.StarExpr); ok {
					typ = p.X
				}
				if id, ok := typ.(*ast.Ident); ok && exprs[id.Name] && !exprs[name] {
					exprs[name], changed = true, true
				}
			}
		}
	}
	var statements func(ast.Expr, map[string]bool) bool
	statements = func(typ ast.Expr, seen map[string]bool) bool {
		switch x := typ.(type) {
		case *ast.StarExpr:
			return statements(x.X, seen)
		case *ast.ArrayType:
			return statements(x.Elt, seen)
		case *ast.Ident:
			if x.Name == "Stmt" || x.Name == "BlockStmt" {
				return true
			}
			if seen[x.Name] {
				return false
			}
			seen[x.Name] = true
			if st := structs[x.Name]; st != nil {
				for _, field := range st.Fields.List {
					if statements(field.Type, seen) {
						return true
					}
				}
			}
		}
		return false
	}
	forms := map[string]bool{}
	for _, form := range ownReturnForms {
		forms[form.node] = true
	}
	for name := range exprs {
		st := structs[name]
		if st == nil {
			continue
		}
		for _, field := range st.Fields.List {
			if statements(field.Type, map[string]bool{}) && !forms[name] && ownReturnBoundaries[name] == "" && name != "GoExpr" {
				t.Errorf("statement-bearing %s needs a return form or frame boundary", name)
			}
		}
	}
	// These have Expr children but are patterns or type syntax. They do not
	// evaluate an arbitrary expression; adding an admitted form needs a case.
	typeOnly := map[string]bool{"ArrayType": true, "MapType": true, "ChanType": true, "FuncType": true, "StructType": true, "InterfaceType": true, "ErrorRow": true, "MachineType": true, "Ellipsis": true}
	// Dereference needs a borrow parameter, never an expression-produced
	// borrow. A composite type position is not an evaluated value slot.
	slots := map[string]bool{"CompositeLit.Type": true, "StarExpr.X": true}
	for _, slot := range ownReturnSlots {
		slots[strings.Split(slot.field, "/")[0]] = true
	}
	for name := range exprs {
		if typeOnly[name] || structs[name] == nil {
			continue
		}
		for _, field := range structs[name].Fields.List {
			typ := field.Type
			if a, ok := typ.(*ast.ArrayType); ok {
				typ = a.Elt
			}
			if id, ok := typ.(*ast.Ident); !ok || id.Name != "Expr" {
				continue
			}
			for _, id := range field.Names {
				if key := name + "." + id.Name; !slots[key] {
					t.Errorf("expression slot %s needs a return-position case", key)
				}
			}
		}
	}
}
