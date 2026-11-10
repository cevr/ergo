package lower

import "testing"

// Keep the admitted-program sweep as public CLI runs. The cases cross
// generic constraints, dictionary forwarding, conversions and scope edges.
func TestErgoRunE1GenericDomain(t *testing.T) {
	needsBuild(t)
	for _, tc := range []struct{ name, decls, body, want string }{
		{"closure-generic-instance", `func Later[T Valid](x T) func() bool { func() bool { let f = All[T]; f(x) } }`, `let f = Later(Card{N: 1}); Console.Println("\(f())")`, "true\n"},
		{"nested-operation", `func Later[T Valid](x T) func() bool { func() bool { let g = func() bool { x.Check() }; g() } }`, `let f = Later(Card{N: 0}); Console.Println("\(f())")`, "false\n"},
		{"pointer-method-closure", `func Later[T Valid](x *T) bool { let f = func() bool { let m = x.Check; m() }; f() }`, `let mut c = Card{N: 1}; Console.Println("\(Later(&c))")`, "true\n"},
		{"parameter-conversion", `func Later[T Valid](x T) func() bool { func() bool { let v Valid = x; v.Check() } }`, `let f = Later(Card{N: 1}); Console.Println("\(f())")`, "true\n"},
		{"returned-conversion", `func Later[T Valid](x T) func() Valid { func() Valid { Box[T]{V: x} } }`, `let f = Later(Card{N: 1}); let v = f(); Console.Println("\(v.Check())")`, "true\n"},
		{"nested-pointer-witness", `func Via[T Valid](x *T) bool { All(x) }`, `let mut c = Box[Box[Card]]{V: Box[Card]{V: Card{N: 1}}}; Console.Println("\(Via(&c))")`, "true\n"},
		{"method-value-argument", `func Apply(f func() bool) bool { f() }
func Via[T Valid](x T) bool { Apply(x.Check) }`, `Console.Println("\(Via(Card{N: 0}))")`, "false\n"},
		{"ordered-closure", `func Min[T ordered](a T, b T) T { if a < b { a } else { b } }
func Later[T ordered](a T, b T) func() T { func() T { Min(a, b) } }`, `let f = Later(7, 3); let g = Later("z", "a"); Console.Println("\(f()) \(g())")`, "3 a\n"},
		{"number-instance-closure", `func Add[T number](a T, b T) T { a + b }
func Later[T number](a T, b T) func() T { func() T { let f = Add[T]; f(a, b) } }`, `let f = Later(4, 5); let g = Later(1.5, 2.0); Console.Println("\(f()) \(g())")`, "9 3.5\n"},
		{"comparable-record", `func Equal[T comparable](a T, b T) bool { a == b }
func Later[T comparable](a T, b T) func() bool { func() bool { Equal(a, b) } }`, `let f = Later(Box[int]{V: 2}, Box[int]{V: 2}); let g = Later("a", "b"); Console.Println("\(f()) \(g())")`, "true false\n"},
		{"two-witnesses", `type Scored { Score() int, }
type Mark { N int }
func Score(m Mark) int { m.N }
func Later[A Valid, B Scored](a A, b B) func() int { func() int { if All(a) { b.Score() } else { 0 } } }`, `let f = Later(Card{N: 1}, Mark{N: 9}); Console.Println("\(f())")`, "9\n"},
		{"instantiated-operation-constraint", `type Getter[T] { Get() T, }
type Cell[T] { V T }
func Get[T](c Cell[T]) T { c.V }
func Later[T Getter[int]](c T) func() int { func() int { c.Get() } }`, `let f = Later(Cell[int]{V: 8}); Console.Println("\(f())")`, "8\n"},
		{"map-conversion-closure", `func Later[T Valid](x T) func() Map[string, Valid] { func() Map[string, Valid] { Map[string, Valid]{"x": Box[T]{V: x}} } }`, `let f = Later(Card{N: 1}); let m = f(); for v in m { Console.Println("\(v.Check())") }`, "true\n"},
		{"slice-pointer-conversion", `func Later[T Valid](x *T) bool { let f = func() []Valid { []Valid{x} }; let vs = f(); vs[0].Check() }`, `let mut c = Card{N: 1}; Console.Println("\(Later(&c))")`, "true\n"},
		{"two-generic-layers", `func Inner[T Valid](x T) func() bool { func() bool { All(Box[T]{V: x}) } }
func Outer[T Valid](x T) func() bool { func() bool { let f = Inner(Box[T]{V: x}); f() } }`, `let f = Outer(Card{N: 1}); Console.Println("\(f())")`, "true\n"},
		{"pointer-closure-parameter", `func Via[T Valid](x *T) bool { let f = func(y *T) bool { All(y) }; f(x) }`, `let mut c = Card{N: 1}; Console.Println("\(Via(&c))")`, "true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := nestedWitnessDecls + tc.decls + "\nfunc main() needs Console {\n" + tc.body + "\n}\n"
			wantRun(t, src, tc.want)
			t.Logf("admitted and ran: %s", tc.name)
		})
	}
}
