package check

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// oscillate checks src with converge's body check replaced by a synthetic
// one that flips each function's error row between empty and {Bang} on
// every check and always reports a change: a component whose rows never
// settle, which no source produces under class G (round 14 O1). It returns
// the checker and its diagnostics.
func oscillate(t *testing.T, srcs ...string) (*checker, []Diagnostic) {
	t.Helper()
	var files []*syntax.File
	for _, src := range srcs {
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	c := newChecker("main", Std, prelude().scope)
	c.nodeCheck = func(n *bodyNode) bool {
		if n.fn == nil || n.fn.errVar == nil {
			return true
		}
		v := n.fn.errVar
		if v.sol.Empty() {
			bang := c.pkg.Scope.names["Bang"].(*TypeName).Type
			v.sol = c.types.row(bang)
		} else {
			v.sol = nil
		}
		c.newSolutions()
		return true
	}
	c.run(files)
	return c, c.sorted()
}

// TestConvergeRefusalNamesItsFile: an oscillating component that spans two
// files is refused with each function's S108 in that function's file, at
// its name. refuseRows once reported in whichever file was current (E2
// round 3; the per-pass clearing in run found it).
func TestConvergeRefusalNamesItsFile(t *testing.T) {
	srcs := []string{
		decided("type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc f0() { try f1() }\n"),
		decided("func f1() { try f0() }\n"),
	}
	_, diags := oscillate(t, srcs...)
	got := map[string]int{}
	for _, d := range diags {
		if !strings.HasPrefix(d.Message, "cannot infer the error row of ") {
			continue // the synthetic rows' own cascade, as in the one-file test
		}
		name := strings.TrimPrefix(strings.TrimSuffix(d.Message, "; write it"), "cannot infer the error row of ")
		if d.Code != CodeType || d.Offset+d.Length > len(srcs[d.File]) || srcs[d.File][d.Offset:d.Offset+d.Length] != name {
			t.Errorf("a refusal not at its function's name: file %d %d:%d %s", d.File, d.Line, d.Column, d.Message)
			continue
		}
		got[name] = d.File
	}
	if len(got) != 2 || got["f0"] != 0 || got["f1"] != 1 {
		t.Errorf("refusals by file: %v, want f0 in file 0 and f1 in file 1", got)
	}
}

// oscillatingRing is a ring of n functions with inferred rows, each calling
// the next, and a Bang error type. mark is an ordinary function that fails.
func oscillatingRing(n int) string {
	var b strings.Builder
	b.WriteString("type Bang {}\nfunc mark() fails Bang { return Bang }\n")
	for i := range n {
		fmt.Fprintf(&b, "func f%d() { try f%d() }\n", i, (i+1)%n)
	}
	return decided(b.String())
}

// distinctRing is a ring of n functions with inferred rows: f_i fails with
// its own error E_i and calls f_(i+1), so every row ends with all n
// errors: n·n row changes, the most a monotone solve of a ring makes.
func distinctRing(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "type E%d {}\nfunc mark%d() fails E%d { return E%d }\n", i, i, i, i)
	}
	for i := range n {
		fmt.Fprintf(&b, "func f%d() { try mark%d(); try f%d() }\n", i, i, (i+1)%n)
	}
	return decided(b.String())
}

// TestConvergeBoundMonotone: a monotone solve fits the bound. The ring of
// distinct errors is accepted with every error in every row, in fewer
// checks than 2·(bodies + edges)·(n + 1).
func TestConvergeBoundMonotone(t *testing.T) {
	for _, n := range []int{2, 10, 60} {
		c, diags := checkFor(distinctRing(n))
		if c == nil || len(diags) > 0 {
			t.Fatalf("n=%d: %v", n, diags)
		}
		for _, f := range c.funcs {
			if strings.HasPrefix(f.Name, "f") && len(f.errVar.sol.Elems()) != n {
				t.Fatalf("n=%d: %s has %d errors, want %d", n, f.Name, len(f.errVar.sol.Elems()), n)
			}
		}
		bound := 2 * (n + n) * (n + 1)
		t.Logf("n=%d: %d converge checks, bound %d", n, c.convergeChecks, bound)
		if c.convergeChecks > bound {
			t.Errorf("n=%d: %d checks past the bound %d", n, c.convergeChecks, bound)
		}
	}
}

// TestConvergeBoundOscillation: a component whose rows oscillate (empty,
// {Bang}, empty, …) reaches the bound and is refused with the S108 of every
// inferred function, after exactly the bound's checks: Bang is credited once,
// so 2·(bodies + edges)·(1 + 1) checks, whatever the oscillation would earn
// under a growth-based credit (round 14 O1: empty → Bang → empty earned
// credit each time Bang returned).
func TestConvergeBoundOscillation(t *testing.T) {
	for _, n := range []int{1, 2, 5, 40} {
		c, diags := oscillate(t, oscillatingRing(n))
		refused := 0
		for _, d := range diags {
			if d.Code == CodeType && strings.Contains(d.Message, "cannot infer the error row of") {
				refused++
			}
		}
		if refused != n {
			t.Errorf("n=%d: want %d refusals, got %v", n, n, diags)
		}
		bodies, edges := n, n
		if want := 2 * (bodies + edges) * 2; c.convergeChecks != want {
			t.Errorf("n=%d: converge ran %d checks, want the bound %d", n, c.convergeChecks, want)
		}
	}
}
