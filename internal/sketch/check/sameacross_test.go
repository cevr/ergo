package check

import (
	"fmt"
	"strings"
	"testing"
)

// TestSameAcrossTellsCapabilitiesApart is the law of the second check's
// guard (withown.go sameAcross): over capabilities of every argument form,
// each checked in two separate checks of one source, a capability of one
// check is the same as exactly one of the other's, its own. The pairs that
// differ only in a name, or in a row that holds the other's, are the ones
// a rebuild that drops a name or a row element would confuse.
//
// Mutants it kills that no other test does (ledger: rel5-same-*): the
// guard's name comparison dropped, and its row size comparison dropped
// (a row that holds another's then passes as it).
func TestSameAcrossTellsCapabilitiesApart(t *testing.T) {
	caps := []string{
		"Tab[Job]", "Tab[Bad]", "Tab[int]", "Tab[string]",
		"Ev[Bad]", "Ev[Worse]", "Ev[Bad | Worse]",
		"Tab[[]Job]", "Tab[[2]Job]", "Tab[[3]Job]", "Tab[Map[Job, int]]", "Tab[Map[int, Job]]", "Tab[(Job, int)]",
		"Tab[func(Job) int]", "Tab[func(Job) int fails Bad]", "Tab[func(Job) int fails Bad | Worse]", "Tab[func() needs A]",
		"Tab[func(*Job)]", "Tab[func(f func())]", "Tab[func(f escaping func())]", "Tab[func(r RB)]", "Tab[func(r RB) takes r]",
	}
	var b strings.Builder
	b.WriteString("type Tab[K] { Put(k K) }\ntype Ev[E] { Do() fails E }\ntype Job { id int }\ntype Bad { text string }\ntype Worse { n int }\ntype A { Use() }\ntype RB { id int }\nfunc Close(r RB) {}\n\n")
	for i, c := range caps {
		fmt.Fprintf(&b, "func f%d() needs %s {}\n", i, c)
	}
	b.WriteString("\nfunc main() {}\n")
	src := b.String()
	of := func() []Type {
		pkg, diags := CheckSource(src)
		if pkg == nil || len(diags) > 0 {
			t.Fatalf("source does not check: %v\n%s", diags, src)
		}
		out := make([]Type, len(caps))
		for _, f := range pkg.Funcs {
			var i int
			if _, err := fmt.Sscanf(f.Name, "f%d", &i); err != nil || f.Sig == nil {
				continue
			}
			out[i] = f.Sig.Uses.Elems()[0]
		}
		return out
	}
	one, two := of(), of()
	for i := range caps {
		for j := range caps {
			if got := sameAcross(one[i], two[j]); got != (i == j) {
				t.Errorf("sameAcross(%s, %s) = %v across two checks, want %v", caps[i], caps[j], got, i == j)
			}
		}
	}
}
