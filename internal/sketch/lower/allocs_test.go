package lower

import (
	"fmt"
	"strings"
	"testing"
)

// allocsProgram counts its own allocations in a test (MX-P3 F6): the
// counter is testing.Allocs, which only the test's ambient makes.
const allocsProgram = `import testing

func step(n int) int {
	n * 2 + 1
}

let counted = context needs TestAmbient {
	testing.Allocs: testing.NewAllocCounter(),
}

test StepAllocatesNothing {
	with counted {
		let mut total = 0
		assert testing.Allocs.PerRun(100, func() { total = total + step(3) }) == 0
	}
}
`

// TestTestCountsItsAllocations: an Ergo test counts the heap allocations
// of a call with testing.Allocs under a context that needs TestAmbient: it
// checks, and its lowered test calls the counter's body, AllocsPerRun. A
// main that makes the counter is S102, since only the test runner gives
// TestAmbient. Mutant (stdlib mutants.json): f6-counter-ambient-free.
func TestTestCountsItsAllocations(t *testing.T) {
	u := checkPackage(t, "main", []pkgSource{{testSourcePath, allocsProgram}}, nil)
	files, _, err := LowerIRTests(u)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		all.Write(f.Data)
	}
	text := all.String()
	at := strings.Index(text, "func "+goHelper("ergoTestStepAllocatesNothing")+"(")
	if at < 0 {
		t.Fatalf("no lowered test StepAllocatesNothing:\n%s", text)
	}
	if body := text[at:]; !strings.Contains(body, ".Allocs.PerRun(") || !strings.Contains(body, ".OpenTestDirectory()") {
		t.Errorf("the test does not open its folder and count through testing.Allocs:\n%s", body)
	}
	if !strings.Contains(text, ".AllocsPerRun(") {
		t.Errorf("no lowered file calls the counter's body AllocsPerRun:\n%s", text)
	}
	const outside = `import testing

func main() needs Console {
	let c = testing.NewAllocCounter()
	Console.Println("\(testing.PerRun(c, 1, func() {}))")
}
`
	q := lowerSource("allocsoutside", outside, true)
	if got := fmt.Sprint(q.diags); q.err != nil || !strings.Contains(got, "S102") || !strings.Contains(got, "TestAmbient") {
		t.Errorf("a main that makes the counter: err=%v diagnostics=%s, want S102 naming TestAmbient", q.err, got)
	}
}
