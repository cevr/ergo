package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestGeneratorRankLimitIsAnError is the diagnostic's text and position for
// a type whose least value is deeper than a generator draws: a chain of
// records each holding the one before it ranks past check.GenRankLimit,
// and a property over it is refused at check time, where eval's bound
// would have ended the run (an ice now). Mutant lu-rank-limit-unchecked
// (the limit check removed) is its kill.
func TestGeneratorRankLimitIsAnError(t *testing.T) {
	n := check.GenRankLimit + 1
	var b strings.Builder
	b.WriteString("type R0 { A int }\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "type R%d { A R%d }\n", i, i-1)
	}
	fmt.Fprintf(&b, "test Deep(r R%d) { assert true }\n", n)
	ds := checkDiagList(t, b.String())
	// R_i's least value is i+1 levels deep, so R(n-1) is the first by name
	// past the limit.
	want := fmt.Sprintf("%d:13 S108 a property parameter is generated, and R%d cannot be: R%d has no value a generator reaches: its least value is %d levels deep, past %d (196l)", n+2, n, n-1, n, check.GenRankLimit)
	if len(ds) != 1 {
		t.Fatalf("got %d diagnostics %v, want\n%s", len(ds), ds, want)
	}
	d := ds[0]
	if got := fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

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
