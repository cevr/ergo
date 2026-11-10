package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// syntaxInventory derives the node universe of the syntax package
// independently of every hand-written list (round 7 review T2): it parses
// each non-test .go file of the package with go/parser and returns every
// struct type that has both a Pos and an End method (syntax.Node), with
// its exported fields that are not positions.
func syntaxInventory(t *testing.T) map[string][]string {
	t.Helper()
	dir := filepath.Join("..", "syntax")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	structs := map[string]*ast.StructType{}
	methods := map[string]map[string]bool{}
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
				if d.Recv == nil || len(d.Recv.List) == 0 {
					continue
				}
				rt := d.Recv.List[0].Type
				if s, ok := rt.(*ast.StarExpr); ok {
					rt = s.X
				}
				if id, ok := rt.(*ast.Ident); ok {
					if methods[id.Name] == nil {
						methods[id.Name] = map[string]bool{}
					}
					methods[id.Name][d.Name.Name] = true
				}
			}
		}
	}
	out := map[string][]string{}
	for name, st := range structs {
		if !methods[name]["Pos"] || !methods[name]["End"] {
			continue
		}
		var fields []string
		for _, f := range st.Fields.List {
			if id, ok := f.Type.(*ast.Ident); ok && id.Name == "Pos" {
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					fields = append(fields, n.Name)
				}
			}
		}
		out[name] = fields
	}
	if len(out) < 60 {
		t.Fatalf("found only %d node types", len(out))
	}
	return out
}

// builtFields records, for each node type in the tree v, that it occurs,
// and each exported field that holds a value in some node of that type
// (not the zero value): "Type" and "Type.Field".
func builtFields(v reflect.Value, out map[string]bool, seen map[uintptr]bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
		builtFields(v.Elem(), out, seen)
	case reflect.Interface:
		if !v.IsNil() {
			builtFields(v.Elem(), out, seen)
		}
	case reflect.Slice:
		for i := range v.Len() {
			builtFields(v.Index(i), out, seen)
		}
	case reflect.Struct:
		ty := v.Type()
		out[ty.Name()] = true
		for i := range v.NumField() {
			f := ty.Field(i)
			if !f.IsExported() {
				continue
			}
			if !v.Field(i).IsZero() {
				out[ty.Name()+"."+f.Name] = true
			}
			builtFields(v.Field(i), out, seen)
		}
	}
}

// A nodeEntry places one node type of the syntax package: the decided
// family whose rows build it, and the stage that rules it when no 196a
// check reads it (the S109 of every row that builds it names the stage).
// A type the decided parser never builds names notBuiltFamily.
type nodeEntry struct {
	family, stage string
}

const notBuiltFamily = "not built"

// nodeTable places every node type syntaxInventory finds. A type it does
// not list fails TestDecidedNodeInventory by name.
var nodeTable = map[string]nodeEntry{
	"ArrayType": {"core forms", ""}, "BasicLit": {"core forms", ""}, "BinaryExpr": {"core forms", ""},
	"BlockStmt": {"core forms", ""}, "CallExpr": {"core forms", ""}, "CompositeLit": {"core forms", ""},
	"DeclStmt": {"core forms", ""}, "ExprStmt": {"core forms", ""}, "Field": {"core forms", ""},
	"FieldList": {"core forms", ""}, "File": {"core forms", ""}, "FuncDecl": {"core forms", ""},
	"FuncType": {"core forms", ""}, "Ident": {"core forms", ""}, "IfStmt": {"core forms", ""},
	"IndexExpr": {"core forms", ""}, "KeyValueExpr": {"core forms", ""}, "ParenExpr": {"core forms", ""},
	"ReturnStmt": {"core forms", ""}, "SelectorExpr": {"core forms", ""}, "SliceExpr": {"core forms", ""},
	"StarExpr": {"core forms", ""}, "UnaryExpr": {"core forms", ""},
	"Comment": {"doc comments", ""}, "CommentGroup": {"doc comments", ""},
	"ImportDecl": {"import path", ""}, "ImportSpec": {"import path", ""},
	"VarDecl": {"let", ""}, "VarSpec": {"let", ""}, "AssignStmt": {"assignment", ""},
	"BranchStmt": {"break and continue", ""}, "ForStmt": {"for condition", ""}, "RangeStmt": {"for in", ""},
	"SwitchStmt": {"value match", ""}, "CaseClause": {"value match", ""},
	"TypeSwitchStmt": {"type match", ""}, "TypeGuard": {"type match", ""}, "AsPattern": {"as pattern", ""},
	"CatchExpr": {"catch arm", ""}, "CatchArm": {"catch arm", ""}, "TryExpr": {"fails and needs", ""},
	"ErrorRow": {"fails and needs", ""}, "NeedsClause": {"fails and needs", ""}, "TakesClause": {"fails and needs", ""},
	"TypeDecl": {"record type", ""}, "StructType": {"record type", ""}, "InterfaceType": {"operation type", ""},
	"MapType": {"Map type", ""}, "FuncLit": {"function literal", ""}, "ExternSpec": {"extern", ""},
	"InterpolatedString": {"interpolation", ""},
	"MachineType":        {"machine", ""}, "MachineEntry": {"machine", ""},
	"ContextDecl": {"context declaration", ""}, "ContextBody": {"context declaration", ""}, "ContextEntry": {"context entries", ""},
	"ContextLit": {"context literal", ""}, "ProvidesClause": {"context header", ""},
	// A tuple is a value and a type (196t); an inline variant declares a
	// record in the package scope (196 fifth round).
	"TupleExpr":   {"tuple value", ""},
	"VariantSpec": {"variant in place", ""},
	"LawDecl":     {"law", ""}, "LawReach": {"machine", ""}, "TestDecl": {"test", ""}, "AssertStmt": {"assert", ""},
	// The forms that left the language (#196) left the syntax package in
	// lower-v1 stage 5 (D10), so no type names notBuiltFamily today.
}

// notBuiltFields are the fields of built node types that the decided
// parser never sets, each with the rule that keeps it empty. Stage 5
// (D10) deleted every such field, so the map is empty; a field added
// later that no row sets fails TestDecidedNodeInventory by name.
var notBuiltFields = map[string]string{
	"File.Name": "set by a caller that loads files from a directory, not by the parser; check.Check orders a package's files by it (pkg-lower round 9)",
}

// TestDecidedNodeInventory (round 7 review T2): the decided families and
// their rows cover every node type and field of the syntax package, where
// the universe comes from the package's source (syntaxInventory), not from
// a list. Every node type is placed in nodeTable, and some row of its
// family builds it; a staged type gets an S109 naming its stage in every
// row that builds it; every field of a built type is set by some row, or
// is listed in notBuiltFields and set by none; a type the decided parser
// never builds is built by no row.
func TestDecidedNodeInventory(t *testing.T) {
	inv := syntaxInventory(t)
	families := map[string]bool{notBuiltFamily: true}
	for _, f := range decidedFamilies {
		families[f] = true
	}
	built := map[string]bool{}
	byFamily := map[string]map[string]bool{}
	for _, tc := range decidedCases {
		file, err := syntax.Parse(tc.src)
		if err != nil {
			t.Errorf("row %q does not parse: %v", tc.name, err)
			continue
		}
		b := map[string]bool{}
		builtFields(reflect.ValueOf(file), b, map[uintptr]bool{})
		if byFamily[tc.family] == nil {
			byFamily[tc.family] = map[string]bool{}
		}
		for k := range b {
			built[k] = true
			byFamily[tc.family][k] = true
		}
		_, diags := check.CheckSource(tc.src)
		for k := range b {
			e, ok := nodeTable[k]
			if !ok || e.stage == "" {
				continue
			}
			staged := false
			for _, d := range diags {
				if d.Code == "S109" && strings.Contains(d.Message, e.stage) {
					staged = true
				}
			}
			if !staged {
				t.Errorf("row %q builds %s, ruled by %s, with no S109 naming it: %v", tc.name, k, e.stage, diags)
			}
		}
	}
	var unplaced []string
	for name, fields := range inv {
		e, ok := nodeTable[name]
		if !ok {
			unplaced = append(unplaced, name)
			continue
		}
		if !families[e.family] {
			t.Errorf("%s names the family %q, which decidedFamilies does not list", name, e.family)
			continue
		}
		if e.family == notBuiltFamily {
			if built[name] {
				t.Errorf("%s is listed as never built, and a row builds it", name)
			}
			continue
		}
		if !byFamily[e.family][name] {
			t.Errorf("no row of the family %q builds %s", e.family, name)
		}
		for _, f := range fields {
			key := name + "." + f
			reason, exempt := notBuiltFields[key]
			switch {
			case exempt && built[key]:
				t.Errorf("%s is listed as never set (%s), and a row sets it", key, reason)
			case !exempt && !built[key]:
				t.Errorf("no row sets %s", key)
			}
		}
	}
	sort.Strings(unplaced)
	if len(unplaced) > 0 {
		t.Errorf("node types nodeTable does not place: %s", strings.Join(unplaced, ", "))
	}
	for name := range nodeTable {
		if _, ok := inv[name]; !ok {
			t.Errorf("nodeTable places %s, which the syntax package does not declare", name)
		}
	}
	for key := range notBuiltFields {
		name, field, _ := strings.Cut(key, ".")
		found := false
		for _, f := range inv[name] {
			found = found || f == field
		}
		if !found {
			t.Errorf("notBuiltFields lists %s, which the syntax package does not declare", key)
		}
	}
}
