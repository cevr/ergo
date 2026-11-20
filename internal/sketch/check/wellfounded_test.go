package check_test

import (
	"fmt"
	"testing"
)

// TestBuilderWithoutBaseIsRefused is the diagnostic's text and position
// (BUILDER-WELLFOUNDED): Model's one builder, NewMetered, takes a Model,
// as std llm's does, so no finite Model is built. A property over a record
// that holds one is refused at its parameter's type, and the text names
// the builder, the cause, before the record that holds its type.
// eval's TestGenerationEndsOrIsRefused holds the rule over generated type
// graphs; this row holds the words. Mutant bw-builder-text-generic (the
// text says "each of its values holds another") is its kill.
func TestBuilderWithoutBaseIsRefused(t *testing.T) {
	src := `type Model { Ask() int }
type Metered { Inner Model } implements Model
func Ask(m Metered) int { m.Inner.Ask() }
func NewMetered(inner Model) Metered { Metered{Inner: inner} }
type Held { M Model }
test Holds(h Held) { assert true }
`
	ds := checkDiagList(t, src)
	want := "6:14 S108 a property parameter is generated, and Held cannot be: Model has no finite value: its builder NewMetered takes an argument that has none, as a builder that needs its own type does (196l)"
	if len(ds) != 1 {
		t.Fatalf("got %d diagnostics %v, want\n%s", len(ds), ds, want)
	}
	d := ds[0]
	if got := fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
