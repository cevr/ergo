package eval

import "testing"

// TestNativeOnlyLawsRunNatively: the evaluator runs no Go, so a law
// binding whose provider calls Go (README Gap 17) is native with its
// reason, never passed and never refuted, even where the native run fails
// it (Twice); lower TestGoBackedLawsRunNatively runs the same program
// natively (types.md, Laws 10; owner q277). Mutant: g17-eval-passes (the
// evaluator runs the binding through its Go stub, or reports it passed).
func TestNativeOnlyLawsRunNatively(t *testing.T) {
	const src = `import go strings

type Upper {
	Up(s string) string,

	law Idem(s string) { Up(Up(s)) == Up(s) },
}

type Real { tag int } implements Upper

func NewReal() Real needs go.Foreign { Real{tag: len(strings.ToUpper("x"))} }

func Up(r Real, s string) string needs go.Foreign { strings.ToUpper(s) }

type Twice {} implements Upper

func Up(t Twice, s string) string needs go.Foreign { strings.Repeat(s, 2) }

func main() {}
`
	rep := evalSeeded(t, src, 7)
	want := map[string]string{
		"law Upper.Idem for Real":  NativeReason("Real"),
		"law Upper.Idem for Twice": NativeReason("Twice"),
	}
	for _, r := range rep.Results {
		w, ok := want[r.Name]
		if !ok {
			t.Errorf("unexpected result %s: %s (%s)", r.Name, r.Status, r.Reason)
			continue
		}
		delete(want, r.Name)
		if r.Status != Native || r.Reason != w {
			t.Errorf("%s: %s (%s), want native (%s)", r.Name, r.Status, r.Reason, w)
		}
	}
	for name := range want {
		t.Errorf("no result for %s", name)
	}
}
