package check_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestResourceTypeModel (#197 L8 A1) is a seeded property of mode
// crossing (resource.go; plan 1.2): it generates declaration graphs (records,
// empty records, defined basic types and unions, each with a Close of one
// shape or none; fields of slices, Maps, tuples, Options, a generic record,
// an operation type that takes, one that does not, and one with a Close,
// recursive through slices and Maps) and uses of them (a map key, a
// conversion to an operation type with no Close, a zero value, a takes
// clause on a parameter of each type and on a type parameter, a takes that
// names no parameter). A reference classifier written from plan 1.2 decides
// each verdict; the checker's codes and positions must equal it.
// ERGO_SEED=<n> replays one seed; a failure prints the Ergo source and both
// verdicts.
//
// Mutants it kills that no other test does (ledger: resource.go): a union
// crosses when one variant holds a resource; a back edge counts as a
// resource; a Close with a result, an error row or a parameter counts; a
// resource map key admitted; a takes operation of an operation type
// ignored, or counted on a crossing instance; a conversion to a crossing
// operation type admitted; takes on a crossing parameter admitted silently;
// a resource's zero value admitted.
func TestResourceTypeModel(t *testing.T) {
	seeds := make([]uint64, 0, 300)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(300) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		g := newResGen(seed)
		src, want := g.program()
		_, diags := check.CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Code))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			var full []string
			for _, d := range diags {
				full = append(full, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestResourceTypeModel ./internal/sketch/check)\nreference: %v\nchecker:   %v\n%s\n--- source\n%s", seed, seed, want, got, strings.Join(full, "\n"), src)
		}
	}
}

// The shapes of a generated type expression.
const (
	rxInt = iota
	rxString
	rxNamed  // T<i>
	rxSlice  // []e
	rxMap    // Map[string, e]
	rxTuple  // (int, e)
	rxOption // Option[e]
	rxGen    // G[e]: a generic record that holds e
	rxBox    // Box[e]: an operation type whose Put takes e
	rxView   // View[e]: an operation type with no takes and no Close
	rxRes    // Res: an operation type with Close
	rxParam  // T, a type parameter
)

type rexpr struct {
	kind int
	n    int
	elem *rexpr
}

// The kinds of a generated declaration and the shapes of its Close.
const (
	rdRecord = iota
	rdEmpty
	rdBasic
	rdUnion
)

const (
	rcNone = iota
	rcRight
	rcResult
	rcFails
	rcParam
)

type rdecl struct {
	kind   int
	fields []*rexpr // a union's variant A holds fields[0] (if any)
	close  int
}

type resGen struct {
	r     *rand.Rand
	decls []rdecl
	lines []string
	want  []string
}

func newResGen(seed uint64) *resGen {
	return &resGen{r: rand.New(rand.NewPCG(seed, 0x197a1))}
}

// expr makes a field type of declaration i: a named type by value only
// below i (a value cycle has no size), any through a slice, a Map or an
// operation type.
func (g *resGen) expr(i, depth int, byValue bool) *rexpr {
	k := g.r.IntN(11)
	if depth > 2 {
		k = g.r.IntN(3)
	}
	switch k {
	case rxInt, rxString:
		return &rexpr{kind: k}
	case rxNamed:
		if byValue {
			if i == 0 {
				return &rexpr{kind: rxInt}
			}
			return &rexpr{kind: rxNamed, n: g.r.IntN(i)}
		}
		return &rexpr{kind: rxNamed, n: g.r.IntN(len(g.decls))}
	case rxSlice, rxMap, rxBox, rxView:
		return &rexpr{kind: k, elem: g.expr(i, depth+1, false)}
	case rxTuple, rxOption, rxGen:
		return &rexpr{kind: k, elem: g.expr(i, depth+1, byValue)}
	}
	return &rexpr{kind: rxRes}
}

func (e *rexpr) String() string {
	switch e.kind {
	case rxInt:
		return "int"
	case rxString:
		return "string"
	case rxNamed:
		return fmt.Sprintf("T%d", e.n)
	case rxSlice:
		return "[]" + e.elem.String()
	case rxMap:
		return "Map[string, " + e.elem.String() + "]"
	case rxTuple:
		return "(int, " + e.elem.String() + ")"
	case rxOption:
		return "Option[" + e.elem.String() + "]"
	case rxGen:
		return "G[" + e.elem.String() + "]"
	case rxBox:
		return "Box[" + e.elem.String() + "]"
	case rxView:
		return "View[" + e.elem.String() + "]"
	case rxParam:
		return "T"
	}
	return "Res"
}

// holds is the reference classifier (plan 1.2): the least fixpoint of
// "holds a resource" over the declarations, a type parameter counting as
// params says.
func (g *resGen) holds(e *rexpr, decl []bool, params bool) bool {
	switch e.kind {
	case rxNamed:
		return decl[e.n]
	case rxSlice, rxMap, rxTuple, rxOption, rxGen, rxBox:
		return g.holds(e.elem, decl, params)
	case rxRes:
		return true
	case rxParam:
		return params
	}
	return false
}

func (g *resGen) declHolds() []bool {
	decl := make([]bool, len(g.decls))
	for changed := true; changed; {
		changed = false
		for i, d := range g.decls {
			if decl[i] {
				continue
			}
			h := d.close == rcRight
			for _, f := range d.fields {
				h = h || g.holds(f, decl, false)
			}
			if h {
				decl[i], changed = true, true
			}
		}
	}
	return decl
}

// keyable reports whether declaration i is comparable by construction: a
// basic or empty type, or a record of int, string and keyable types.
func (g *resGen) keyable(e *rexpr) bool {
	switch e.kind {
	case rxInt, rxString:
		return true
	case rxNamed:
		d := g.decls[e.n]
		switch d.kind {
		case rdBasic, rdEmpty:
			return true
		case rdRecord:
			for _, f := range d.fields {
				if f.kind == rxNamed && f.n >= e.n || !g.keyable(f) {
					return false
				}
			}
			return true
		}
	case rxGen:
		return g.keyable(e.elem)
	}
	return false
}

func (g *resGen) add(format string, args ...any) int {
	g.lines = append(g.lines, fmt.Sprintf(format, args...))
	return len(g.lines)
}

// at records a reference diagnostic at the column of the n-th occurrence
// (from 1) of marker in line ln, plus skip.
func (g *resGen) at(ln int, marker string, nth, skip int, code string) {
	text := g.lines[ln-1]
	col := -1
	for from := 0; nth > 0; nth-- {
		i := strings.Index(text[from:], marker)
		if i < 0 {
			panic(fmt.Sprintf("no %q in %q", marker, text))
		}
		col, from = from+i, from+i+len(marker)
	}
	g.want = append(g.want, fmt.Sprintf("%d:%d %s", ln, col+1+skip, code))
}

func (g *resGen) program() (string, []string) {
	n := 2 + g.r.IntN(5)
	g.decls = make([]rdecl, n)
	for i := range n {
		d := rdecl{kind: g.r.IntN(4), close: g.r.IntN(5)}
		switch d.kind {
		case rdRecord:
			for range 1 + g.r.IntN(3) {
				d.fields = append(d.fields, nil)
			}
		case rdUnion:
			d.fields = []*rexpr{nil}
		}
		g.decls[i] = d
	}
	for i := range g.decls {
		for j := range g.decls[i].fields {
			e := g.expr(i, 0, true)
			for e.kind == rxTuple || e.kind == rxSlice && e.elem.kind == rxTuple {
				// A field "f (int, X)" or "f [](int, X)" reads as an
				// operation: no tuple first.
				e = g.expr(i, 0, true)
			}
			g.decls[i].fields[j] = e
		}
	}
	g.add("type Bad {}")
	g.add("type Res { Close() }")
	g.add("type Named { Name() string }")
	g.add("type G[T] { v T, n int }")
	g.add("type Box[T] { Put(v T) takes v }")
	g.add("type View[T] { Get() T }")
	for i, d := range g.decls {
		switch d.kind {
		case rdRecord:
			var fs []string
			for j, f := range d.fields {
				fs = append(fs, fmt.Sprintf("f%d %s", j, f))
			}
			g.add("type T%d { %s }", i, strings.Join(fs, ", "))
		case rdEmpty:
			g.add("type T%d {}", i)
		case rdBasic:
			g.add("type T%d int", i)
		case rdUnion:
			g.add("type T%d = T%dA{v %s} | T%dB{}", i, i, d.fields[0], i)
		}
		switch d.close {
		case rcRight:
			g.add("func Close(x T%d) {}", i)
		case rcResult:
			g.add("func Close(x T%d) int { 0 }", i)
		case rcFails:
			ln := g.add("func Close(x T%d) fails Bad {}", i)
			g.at(ln, "Close", 1, 0, "S108") // R2
		case rcParam:
			g.add("func Close(x T%d, n int) {}", i)
		}
		g.add("func Name(x T%d) string { \"\" }", i)
	}
	decl := g.declHolds()
	for i, d := range g.decls {
		// R22: a conversion to an operation type with no Close.
		ln := g.add("func cv%d(x T%d) Named { x }", i, i)
		if decl[i] {
			g.at(ln, "{ x", 1, 2, "S108")
		}
		// R1: the zero value of a defaultable type.
		if d.kind == rdBasic || d.kind == rdEmpty {
			g.add("func dz%d() T%d {", i, i)
			ln := g.add("\tlet z T%d", i)
			if d.close == rcRight {
				g.at(ln, "z", 1, 0, "S108")
			}
			g.add("\tz")
			g.add("}")
		}
	}
	// R40: map keys.
	for k := range 1 + g.r.IntN(3) {
		var e *rexpr
		for range 20 {
			e = g.expr(len(g.decls), 0, true)
			if g.r.IntN(2) == 0 {
				e = &rexpr{kind: rxNamed, n: g.r.IntN(len(g.decls))}
			}
			if g.keyable(e) {
				break
			}
			e = nil
		}
		if e == nil {
			continue
		}
		ln := g.add("func key%d(m Map[%s, int]) {}", k, e)
		if g.holds(e, decl, false) {
			g.at(ln, "Map[", 1, len("Map["), "S112") // at the key
		}
	}
	// takes: on a parameter of each kind of type, and on a type parameter.
	for k := range 1 + g.r.IntN(3) {
		e := g.expr(len(g.decls), 0, false)
		ln := g.add("func tk%d(p %s) takes p {}", k, e)
		if !g.holds(e, decl, true) {
			g.at(ln, "takes p", 1, 6, "S110")
		}
	}
	if g.r.IntN(2) == 0 {
		g.add("func tg[T](p T, q G[T], r []T) takes p, q, r {}")
	}
	if g.r.IntN(3) == 0 {
		ln := g.add("func tw(p int) takes w {}")
		g.at(ln, "takes w", 1, 6, "S108")
	}
	g.add("func main() {}")
	return strings.Join(g.lines, "\n") + "\n", g.want
}
