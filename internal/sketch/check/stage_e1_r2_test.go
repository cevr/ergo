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

// Conversions carry the same complete instance facts as direct calls.
func TestE1ConversionInstanceFacts(t *testing.T) {
	const src = `type Valid { Check() bool, }
type Card { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func Values(c Card) []Valid { []Valid{Box[Card]{V: c}} }
`
	pkg, ds := check.CheckSource(src)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	count := 0
	for _, conv := range pkg.Info.Implicit {
		if conv.Kind != check.ConvOperations {
			continue
		}
		inst := conv.Instances["Check"]
		if inst == nil || inst.Func != conv.Table["Check"] || len(inst.TypeArgs) != 1 || len(inst.Witnesses) != 1 {
			t.Fatalf("incomplete conversion instance: %+v", conv)
		}
		w := inst.Witnesses[0]
		if len(w.Ops) != 1 || w.Ops[0].Func == nil || w.Ops[0].Sig == nil || w.Ops[0].Name != "Check" || w.Arg.String() != "main.Card" {
			t.Fatalf("incomplete conversion witness: %+v", w)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("want one operations conversion, got %d", count)
	}
}
