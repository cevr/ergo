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
	// Box has no constructor, so it is drawn field by field, and its field
	// is an operation type that its builder NewHeld draws (MX-EVLAW).
	h := target("law Cell.Kept for Box", 1)
	// Slow holds a func() with no rows, whose generator is the one
	// function that returns: it draws nothing, so a case draws Slow's N
	// alone, and the law calls Wait before it reads N (LAW-GEN-FUNC).
	w := target("law Waits.After for Slow", 1)
	// Shelf is drawn field by field too; its field Wrap is built by its
	// constructor, whose record holds an operation type, so a case shows
	// the constructor's call (testplan.ByConstructor; CASEFIX2). Its law
	// is false on the first case.
	first["law Shelves.Empty for Shelf"] = 1
	// Bounds straddle the finite-type cap; the expected evidence comes from
	// integer-domain cardinality, not from testplan.Domain.
	cap := 255 + int(seed%2)
	src := fmt.Sprintf(`
type Flag { A bool, B bool }
test Property(x Flag) { assert !x.A }
func Holds(x bool) bool { x }
func Num(x int) int { x }
law Refutes(x bool) { Holds(x) }
law Samples(x int) { Num(x) != %d }
type Bounded int where 0 <= it && it <= %d
law Domain(x Bounded) { x >= 0 }
type Reader { Read() int, law Fresh() { Read() != %d } }
type P { N int } implements Reader
func NewP(n int) P { P{N: n} }
func Read(p P) int { p.N }
law Slot(r Reader, x bool) { r.Read() != %d }
test Nested(xs []int8, m Map[string, int16], pair (bool, int), f float32, s string) { assert len(xs) == 0 }
type Mirror[T comparable] { Echo(v T) T, law Same(v T) { Echo(v) == v } }
type BoolEcho { B bool } implements Mirror[bool]
func Echo(p BoolEcho, v bool) bool { v }
type IntEcho { B bool } implements Mirror[int]
func Echo(p IntEcho, v int) int { v }
type Inner { Get() int }
type Held { N int } implements Inner
func NewHeld(n int) Held { Held{N: n} }
func Get(h Held) int { h.N }
type Cell { Got() int, law Kept() { Got() != %d } }
type Box { r Inner } implements Cell
func Got(b Box) int { b.r.Get() }
type Wrap { r Inner }
func NewWrap(n int) Wrap { Wrap{r: NewHeld(n)} }
type Shelves { Size() int, law Empty() { Size() < 0 } }
type Shelf { w Wrap } implements Shelves
func Size(s Shelf) int { s.w.r.Get() * 0 }
type Left { L() int }
type Right { R() int }
type Twin { N int } implements Left, Right
func NewTwin() Twin { Twin{N: 0} }
func L(t Twin) int { t.N }
func R(t Twin) int { t.N + 1 }
type Pair { Sum() int, law Apart() { Sum() == 1 } }
type Two { l Left, r Right } implements Pair
func Sum(t Two) int { t.l.L() + t.r.R() }
type Waits { Ready() int, law After() { Ready() != %d } }
type Slow { Wait func(), N int } implements Waits
func Ready(s Slow) int {
	s.Wait()
	s.N
}
`, n, cap, p, s, h, w)
	return Program{Source: strings.TrimSpace(src) + "\n", First: first}
}
