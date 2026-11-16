package lower

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestInlinedCallEndsAnArm is the smallest repro of UC H8a's parse defect:
// a match or catch arm whose last statement is a call the lowering
// inlines (inline.go), when the body leaves early through its end label
// (an if / else if with no else), ended the Go case with `inl:` and
// nothing after it. A label needs a statement, and a `case` is none, so
// the emitted Go did not parse ("expected statement, found 'default'").
// It lowers the program in process and parses its Go. Mutant:
// inline-label-no-statement.
func TestInlinedCallEndsAnArm(t *testing.T) {
	const src = `type Box { n int }

func put(b *Box, k int) {
	b.n = b.n + k
}

func shown(b *Box, k int) {
	if k < 0 {
		put(b, 1)
	} else if k%2 == 0 {
		put(b, k)
	}
}

func main() needs Console {
	let mut b = Box{n: 0}
	for k in 0..10 {
		match k % 3 {
		case 0:
			shown(&b, -1)
		case _:
			shown(&b, k)
		}
	}
	Console.Println("\(b.n)")
}
`
	code := mustLower(t, "inlinelabel", src).mainGo()
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", code, parser.SkipObjectResolution); err != nil {
		t.Fatalf("the lowered Go does not parse: %v", err)
	}
	if !strings.Contains(code, "goto inl") {
		t.Fatalf("shown is not inlined with an end label, so the repro no longer reaches the label:\n%s", code)
	}
}
