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
		// The rows of the E1c diagnosis case table (§2.2), with its "Rule
		// result" column, and the R2' controls.
		{"q21-holder", []string{"type A { X B }", "type B { Y A }", "type C { Y A }"}, []string{"A,B"}, nil},
		{"zz-reaches-other", []string{"type Bad { B Bad }", "type Zz[T] { V Zz2[T], W Bad }", "type Zz2[T] { V Zz[T] }"}, []string{"Bad", "Zz,Zz2"}, nil},
		{"q31", []string{"type Aa[T] { V T, N Aa[T] }", "type Zz[T] { V Aa[Zz[T]] }"}, []string{"Aa", "Zz"}, nil},
		{"q32", []string{"type Aa[T] { V T, N Option[Aa[T]] }", "type Zz[T] { V Aa[Zz[T]] }"}, []string{"Zz"}, nil},
		{"q30", []string{"type Bad { B Bad }", "type Agen[T] { V T, W Bad }", "type Zrec[T] { V Agen[Zrec[T]] }"}, []string{"Bad", "Zrec"}, nil},
		{"two-comp-share-generic", []string{"type W[T] { V T }", "type A { X W[B] }", "type B { Y A }", "type C { Z W[C] }"}, []string{"A,B", "C"}, nil},
		{"zrec-int-cascade", []string{"type Agen[T] { V T, W Zrec[int] }", "type Zrec[T] { V Agen[Zrec[T]] }"}, []string{"Zrec"}, nil},
		{"hold-cascade", []string{"type Aa[T] { V T, N Aa[T] }", "type Hold { X Aa[int] }"}, []string{"Aa"}, nil},
		{"mutual-generics-three", []string{"type G[T] { Next H[T] }", "type H[T] { Next K[T] }", "type K[T] { Next G[T] }"}, []string{"G,H,K"}, nil},
		{"renamed-two-params", []string{"type G[T] { X H[T, T] }", "type H[A, B] { Y G[A] }"}, []string{"G"}, nil},
		{"concrete-self-instance", []string{"type Box[T] { V T }", "type S[T] { F Box[S[int]] }"}, []string{"S"}, nil},
		{"concrete-self-direct", []string{"type P[T] { A T, B P[int] }"}, []string{"P"}, nil},
		{"tuple-defined-self", []string{"type A (A, int)"}, []string{"A"}, nil},
		{"tuple-defined-pair", []string{"type A (B, int)", "type B A"}, []string{"A,B"}, nil},
		// The R2/P5 counterexample: R2 hid the orphan {D1, D2} behind the
		// intrinsic group {D0, D2}; R2' reports both.
		{"r2-p5-three", []string{"type D0[T] { F0 D2[T] }", "type D1[T] { F0 D2[int] }", "type D2[T] { F0 D0[T], F1 D1[int] }"}, []string{"D0,D2", "D1,D2"}, nil},
		{"r2-p5-three-repaired", []string{"type D0[T] { F0 Option[D2[T]] }", "type D1[T] { F0 D2[int] }", "type D2[T] { F0 D0[T], F1 D1[int] }"}, []string{"D1,D2"}, nil},
		{"r2-p5-four", []string{
			"type D0 { F0 D2[D0], F1 Option[int] }",
			"type D1[T, U] { F0 D3[T, U], F1 []D3[T, U] }",
			"type D2[T] { F0 D3[int, int], F1 Option[D1[int, int]] }",
			"type D3[T, U] { F0 D1[T, U], F1 D2[int] }",
		}, []string{"D1,D3", "D2,D3"}, nil},
		{"r2-p5-four-repaired", []string{
			"type D0 { F0 D2[D0], F1 Option[int] }",
			"type D1[T, U] { F0 Option[D3[T, U]], F1 Option[[]D3[T, U]] }",
			"type D2[T] { F0 D3[int, int], F1 Option[D1[int, int]] }",
			"type D3[T, U] { F0 D1[T, U], F1 D2[int] }",
		}, []string{"D2,D3"}, nil},
		{"intrinsic-and-orphan-self", []string{"type D0[T] { F0 D0[T], F1 D0[int] }"}, []string{"D0"}, nil},
		{"intrinsic-and-orphan-distinct", []string{"type D0[T] { F0 D0[T], F1 D1[int] }", "type D1[T] { F0 D0[int] }"}, []string{"D0", "D0,D1"}, nil},
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
