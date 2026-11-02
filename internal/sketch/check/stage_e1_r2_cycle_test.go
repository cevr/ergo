package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Each component reports once at its lexically first type's name. The
// classification follows the whole cycle, independent of resolution order.
func TestE1CycleDeclarationPermutations(t *testing.T) {
	decls := []string{"type A{A A}", "type r{A0000 t }", "type t r"}
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			var lines []string
			for _, i := range order {
				lines = append(lines, decls[i])
			}
			_, ds := check.CheckSource(strings.Join(lines, "\n") + "\n")
			if len(ds) != 2 {
				t.Fatalf("want two independent cycle diagnostics, got %v", ds)
			}
			for _, d := range ds {
				first := 0
				name := "A"
				if strings.HasPrefix(d.Message, "r ") || strings.HasPrefix(d.Message, "t ") {
					for order[first] != 1 {
						first++
					}
					name = "r"
				} else {
					for order[first] != 0 {
						first++
					}
				}
				want := name + " contains itself by value, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"
				if d.Code != "S108" || d.Message != want || d.Line != first+1 || d.Column != 6 || d.Length != 1 {
					t.Errorf("want %s at %d:6-7, got %+v", want, first+1, d)
				}
			}
			if problems := check.OrderProblems(strings.Join(lines, "\n")+"\n", 8); len(problems) != 0 {
				t.Fatal(problems)
			}
		})
	}
}

func TestE1DefinedTypeCycleClassification(t *testing.T) {
	for _, src := range []string{"type Zebra Alpha\ntype Alpha Zebra\n", "type Alpha Zebra\ntype Zebra Alpha\n"} {
		_, ds := check.CheckSource(src)
		if len(ds) != 1 || ds[0].Code != "S108" || ds[0].Message != "Alpha is defined over itself (types.md, Defined types)" ||
			ds[0].Offset != strings.Index(src, "Alpha Zebra") || ds[0].Length != len("Alpha") {
			t.Fatalf("want one defined-type cycle at Alpha's name, got %v", ds)
		}
	}
	for _, src := range []string{
		"type Alias Rec\ntype Rec { Next []Alias }\nfunc Next(a Alias) []Alias { a.Next }\n",
		"type Rec { Next []Alias }\ntype Alias Rec\nfunc Next(a Alias) []Alias { a.Next }\n",
		"type List []List\ntype Callback func() Callback\n",
	} {
		if _, ds := check.CheckSource(src); len(ds) != 0 {
			t.Fatalf("indirection breaks a containment cycle: %v", ds)
		}
	}
}
