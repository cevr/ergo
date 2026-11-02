package check_test

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// R2 groups mutually dependent intrinsic declarations and orphan authors.
// Instances contribute edges, never an innocent generic origin.
func TestE1CycleDeclarationPermutations(t *testing.T) {
	for _, tc := range []struct {
		name           string
		decls          []string
		value, defined []string
	}{
		{"original", []string{"type A{A A}", "type r{A0000 t }", "type t r"}, []string{"A", "r,t"}, nil},
		{"three-record", []string{"type Ca { X Cb }", "type Cb { Y Cc }", "type Cc { Z Ca }"}, []string{"Ca,Cb,Cc"}, nil},
		{"three-defined", []string{"type Da Db", "type Db Dc", "type Dc Da"}, nil, []string{"Da,Db,Dc"}},
		{"four-record", []string{"type Ra { X Rb }", "type Rb { Y Rc }", "type Rc { Z Rd }", "type Rd { W Ra }"}, []string{"Ra,Rb,Rc,Rd"}, nil},
		{"four-mixed", []string{"type Pm Qm", "type Qm { F Rm }", "type Rm Sm", "type Sm { G Pm }"}, []string{"Pm,Qm,Rm,Sm"}, nil},
		{"four-mixed-tuple", []string{"type Pt Qt", "type Qt (Rt, int)", "type Rt St", "type St { G Pt }"}, []string{"Pt,Qt,Rt,St"}, nil},
		{"prefix-two", []string{"type Ab { X Ab }", "type A { Y A }", "type A0 { Z A0 }", "type Abc Abc"}, []string{"A", "A0", "Ab"}, []string{"Abc"}},
		{"prefix-joined", []string{"type Ab { X A }", "type A { Y Ab }", "type Abc { Z Abc }"}, []string{"A,Ab", "Abc"}, nil},
		{"case", []string{"type apple { X Banana }", "type Banana { Y apple }"}, []string{"Banana,apple"}, nil},
		{"unicode", []string{"type Äpfel { X Zebra }", "type Zebra { Y Äpfel }", "type Éclair { Z Éclair }"}, []string{"Zebra,Äpfel", "Éclair"}, nil},
		{"generic-shared", []string{"type Gx[T] { V T }", "type X1 { B Gx[X1] }", "type Y1 { B Gx[Y1] }"}, []string{"X1", "Y1"}, nil},
		{"generic-zed", []string{"type Zed[T] { V T }", "type X2 { B Zed[X2] }", "type Y2 { B Zed[Y2] }"}, []string{"X2", "Y2"}, nil},
		{"generic-two-parameters", []string{"type Gx[T, U] { A T, B U }", "type X3 { B Gx[X3, Y3] }", "type Y3 { B Gx[Y3, X3] }"}, []string{"X3,Y3"}, nil},
		{"generic-finite-control", []string{"type Gx[T] { V T }", "type Finite { B Gx[int] }", "type X1 { B Gx[X1] }", "type Y1 { B Gx[Y1] }"}, []string{"X1", "Y1"}, nil},
		{"mixed-two-comp", []string{"type M1 M2", "type M2 M1", "type N1 { X N2 }", "type N2 N1"}, []string{"N1,N2"}, []string{"M1,M2"}},
		{"intrinsic-generic", []string{"type Loop[T] { Next Loop[T] }"}, []string{"Loop"}, nil},
		{"intrinsic-through-finite-wrapper", []string{"type A[T] { V T }", "type Z[T] { V A[Z[T]] }"}, []string{"Z"}, nil},
		{"intrinsic-through-two-wrappers", []string{"type A[T] { V B[T] }", "type B[T] { V T }", "type Z[T] { V A[Z[T]] }"}, []string{"Z"}, nil},
		{"intrinsic-mutual-generics", []string{"type G[T] { Next H[T] }", "type H[T] { Next G[T] }"}, []string{"G,H"}, nil},
		{"intrinsic-swapped-arguments", []string{"type Pair[T, U] { Next Pair[U, T] }"}, []string{"Pair"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := make([]int, len(tc.decls))
			for i := range order {
				order[i] = i
			}
			var visit func(int)
			permutations := 0
			visit = func(n int) {
				if n < len(order) {
					for i := n; i < len(order); i++ {
						order[n], order[i] = order[i], order[n]
						visit(n + 1)
						order[n], order[i] = order[i], order[n]
					}
					return
				}
				var lines []string
				permutations++
				for _, i := range order {
					lines = append(lines, tc.decls[i])
				}
				src := strings.Join(lines, "\n") + "\n"
				_, ds := check.CheckSource(src)
				// One declaration may lead an intrinsic group and an orphan
				// group, so the expected diagnostics are a list of messages.
				var want []string
				for _, group := range tc.value {
					want = append(want, e1cMessage(strings.Split(group, ","), true))
				}
				for _, group := range tc.defined {
					want = append(want, e1cMessage(strings.Split(group, ","), false))
				}
				if len(ds) != len(want) {
					t.Errorf("order %v: want %d independent groups, got %v", order, len(want), ds)
				}
				for _, d := range ds {
					name := strings.Split(d.Message, " ")[0]
					offset := strings.Index(src, "type "+name+" ")
					if offset < 0 {
						offset = strings.Index(src, "type "+name+"{")
					}
					if offset < 0 {
						offset = strings.Index(src, "type "+name+"[")
					}
					offset += len("type ")
					i := slices.Index(want, d.Message)
					if d.Code != "S108" || i < 0 || d.Offset != offset || d.Column != 6 || d.Length != len(name) || d.Line != strings.Count(src[:max(0, offset)], "\n")+1 {
						t.Errorf("order %v: unexpected group or name span: %+v; want %v", order, d, want)
						continue
					}
					want = slices.Delete(want, i, i+1)
				}
				if len(want) != 0 {
					t.Errorf("order %v: missing components %v", order, want)
				}
				if problems := check.OrderProblems(src, 8); len(problems) != 0 {
					t.Errorf("order %v: %v", order, problems)
				}
			}
			visit(0)
			t.Logf("checked %d declaration permutations", permutations)
		})
	}
}

func TestE1DefinedTypeCycleClassification(t *testing.T) {
	for _, src := range []string{"type Zebra Alpha\ntype Alpha Zebra\n", "type Alpha Zebra\ntype Zebra Alpha\n"} {
		_, ds := check.CheckSource(src)
		if len(ds) != 1 || ds[0].Code != "S108" || ds[0].Message != "Alpha is defined over itself with Zebra (types.md, Defined types)" ||
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
