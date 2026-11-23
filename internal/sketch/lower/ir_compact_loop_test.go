package lower

import (
	"encoding/json"
	"testing"
)

// TestIRCompactSliceLoopForm guards the two source shapes that admit the
// same loop to Go's inliner as hand Go: a native zero-based range and a
// native slice allocation. Neither may replace a different start or
// turn the runtime defect of a negative capacity into a compiler error.
//
// Mutants: ir-compact-keep-count, ir-compact-ignore-start, ir-compact-blank-range,
// ir-compact-keep-make-call, ir-compact-negative-constant,
// ir-compact-computed-bound, ir-compact-nested-frame.
func TestIRCompactSliceLoopForm(t *testing.T) {
	const src = `
func work(n int) int {
	let xs = MakeSlice[int](n)
	let mut total = len(xs)
	for i in 0..n { total = total + i }
	for i in 1..n { total = total + i }
	for _ in 0..n { total = total + 1 }
	for i in 0..(n-1) { total = total + i }
	total + len(MakeSlice[int](0))
}
func nested(n int) int {
	let xs = MakeSlice[int](n)
	let mut total = len(xs)
	for i in 0..n {
		for j in 0..n { total = total + i + j }
	}
	total
}
func negative() int { len(MakeSlice[int](-1)) }
func main() needs Console {
	Console.Println("\(work(4)) \(nested(4))")
	Console.Println("\(negative())")
}`
	u := factsUnit(t, src)
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Fatalf("unexpected fallback: %v\n%s", rep.Fallback, src)
	}
	ranges, counts, makes, defects := 0, 0, 0, 0
	var expr func(*irExpr, bool)
	expr = func(e *irExpr, negative bool) {
		if e == nil {
			return
		}
		if e.K == "builtin" && e.Name == "make" && !negative {
			makes++
		}
		if e.K == "pcall" && negative {
			defects++
		}
		expr(e.X, negative)
		expr(e.Y, negative)
		for _, a := range e.Args {
			expr(a, negative)
		}
	}
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			if fn.Name != "work" && fn.Name != "negative" && fn.Name != "nested" {
				continue
			}
			for _, st := range fn.Body {
				switch st.K {
				case "range":
					ranges++
				case "count":
					counts++
				}
				expr(st.X, fn.Name == "negative")
			}
		}
	}
	if ranges != 1 || counts != 4 || makes != 2 || defects != 1 {
		t.Fatalf("native range=%d count=%d native make=%d negative runtime call=%d; want 1, 4, 2, 1\n%s",
			ranges, counts, makes, defects, src)
	}
	typecheckGo(t, []*lowered{{name: "compact_slice_loop", src: src, files: files}})
}
