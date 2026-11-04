package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// C46 (check-v1 E6b): FuzzCheck input 658ea47f5470cbee. A call that typing
// skips for a diagnosed reason (an arity error, a callee of unknown or
// non-function type, an unknown name) and that names, outside one of its
// callback frames, a function class G refuses: the reporting pass recorded
// that frame invalid-diagnosed when the refused name sat where typing did
// not read it (a callback frame of the callee that an arity error skipped)
// or in a later component (whose reference reads no undiagnosed value).
// The reference walk (RefWalkProblems, class H) holds that such a frame is
// never invalid-diagnosed, so the class G uses inside it are never
// dropped. The family varies the refused member, the callback position
// and the diagnosis that skips the call; every program must have the
// member's class G S108, no reference-walk problem and the same verdict
// in every declaration order.
func TestClassHC46Family(t *testing.T) {
	n, refused := 0, 0
	for _, m := range c46Members {
		for _, p := range c46Positions {
			for _, d := range c46Diagnoses {
				src := c46Program(m, p, d)
				name := m.name + "/" + p.name + "/" + d.name
				_, diags := check.CheckSource(src)
				if !hasGroupRefusal(diags, m.refused) {
					t.Errorf("%s: no class G S108 for %s: %v\n%s", name, m.refused, diags, src)
					continue
				}
				refused++
				if problems := check.RefWalkProblems(src); len(problems) > 0 {
					t.Errorf("%s: reference walk: %s\n%s", name, strings.Join(problems, "; "), src)
				}
				if problems := check.OrderProblems(src, 2); len(problems) > 0 {
					t.Errorf("%s: declaration order: %s", name, problems[0])
				}
				n++
			}
		}
	}
	if want := len(c46Members) * len(c46Positions) * len(c46Diagnoses); n != want || refused != want {
		t.Errorf("%d programs checked, %d with the class G S108; want %d", n, refused, want)
	}
}

// A c46Member declares the function class G refuses (refused) and the
// function g whose body holds the skipped call; inGroup puts g in the
// member's component.
type c46Member struct {
	name, refused, decls string
}

var c46Members = []c46Member{
	// A self-recursive member; g is a later component.
	{"self", "n", `func n() int {
	let f = n
	Console.Println("x")
	1
}
`},
	// The member calls g: g is in its component, so its reference reads
	// an undiagnosed value where typing reads it.
	{"group", "n", `func n() int {
	let f = n
	Console.Println("x")
	g()
	1
}
`},
	// Two members; n is refused for m's value use of it.
	{"pair", "n", `func m() int {
	let f = n
	Console.Println("y")
	n()
}

func n() int {
	m()
}
`},
}

// A c46Position is the skipped call, with N for the refused name and K
// for the callee.
type c46Position struct{ name, call string }

var c46Positions = []c46Position{
	{"trailing-in-callee-frame", "K(){ (N) }(){}"},
	{"literal-in-callee-frame", "K(){ (N) }(func() {})"},
	{"call-in-callee-frame", "K(){ N() }(){}"},
	{"selector-callee", "K(){ N() }.x(){}"},
	{"index-callee", "K(){ N() }[0](){}"},
	{"literal-argument", "K(func() { N }){}"},
	{"value-argument", "K(N)(){}"},
}

// A c46Diagnosis is what skips the call: K's declaration (or none) and
// the name the call uses.
type c46Diagnosis struct{ name, decl, callee string }

var c46Diagnoses = []c46Diagnosis{
	{"arity", "func k() int { 1 }\n", "k"},
	{"unknown-result", "func k(f func()) bad.T { 1 }\n", "k"},
	{"not-a-function", "func k(f func()) int { 1 }\n", "k"},
	{"unknown-name", "", "kk"},
}

func c46Program(m c46Member, p c46Position, d c46Diagnosis) string {
	call := strings.NewReplacer("K", d.callee, "N", m.refused).Replace(p.call)
	return fmt.Sprintf("%s%s\nfunc g() {\n\t%s\n}\n", d.decl, m.decls, call)
}

// hasGroupRefusal reports the class G S108 at the declaration of name.
func hasGroupRefusal(diags []check.Diagnostic, name string) bool {
	for _, d := range diags {
		if d.Code == "S108" && strings.HasPrefix(d.Message, name+" is used as a value") && strings.Contains(d.Message, "inside its own recursive group") {
			return true
		}
	}
	return false
}
