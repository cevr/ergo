package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// C45 (check-v1 E6b): a type defined over a tuple destructures through its
// underlying tuple, as a tuple pattern already matches it (D-B6): a
// several-name let of a value of a defined tuple type binds its elements.
// lower/stage_e6b_test.go runs the same program (TestErgoRunDefinedTuple).
const definedTupleProgram = `type Pair (int, string)

type Triple (int, string, bool)

type Again Pair

type Two[T] (T, T)

type Box[T] { v T }

type Bad { why string }

func pair() Pair {
	Pair((1, "a"))
}

func risky(fail bool) Pair fails Bad {
	if fail {
		return Bad{why: "no"}
	}
	Pair((2, "b"))
}

func swap(p Pair) Pair {
	let n, s = p
	Pair((n + 10, s + "!"))
}

let top, word = pair()

let _, again = Again(Pair((3, "c")))

func main() needs Console {
	let a, b = pair()
	Console.Println("\(a) \(b)")
	let x, _, z = Triple((4, "d", true))
	Console.Println("\(x) \(z)")
	let _, m = Again(swap(pair()))
	Console.Println(m)
	let l, r = Two[string](("l", "r"))
	Console.Println(l + r)
	let i, _ = Box[Pair]{v: pair()}.v
	Console.Println("\(i)")
	let c, d = risky(true) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(c) \(d)")
	let e, f = risky(false) catch { case Bad: Pair((0, "caught")) }
	Console.Println("\(e) \(f)")
	match Triple((5, "e", false)) {
	case (n, s, true):
		Console.Println("\(n) \(s) yes")
	case (n, s, false):
		Console.Println("\(n) \(s) no")
	}
	Console.Println("\(top) \(word) \(again)")
}
`

func TestDefinedTupleLet(t *testing.T) {
	if _, diags := check.CheckSource(definedTupleProgram); len(diags) > 0 {
		t.Fatalf("the defined tuple program: %s", describe(diags))
	}
	// The count of names still has to match the underlying tuple's.
	for _, c := range []struct{ body, want string }{
		{"let a, b, c = pair()", "3 names for 2 values"},
		{"let a, b = Triple((1, \"x\", true))", "2 names for 3 values"},
		{"let a, b = Box[Pair]{v: pair()}", "2 names for 1 values"},
		{"let a, b = 1", "2 names for 1 values"},
	} {
		src := "type Pair (int, string)\ntype Triple (int, string, bool)\ntype Box[T] { v T }\nfunc pair() Pair { Pair((1, \"a\")) }\nfunc f() {\n\t" + c.body + "\n}\n"
		_, diags := check.CheckSource(src)
		if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Message != c.want {
			t.Errorf("%s: want S108 %q, got %s", c.body, c.want, describe(diags))
		}
	}
}

// tupleAssertionExempt names each *Tuple type assertion in check and lower
// whose operand is not the underlying type, with the reason a type defined
// over a tuple cannot reach it or must not pass it.
var tupleAssertionExempt = map[string]string{
	"check/call.go yieldingBlock op.typ":           "a legacy result list of several types: several Go results, which a value of a defined tuple type is not; the decided parser writes one result type (parseSignature)",
	"check/stmt.go returnTuple op.typ":             "the same legacy result list: a return of several values",
	"check/entry.go checkRecursiveStructs written": "the written template read in parallel with an actual *Tuple; a defined written type holds no template, and nil is the fallback",
	"check/unify.go nify y":                        "structural unification: x is a *Tuple here, and a defined type unifies only with a defined type",
	"lower/gocall.go goCall sig.Results[0]":        "a binding's result: Go's several results are an unnamed tuple (gobind), and a Go type defined over one does not exist, so a defined type here is one Go result",
	"check/extern.go resultList s.Results[0]":      "a signature's several results are an unnamed tuple; a defined type over one is one result, as Go's is",
	"lower/gocall.go goExtern written[0]":          "a user extern's several results are an unnamed tuple (resultList in the checker); a defined type over one is one result",
}

// TestTupleAssertionsUnwrap is C45's guard: a type assertion to *Tuple in
// the checker or the lowering reads the underlying type (under, under0,
// underlying or Underlying()), or is exempt with its reason above. Type
// switches walk the type structure and meet a defined type in its own
// case, so they are not assertions here.
func TestTupleAssertionsUnwrap(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, dir := range []string{".", "../lower"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, path, data, 0)
			if err != nil {
				t.Fatal(err)
			}
			pkgDir := "check"
			if dir != "." {
				pkgDir = "lower"
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					ta, ok := n.(*ast.TypeAssertExpr)
					if !ok || ta.Type == nil || !isTupleType(ta.Type) || readsUnderlying(ta.X) {
						return true
					}
					key := pkgDir + "/" + filepath.Base(path) + " " + fd.Name.Name + " " + string(data[fset.Position(ta.X.Pos()).Offset:fset.Position(ta.X.End()).Offset])
					seen[key] = true
					if tupleAssertionExempt[key] == "" {
						t.Errorf("%s: %s asserts *Tuple on a type that may be defined over one; read under(…) or name it in tupleAssertionExempt", fset.Position(ta.Pos()), key)
					}
					return true
				})
			}
		}
	}
	for key := range tupleAssertionExempt {
		if !seen[key] {
			t.Errorf("stale exemption %s", key)
		}
	}
}

func isTupleType(e ast.Expr) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := star.X.(type) {
	case *ast.Ident:
		return x.Name == "Tuple"
	case *ast.SelectorExpr:
		return x.Sel.Name == "Tuple"
	}
	return false
}

// readsUnderlying reports an operand that is the underlying type: a call
// of under, under0 or underlying, or of a method Underlying.
func readsUnderlying(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "under" || fun.Name == "under0" || fun.Name == "underlying"
	case *ast.SelectorExpr:
		return fun.Sel.Name == "Underlying"
	}
	return false
}
