// Package casegen composes well-typed case-runner programs for the evaluator
// model and native agreement tests. It supplies source, never evaluated values.
package casegen

import (
	"fmt"
	"strings"

	"ergo.local/prototype/runtime/testgen"
)

// Program is a replayable source and the independent sequential runner model's
// first refuting cases. The stream is shared by design; execution is not.
type Program struct {
	Source string
	First  map[string]int
}

func Generate(seed uint64) Program {
	k := int(seed%70) + 5
	first := map[string]int{"Property": 3, "law Refutes": 1}
	target := func(name string, draws int) int64 {
		r := testgen.New(seed, name)
		values := make([]int64, k)
		for i := range values {
			r.Start(i + 1)
			values[i] = r.I64()
			if draws == 2 {
				_ = r.Bool()
			}
		}
		v := values[k-1]
		for i, x := range values {
			if x == v {
				first[name] = i + 1
				break
			}
		}
		return v
	}
	n := target("law Samples", 1)
	p := target("law Reader.Fresh for P", 1)
	s := target("law Slot with r = P", 2)
	// Bounds straddle the finite-type cap; the expected evidence comes from
	// integer-domain cardinality, not from testplan.Domain.
	cap := 255 + int(seed%2)
	src := fmt.Sprintf(`
type Flag { A bool, B bool }
test Property(x Flag) { assert !x.A }
law Refutes(x bool) { x }
law Samples(x int) { x != %d }
type Bounded int where 0 <= it && it <= %d
law Domain(x Bounded) { x >= 0 }
type Reader { Read() int, law Fresh() { Read() != %d } }
type P { N int }
func NewP(n int) P { P{N: n} }
func Read(p P) int { p.N }
law Slot(r Reader, x bool) { r.Read() != %d }
test Nested(xs []int8, m Map[string, int16], pair (bool, int), f float32, s string) { assert len(xs) == 0 }
law Skips(f func() bool) { f() }
type Mirror[T comparable] { Echo(v T) T, law Same(v T) { Echo(v) == v } }
type BoolEcho { B bool }
func Echo(p BoolEcho, v bool) bool { v }
type IntEcho { B bool }
func Echo(p IntEcho, v int) int { v }
`, n, cap, p, s)
	return Program{Source: strings.TrimSpace(src) + "\n", First: first}
}
