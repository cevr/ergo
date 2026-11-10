package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// D-B7 admits one visible hop to a defined type's base, and duration
// converts only to/from int64. Go's broader underlying-type conversion
// rule must not let a rejected Ergo program reach lowering.
func TestConversionsRequireOneHop(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"sibling-defined-types", `type A int
type B int
func F(a A) B { return B(a) }`},
		{"duration-to-float", `func F(d duration) float64 { return float64(d) }`},
		{"duration-to-int", `func F(d duration) int { return int(d) }`},
		{"int-to-duration", `func F(n int) duration { return duration(n) }`},
		{"defined-two-hops", `type Length int
type Feet Length
func F(feet Feet) int { return int(feet) }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src + "\n")
			if len(d) != 1 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, "cannot convert") {
				t.Fatalf("want one conversion refusal, got %s", describe(d))
			}
		})
	}
}
