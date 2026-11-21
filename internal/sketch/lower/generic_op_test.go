package lower

import (
	"strings"
	"testing"
)

// genericOpSource declares Foldable, an operation type whose operation takes
// a type parameter of its own (types.md, Interface assignability, Generic
// operations; ADT-NOPRIV roles.md R1), two providers of it, and two
// functions over [M Foldable[int]] that call the operation with its type
// argument inferred and written.
const genericOpSource = `type Just[T] { Value T }
type Nothing {}
type Maybe[T] = Just[T] | Nothing implements Foldable[T]

type Pair[T] { A T, B T } implements Foldable[T]

type Foldable[T] {
	Fold[A](z A, f func(acc A, x T) A) A,
}

func Fold[T, A](m Maybe[T], z A, f func(acc A, x T) A) A {
	match m {
	case Just[T] as j:
		f(z, j.Value)
	case Nothing:
		z
	}
}

func Fold[T, A](p Pair[T], z A, f func(acc A, x T) A) A {
	f(f(z, p.A), p.B)
}

func Sum[M Foldable[int]](m M) int {
	m.Fold(0, func(acc int, x int) int { acc + x })
}

func Show[M Foldable[int]](m M) string {
	m.Fold[string]("", func(acc string, x int) string { acc + "<\(x)>" })
}

func main() needs Console {
	let j Maybe[int] = Just[int]{Value: 4}
	let n Maybe[int] = Nothing
	let p = Pair[int]{A: 2, B: 3}
	Console.Println("\(Sum(j)) \(Sum(n)) \(Sum(p))")
	Console.Println("[\(Show(j))] [\(Show(n))] [\(Show(p))]")
}
`

// genericOpRefusals are the checker's refusals of rule 3 (a provider's
// extra type parameters match the operation's in count and constraint)
// and rule 4 (an operation type with a generic operation has no values).
// Rows: each a diagnostic's text (AGENTS.md Tests).
var genericOpRefusals = []struct {
	name, decls, want string
}{
	{"value let", `func use(m Maybe[int]) { let s Foldable[int] = m }`,
		"Foldable has a generic operation, so it is a constraint and has no values; write [M Foldable[T]]"},
	{"value param", `func use(s Foldable[int]) {}`,
		"Foldable has a generic operation, so it is a constraint and has no values"},
	{"extra count", `type Box[T] { V T } implements Foldable[T]

func Fold[T, A, B](b Box[T], z A, f func(acc A, x T) A) A { z }`,
		"Box.Fold has 2 type parameters beyond its first parameter's, and Foldable.Fold has 1"},
	{"extra constraint", `type Box[T] { V T } implements Foldable[T]

func Fold[T, A comparable](b Box[T], z A, f func(acc A, x T) A) A { z }`,
		"Box cannot satisfy Foldable"},
}

// TestGenericOperations: a function over a constraint-only operation type
// runs at two providers, with the operation's type argument inferred
// (Sum) and written (Show), and it lowers as plain copies: the lowered Go
// declares no interface for Foldable (roles.md R1, Lowering). The
// refusals are the checker's. It builds Go because the claim is that the
// per-instance copies compile and run.
func TestGenericOperations(t *testing.T) {
	base := strings.SplitN(genericOpSource, "func Sum[", 2)[0]
	for _, tc := range genericOpRefusals {
		o := lowerSource("genop_"+strings.ReplaceAll(tc.name, " ", "_"), base+"\n"+tc.decls+"\n\nfunc main() {}\n", true)
		found := false
		var got []string
		for _, d := range o.diags {
			got = append(got, d.Message)
			found = found || strings.Contains(d.Message, tc.want)
		}
		if !found {
			t.Errorf("%s: want a diagnostic containing %q; got %q (err %v)", tc.name, tc.want, got, o.err)
		}
	}
	o := mustLower(t, "genop", genericOpSource)
	for _, f := range o.files {
		if text := string(f.Data); strings.Contains(text, "Foldable interface") || strings.Contains(text, "type Foldable") {
			t.Errorf("%s declares Foldable in Go: a constraint-only operation type lowers as copies only", f.Path)
		}
	}
	typecheckGo(t, []*lowered{o})
	needsBuild(t)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["genop"])
	if want := "4 0 5\n[<4>] [] [<2><3>]\n"; code != 0 || stdout != want {
		t.Errorf("exit %d, stdout %q, want %q; stderr:\n%s", code, stdout, want, firstLines(stderr, 8))
	}
}
