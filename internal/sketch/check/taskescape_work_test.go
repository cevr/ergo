package check

import (
	"fmt"
	"strings"
	"testing"
)

// holdsTask is linear in the type graph: every nested container's
// type-position check and every use of the value walk it. Count the walk's
// visits (cache hits included), not host time.
func TestTaskEscapeTypeWalkIsLinear(t *testing.T) {
	for _, depth := range []int{50, 100, 200} {
		for _, shape := range []struct{ open, close string }{
			{"[]", ""}, {"Map[string, ", "]"}, {"(", ", int)"},
		} {
			t.Run(fmt.Sprintf("%s-%d", shape.open, depth), func(t *testing.T) {
				typ := strings.Repeat(shape.open, depth) + "int" + strings.Repeat(shape.close, depth)
				c, diags := checkFor("func f(x " + typ + ") { _ = x }\n")
				if len(diags) != 0 {
					t.Fatalf("diagnostics: %v", diags)
				}
				if c.taskSteps <= depth || c.taskSteps > 12*depth+32 {
					t.Fatalf("depth=%d: %d Task-walk visits outside (%d, %d]", depth, c.taskSteps, depth, 12*depth+32)
				}
				t.Logf("depth=%d Task-walk visits=%d bound=%d", depth, c.taskSteps, 12*depth+32)
			})
		}
	}
}

// An instance walk could generate an infinite sequence of types before the
// instantiation-cycle diagnostic. The Task walk visits only declared bodies,
// so the graph it walks is finite.
func TestTaskWalkDoesNotExpandArguments(t *testing.T) {
	for _, declaration := range []string{
		"type E[T] []E[[]T]",
		"type E[T] Map[string, E[(T, T)]]",
	} {
		t.Run(declaration, func(t *testing.T) {
			c, diags := checkFor(declaration + "\nfunc f(x E[int]) int { let local = x; let g = func() int { len(local) }; g() }\n")
			if len(diags) != 1 || diags[0].Code != CodeType || !strings.Contains(diags[0].Message, "instantiation cycle") {
				t.Fatalf("want an instantiation-cycle diagnostic, got %v", diags)
			}
			if c.taskSteps == 0 || c.taskSteps > 128 {
				t.Fatalf("Task walk took %d visits; want at most 128 on the finite declaration graph", c.taskSteps)
			}
			t.Logf("Task-walk visits=%d bound=128", c.taskSteps)
		})
	}
}

// A component's members share one answer: a Task anywhere on a cycle is
// held by every type on it, whichever member the walk starts from.
func TestTaskWalkComponents(t *testing.T) {
	const src = "type Fault {}\ntype A[T] []B[T]\ntype B[T] Map[string, A[T]]\ntype R { kids []R, n int }\n" +
		"func f(x A[Task[int, Fault]], y B[int], z B[Task[int, Fault]], r R) {}\n"
	want := []bool{true, false, true, false}
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}} {
		c, diags := checkFor(src)
		if len(diags) != 0 {
			t.Fatalf("diagnostics: %v", diags)
		}
		var params []*Var
		for _, f := range c.funcs {
			if f.Name == "f" {
				params = f.Sig.Params
			}
		}
		if len(params) != 4 {
			t.Fatalf("parameters of f: %v", params)
		}
		// The use pass cached some answers; start from an empty cache.
		c.taskMemo = nil
		for _, i := range order {
			if got := c.holdsTask(params[i].Type); got != want[i] {
				t.Errorf("order %v: holdsTask(%s) = %v, want %v", order, params[i].Type, got, want[i])
			}
		}
	}
}
