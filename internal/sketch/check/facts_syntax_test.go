package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

func TestFactsSyntaxS109Gate(t *testing.T) {
	const wantMsg = "value-tied fact clause waits for VF-1-FACTS-CHECK r2 (facts.md)"

	cases := []struct {
		name      string
		src       string
		wantCount int
	}{
		{
			name: "function with given gives keeps when",
			src: `func f(x int) int given x > 0 keeps x != 0 when x > 1 gives it >= 0 {
	x
}
func main() {}
`,
			wantCount: 3,
		},
		{
			name: "extern go with given",
			src: `extern go strings.ToUpper func Upper(s string) string given len(s) >= 0
func main() { _ = Upper("a") }
`,
			wantCount: 1,
		},
		{
			name: "operation method with given",
			src: `type Queue {
	Pop() int given true,
}
func use[T Queue](q T) { _ = q }
func main() {}
`,
			wantCount: 1,
		},
		{
			name: "function literal with gives",
			src: `func main() {
	let f = func(x int) int gives it == x { x }
	_ = f(1)
}
`,
			wantCount: 1,
		},
		{
			name: "function type parameter with given",
			src: `func apply(f func(x int) int given x > 0) int { f(1) }
func main() { _ = apply(func(x int) int { x }) }
`,
			wantCount: 1,
		},
		{
			name: "record where monomorphic and generic",
			src: `type Pos { X int } where it.X > 0
type Ascending[T ordered] { xs []T } where len(it.xs) >= 0
func main() {}
`,
			wantCount: 2,
		},
		{
			name: "record where with implements",
			src: `type Sized { Size() int }
type Box { V int } where it.V >= 0 implements Sized
func Size(b Box) int { b.V }
func main() {}
`,
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			s109 := 0
			for _, d := range diags {
				if d.Code != check.CodeOutside || !strings.Contains(d.Message, wantMsg) {
					t.Errorf("unexpected diagnostic %s: %s", d.Code, d.Message)
					continue
				}
				s109++
			}
			if s109 != tc.wantCount {
				t.Fatalf("got %d S109 diagnostics, want %d; all diags: %v", s109, tc.wantCount, diags)
			}
		})
	}

	cleanCases := []string{
		`func given(gives int) int { gives }
func main() {
	let keeps = given(1)
	_ = keeps
}
`,
		`type Port int where 1 <= it && it <= 65535
func main() {
	let p = ParsePort(80) catch {
	case Unsatisfied:
		return
	}
	_ = p
}
`,
	}
	for i, src := range cleanCases {
		_, diags := check.CheckSource(src)
		if len(diags) != 0 {
			t.Fatalf("clean case %d had diagnostics: %v", i, diags)
		}
	}
}
