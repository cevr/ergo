package eval

import "testing"

// TestLiteralKeepsItsIterationsBinding is the smallest repro of a fixed
// bug (CF1, found by std html's StaticIsItsTree at check time): a function
// literal made in a loop read the loop's variable, and a match binding in
// the body, from the frame's table at call time, so every literal saw the
// last iteration, where the lowered Go (per-iteration loop variables)
// sees each its own. Mutant only it kills (internal/sketch/eval
// testdata/mutants.json): cf1-literal-shares-loop-slot.
func TestLiteralKeepsItsIterationsBinding(t *testing.T) {
	pkg := checked(t, `type box { v int }

type item = box | other

type other {}

func main() needs Console {
	let mut fs = []func() int{}
	for x in []int{1, 2} {
		fs = fs.Append(func() int { x })
	}
	for it in []item{box{v: 3}, box{v: 4}} {
		match it {
		case box as b:
			fs = fs.Append(func() int { b.v })
		case other:
		}
	}
	let mut total = 0
	for f in fs {
		total = total * 10 + f()
	}
	Console.Println("\(total)")
}
`)
	out, err := RunMain(pkg, DefaultBudget(), Process{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != "" || out.Stdout != "1234\n" || out.Status != 0 {
		t.Fatalf("main gives %+v, want stdout \"1234\\n\"", out)
	}
}
