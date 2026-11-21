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
law Skips(f func() bool) { Holds(f()) }
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
`, n, cap, p, s, h)
	return Program{Source: strings.TrimSpace(src) + "\n", First: first}
}

// Imported is a two-package case program: Source, package main, imports
// Lib, package LibPath, under the alias cl, as a name clash asks (main
// declares lib). Each law is false on its first case, whose arguments show
// Lib's names: Rack's field c is built by Lib's constructor
// (testplan.ByConstructor), its field g by Lib's builder of an operation
// type, and Metered's g by its provider's constructor. First is each law's
// first refuting case and Shows the text its arguments hold: Lib's names as
// Source spells them, cl.NewCrate(…), never lib.NewCrate(…) (ALIAS-CASE).
// A law's label (law Metered with g = lib.Meter) keeps the package's name:
// it is the law's key in the verdict ledger and for --law, so it must not
// change with a file's alias.
type Imported struct {
	LibPath, Lib, Source string
	First                map[string]int
	Shows                map[string][]string
}

// GenerateImported is the imported program. The seed changes the cases'
// draws, not the source.
func GenerateImported() Imported {
	return Imported{
		LibPath: "lib",
		Lib: `type Gauge { Level() int }
type Meter { N int } implements Gauge
func NewMeter(n int) Meter { Meter{N: n} }
func Level(m Meter) int { m.N }
type Crate { g Gauge }
func NewCrate(n int) Crate { Crate{g: NewMeter(n)} }
`,
		Source: `import cl lib
func lib() int { 0 }
type Racks { Count() int, law Bare() { Count() < 0 } }
type Rack { c cl.Crate, g cl.Gauge } implements Racks
func Count(r Rack) int { lib() }
law Metered(g cl.Gauge) { g.Level() * 0 == 1 }
`,
		First: map[string]int{"law Racks.Bare for Rack": 1, "law Metered with g = lib.Meter": 1},
		Shows: map[string][]string{
			"law Racks.Bare for Rack":        {"Rack{c: cl.NewCrate(…), g: cl.NewMeter(…)}"},
			"law Metered with g = lib.Meter": {"g = cl.NewMeter("},
		},
	}
}
