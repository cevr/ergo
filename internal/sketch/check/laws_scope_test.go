package check_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The law scope rule (laws.go rule 3; D-D2): the operations of a law's
// type enclose its body, so no binder of the law may take an operation's
// name. The domain is every syntax field a name can stand in: every field
// of type Expr, []Expr, *Ident or []*Ident of every node type, read from
// the syntax package's source (TestLawScopeGuard). Each field either binds
// a name, and lawBinders has the programs that put a binder there inside a
// law, or binds none, and lawNoBinder gives the reason. A field of a node
// type or a field the decided parser never builds (node_inventory_test.go)
// is outside the domain by that table's reason.

// lawScopeDecls are the declarations every binder program shares: the
// operation type whose law holds the binder, a union to match on, and a
// fallible function to catch.
const lawScopeDecls = `type Bang {}

func risky(n int) int fails Bang {
	if n < 0 {
		return Bang
	}
	n
}

type Dot {}
type Line { N int }
type Shape = Dot | Line

`

// lawBinder is one law that binds $B. Named Get, an operation of the law's
// type, the binder is exactly one S108 at its line; named got, the program
// checks clean, so the S108 is the name's.
type lawBinder struct {
	name, law string
}

// lawLowerOnly are the binder programs where only a lower-case name binds,
// so only the get operation meets the binder: a catch arm's capitalized
// lone name is a type test (rows.md Catch 7, catchAll).
var lawLowerOnly = map[string]bool{"a catch arm's rest": true}

const lawOwner = "type C {\n\tGet() int,\n\tget() int,\n\n\t%s,\n}\n"

// lawBinders are the binder fields, by the syntax field that holds the
// binder, with the laws that place one there.
var lawBinders = map[string][]lawBinder{
	"Field.Names": {
		{"a law's parameter", "law L($B int) { $B == 1 }"},
		{"a function literal's parameter", "law L(v int) {\n\t\tlet g = func($B int) bool { $B == v }\n\t\tg(v)\n\t}"},
	},
	"VarSpec.Names": {
		{"a let", "law L(v int) {\n\t\tlet $B = v\n\t\t$B == v\n\t}"},
		{"a let in a function literal", "law L(v int) {\n\t\tlet g = func() bool {\n\t\t\tlet $B = v\n\t\t\t$B == v\n\t\t}\n\t\tg()\n\t}"},
	},
	"VarSpec.Pattern": {
		{"a let's record pattern", "law L(v int) {\n\t\tlet Line{N: $B} = Line{N: v}\n\t\t$B == v\n\t}"},
	},
	"TupleExpr.Elts": {
		{"a let's tuple pattern", "law L(v int) {\n\t\tlet ($B, w) = (v, v)\n\t\t$B == w\n\t}"},
	},
	"ParenExpr.X": {
		{"a parenthesized pattern", "law L(s Shape) {\n\t\tmatch s {\n\t\tcase (Line{N: $B}):\n\t\t\t$B == 0\n\t\tcase Dot:\n\t\t\ttrue\n\t\t}\n\t}"},
	},
	"CompositeLit.Elts": {
		{"a record pattern's positional field", "law L(s Shape) {\n\t\tmatch s {\n\t\tcase Line{$B}:\n\t\t\t$B == 0\n\t\tcase Dot:\n\t\t\ttrue\n\t\t}\n\t}"},
	},
	"KeyValueExpr.Value": {
		{"a match's record pattern", "law L(s Shape) {\n\t\tmatch s {\n\t\tcase Line{N: $B}:\n\t\t\t$B == 0\n\t\tcase Dot:\n\t\t\ttrue\n\t\t}\n\t}"},
	},
	"RangeStmt.Key": {
		{"a for's name", "law L(xs []int) {\n\t\tfor $B in xs {\n\t\t\tlet _ = $B\n\t\t}\n\t\ttrue\n\t}"},
	},
	"RangeStmt.Value": {
		{"a for's second name", "law L(xs []int) {\n\t\tfor i, $B in xs {\n\t\t\tlet _ = i + $B\n\t\t}\n\t\ttrue\n\t}"},
	},
	"CaseClause.List": {
		{"a match's name", "law L(v int) {\n\t\tmatch v {\n\t\tcase $B:\n\t\t\t$B == v\n\t\t}\n\t}"},
	},
	"AsPattern.Name": {
		{"a match's as", "law L(s Shape) {\n\t\tmatch s {\n\t\tcase Line as $B:\n\t\t\t$B.N == 0\n\t\tcase Dot:\n\t\t\ttrue\n\t\t}\n\t}"},
	},
	"AsPattern.X": {
		{"a pattern under as", "law L(s Shape) {\n\t\tmatch s {\n\t\tcase Line{N: $B} as l:\n\t\t\t$B == l.N\n\t\tcase Dot:\n\t\t\ttrue\n\t\t}\n\t}"},
	},
	"CatchArm.Types": {
		{"a catch arm's rest", "law L(v int) {\n\t\tlet n = risky(v) catch {\n\t\tcase $B:\n\t\t\tlet _ = $B\n\t\t\t0\n\t\t}\n\t\tn == v\n\t}"},
		{"a catch arm's as", "law L(v int) {\n\t\tlet n = risky(v) catch {\n\t\tcase Bang as $B:\n\t\t\tlet _ = $B\n\t\t\t0\n\t\t}\n\t\tn == v\n\t}"},
	},
}

// The reasons a field binds no name in a law body.
const (
	lawUse      = "an operand: a name there is a use, never a binder"
	lawType     = "a type position: a name there names a type"
	lawPackage  = "a package-level declaration's: no law body holds it"
	lawMember   = "names a member of a value, a type or a package, in no scope"
	lawNotParse = "does not parse (node_inventory_test.go)"
)

// lawNoBinder are the fields that bind no name in a law body, each with its
// reason.
var lawNoBinder = map[string]string{
	"AssertStmt.X": lawUse, "AssignStmt.Rhs": lawUse, "BinaryExpr.X": lawUse, "BinaryExpr.Y": lawUse,
	"CallExpr.Args": lawUse, "CallExpr.Fun": lawUse, "CatchExpr.X": lawUse, "CompositeLit.Base": lawUse, "ContextEntry.Value": lawUse,
	"ExprStmt.X": lawUse, "ForStmt.Cond": lawUse, "IfStmt.Cond": lawUse, "IndexExpr.Indices": lawUse,
	"IndexExpr.X": lawUse, "InterpolatedString.Exprs": lawUse, "RangeStmt.X": lawUse,
	"ReturnStmt.Results": lawUse, "SelectorExpr.X": lawUse, "SliceExpr.High": lawUse, "SliceExpr.Low": lawUse,
	"SliceExpr.X": lawUse, "StarExpr.X": lawUse, "TryExpr.X": lawUse, "TypeGuard.X": lawUse,
	"UnaryExpr.X": lawUse, "VarSpec.Values": lawUse,
	"AssignStmt.Lhs": "an assignment writes a let mut already bound; Ergo has no :=",
	"ArrayType.Elt":  lawType, "CompositeLit.Type": lawType, "ErrorRow.Types": lawType, "Field.Type": lawType,
	"InterfaceType.Union": lawType, "MapType.Key": lawType, "MapType.Value": lawType, "NeedsClause.Names": lawType,
	"ProvidesClause.Names": lawType, "MachineType.Signal": lawType, "MachineType.State": lawType,
	"TypeDecl.Spec": lawType, "VarSpec.Type": lawType, "ContextEntry.Key": lawType, "ContextEntry.Keys": lawType,
	"ContextDecl.Name": lawPackage, "FuncDecl.Name": lawPackage, "ImportSpec.Name": lawPackage,
	"TypeDecl.Name": lawPackage, "TestDecl.Name": lawPackage, "VariantSpec.Name": lawPackage,
	"MachineEntry.Value": lawPackage, "MachineType.Initial": lawPackage, "MachineType.Step": lawPackage, "MachineType.After": lawPackage,
	"LawDecl.Name":     "a law's name is in no scope (laws.go rule 1); one named like an operation is TestLawRules'",
	"KeyValueExpr.Key": "a record pattern's or literal's key " + lawMember,
	"SelectorExpr.Sel": "a selector " + lawMember,
	"SliceExpr.Max":    "a three-index slice " + lawNotParse,
	"SwitchStmt.Tag":   "never set: a match on a value is a TypeSwitchStmt whose subject is a TypeGuard (escaping_e2_test.go)",
}

// lawScopeDomain reads the fields a name can stand in: every field of type
// Expr, []Expr, *Ident or []*Ident of every struct of ../syntax/ast*.go,
// less the node types and fields the decided parser never builds.
func lawScopeDomain(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "syntax", "ast*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no syntax sources: %v", err)
	}
	var fields []string
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || nodeTable[ts.Name.Name].family == notBuiltFamily {
				return false
			}
			for _, fl := range st.Fields.List {
				typ := fl.Type
				if a, ok := typ.(*ast.ArrayType); ok {
					typ = a.Elt
				}
				if s, ok := typ.(*ast.StarExpr); ok {
					typ = s.X
				}
				id, ok := typ.(*ast.Ident)
				if !ok || id.Name != "Expr" && id.Name != "Ident" {
					continue
				}
				for _, name := range fl.Names {
					key := ts.Name.Name + "." + name.Name
					if notBuiltFields[key] == "" {
						fields = append(fields, key)
					}
				}
			}
			return false
		})
	}
	sort.Strings(fields)
	return fields
}

// TestLawScopeGuard is the structural guard: every field of the domain has
// binder programs or a reason it binds nothing, never both, and every row
// names a field of the domain; each binder program is one S108 at the
// binder when the binder is named like an operation, and clean otherwise.
func TestLawScopeGuard(t *testing.T) {
	fields := lawScopeDomain(t)
	if len(fields) < 40 {
		t.Fatalf("found %d fields; the guard reads the syntax package", len(fields))
	}
	inDomain := map[string]bool{}
	for _, f := range fields {
		inDomain[f] = true
		_, binds := lawBinders[f]
		reason := lawNoBinder[f]
		switch {
		case !binds && reason == "":
			t.Errorf("the field %s has no law binder program and no reason it binds nothing", f)
		case binds && reason != "":
			t.Errorf("the field %s has binder programs and a reason it binds nothing", f)
		}
	}
	for f := range lawBinders {
		if !inDomain[f] {
			t.Errorf("lawBinders names %s, no field of the domain", f)
		}
	}
	for f := range lawNoBinder {
		if !inDomain[f] {
			t.Errorf("lawNoBinder names %s, no field of the domain", f)
		}
	}
	named := map[string]bool{}
	for _, cases := range lawBinders {
		for _, bc := range cases {
			named[bc.name] = true
		}
	}
	for name := range lawLowerOnly {
		if !named[name] {
			t.Errorf("lawLowerOnly names %q, no binder program", name)
		}
	}
	programs := 0
	for field, cases := range lawBinders {
		if len(cases) == 0 {
			t.Errorf("the binder field %s has no program", field)
		}
		for _, bc := range cases {
			programs++
			name := field + ", " + bc.name
			law := fmt.Sprintf(lawOwner, bc.law)
			// Named got, the program is clean.
			clean := lawScopeDecls + strings.ReplaceAll(law, "$B", "got")
			if _, diags := check.CheckSource(clean); len(diags) > 0 {
				t.Errorf("%s: named got, want a clean check, got\n\t%s\n%s", name, describe(diags), clean)
				continue
			}
			// Named like an operation, the binder's first appearance is
			// the one S108.
			for _, op := range []string{"Get", "get"} {
				if op == "Get" && lawLowerOnly[bc.name] {
					continue
				}
				src := lawScopeDecls + strings.ReplaceAll(law, "$B", op)
				at := strings.Index(lawScopeDecls+law, "$B")
				want := []string{fmt.Sprintf("S108@%d", strings.Count(src[:at], "\n")+1)}
				_, diags := check.CheckSource(src)
				if strings.Join(got(diags), " ") != strings.Join(want, " ") {
					t.Errorf("%s, named %s: want %v, got %v\n\t%s\n%s", name, op, want, got(diags), describe(diags), src)
					continue
				}
				if !strings.Contains(diags[0].Message, "no shadowing") {
					t.Errorf("%s, named %s: the S108 is not the binder rule's: %s", name, op, diags[0])
				}
			}
		}
	}
	t.Logf("%d fields: %d bind (%d programs), %d bind nothing", len(fields), len(lawBinders), programs, len(lawNoBinder))
}
