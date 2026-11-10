package rt

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestChainCutAtBound: a defect copies at most maxChain tasks of its chain,
// and marks the chain cut exactly when an ancestor remained past the bound,
// so a reader can tell a cut chain from a complete one of the same length
// (runtime.md G2, rule 5).
func TestChainCutAtBound(t *testing.T) {
	if !strings.Contains(ChainCutLine, strconv.Itoa(maxChain)) {
		t.Fatalf("ChainCutLine %q does not name the bound %d", ChainCutLine, maxChain)
	}
	for _, n := range []int{1, 2, maxChain - 1, maxChain, maxChain + 1, maxChain + 8} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			tasks := make([]Task, n)
			x := Root("main")
			for i := 1; i < n; i++ {
				tasks[i].init(x, fmt.Sprintf("t%d", i), false, false)
				x = &tasks[i]
			}
			frames, cut := x.Chain()
			if want := min(n, maxChain); len(frames) != want || cut != (n > maxChain) {
				t.Fatalf("chain of %d tasks: %d frames, cut %t; want %d, %t", n, len(frames), cut, want, n > maxChain)
			}
			var c Cause
			c.defect(x, "boom", false, 0)
			rs := c.Err().(*Cause).Reasons
			if len(rs) != 1 || rs[0].ChainCut != (n > maxChain) {
				t.Fatalf("reason %+v; want ChainCut %t", rs, n > maxChain)
			}
			if got := strings.Contains(c.Error(), ChainCutLine); got != (n > maxChain) {
				t.Fatalf("Cause.Error marks the cut %t; want %t:\n%s", got, n > maxChain, c.Error())
			}
		})
	}
}

// TestFinishedTaskDropsValues: a finished task's handle keeps no context
// values, its own or inherited (runtime.md G1; bounded retention).
func TestFinishedTaskDropsValues(t *testing.T) {
	root := Root("main")
	root.WithValue("k", "inherited")
	s := NewScope(root)
	h := Go(&s, "child", func(ct *Task) (int, error) {
		ct.WithValue("own", "text")
		return 0, nil
	})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if h.Task().values != nil {
		t.Fatalf("a finished task keeps its context values")
	}
	var err error
	s.End(&err)
	if err != nil {
		t.Fatal(err)
	}
}
