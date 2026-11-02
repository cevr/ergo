package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Infinite polymorphic recursion is a source diagnostic, never a
// partially constructed instance or a host compiler failure (E1 r2 M1).
func TestE1GrowingInstantiationCycle(t *testing.T) {
	const src = `type Box[T] { V T }
func Grow[T](x T) { Grow(Box[T]{V: x}) }
`
	_, diags := check.CheckSource(src)
	if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 2 ||
		!strings.Contains(diags[0].Message, "cycle") || !strings.Contains(diags[0].Message, "Grow") {
		t.Fatalf("want S108 naming Grow's instantiation cycle at its call; got %v", diags)
	}
}
