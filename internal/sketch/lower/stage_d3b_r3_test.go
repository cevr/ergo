package lower

import (
	"strings"
	"testing"
)

// Stage D3b round 3 (check-v1): taking a method value binds its receiver,
// so it writes a statement, and every earlier operand is evaluated first
// (runtime.md, Evaluation order; stage D3b round 2 review M1).

// TestErgoRunMethodValueOperandOrder is the review's witness: the first
// argument fails with integer divide by zero before the method value's
// receiver ss[0] is read, which would fail with index out of range.
func TestErgoRunMethodValueOperandOrder(t *testing.T) {
	out, err := ergoRun(t, `type Shape { Area() int }
type Sq { N int }
func Area(s Sq) int { s.N }
func use(a int, f func() int) int { a + f() }
func main() needs Console {
    let zero = 0
    let ss = []Shape{}
    Console.Println("\(use(10 / zero, ss[0].Area))")
}
`)
	if err == nil || !strings.Contains(out, "integer divide by zero") || strings.Contains(out, "index out of range") {
		t.Fatalf("ergo run: %v\n%s\nwant the defect integer divide by zero, the first operand's", err, out)
	}
}
