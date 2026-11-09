package check

import (
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
