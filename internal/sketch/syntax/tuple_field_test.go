package syntax

import "testing"

// TestTupleFieldIsAField is the smallest repro of the L8-A1 parser bug
// (L9): a record field whose type starts with a tuple, `f (int, X)` or
// `g [](int, X)`, parsed as an operation. Beside a field, `f (A, B)` is a
// field of a tuple type, in its place among the fields; with no field
// beside it, it stays an operation of two unnamed parameters.
func TestTupleFieldIsAField(t *testing.T) {
	f, err := Parse("type P {\n\tn int,\n\tf (int, X),\n\tg [](int, X),\n\th [3](int, X),\n\tm string,\n}\n\ntype I { M(int, X) }\n")
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := f.Decls[0].(*TypeDecl).Spec.(*StructType)
	if !ok {
		t.Fatalf("P is %T, want a record", f.Decls[0].(*TypeDecl).Spec)
	}
	var names []string
	for _, fl := range rec.Fields.List {
		names = append(names, fl.Names[0].Name)
	}
	if got := len(names); got != 5 || names[1] != "f" || names[4] != "m" {
		t.Fatalf("fields %v, want [n f g h m]", names)
	}
	if tup, ok := rec.Fields.List[1].Type.(*TupleExpr); !ok || len(tup.Elts) != 2 {
		t.Fatalf("f has type %T, want a tuple of 2", rec.Fields.List[1].Type)
	}
	if _, ok := f.Decls[1].(*TypeDecl).Spec.(*InterfaceType); !ok {
		t.Fatalf("I is %T, want an operation type", f.Decls[1].(*TypeDecl).Spec)
	}
	// One parenthesized type is no tuple: beside a field it mixes kinds.
	if _, err := Parse("type Q {\n\tn int,\n\tf (X),\n}\n"); err == nil {
		t.Fatal("Q parsed; want the mixed-members error")
	}
}
