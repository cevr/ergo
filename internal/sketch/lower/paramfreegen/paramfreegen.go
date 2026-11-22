// Package paramfreegen generates the programs of the param-free arm
// property (GEN9-GAP42 D; types.md Generics 9, resources.md R44) with
// their expected verdict and journals from a reference model. Two
// properties read it: the lowering's (internal/sketch/lower,
// TestParamFreeArmsVerdict and TestParamFreeArmsRun: the checker's
// verdict, the text lowering's test build and the evaluator) and the
// command's (cmd/ergo, TestParamFreeArmsNative: `ergo test --native`, the
// IR build).
//
// A marked generic body over an own generic union releases nothing of T,
// so it may drop the subject only on an arm whose variant holds no value
// of T: an empty variant, or one that holds only a concrete resource,
// which is released as any value's. An arm that drops a variant holding T,
// directly, through a record field or through an Option[T], is S108 Gap
// 42, as is a drop of the whole union.
//
// Each seed declares a union sNU[T own] over a choice of the variants
// below, a generic sNpick[T own] and its concrete twin sNpickRes over
// sNU[Res] with the same arms, and one test per variant and form that
// picks a value of that variant and finishes the result. Every Close
// writes to one journal. The model: the checker's verdict is S108 iff
// some arm drops a variant that holds T; an admitted program's journal is
// the model's for each variant, the same for pick and pickRes.
package paramfreegen

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// The variants a seed's union chooses from.
const (
	Payload  = iota // gP[T]{V T}: holds T
	Empty           // gE: holds nothing
	Nested          // gN[T]{O Option[T]}: holds T through an Option
	Record          // gB[T]{V T, K int}: holds T through a field
	Concrete        // gC{R Res}: holds a concrete resource only
	kinds
)

var names = [kinds]string{"gP", "gE", "gN", "gB", "gC"}

// HoldsT is whether the variant holds a value of T.
func HoldsT(k int) bool { return k == Payload || k == Nested || k == Record }

// Seed is one seed's declarations, tests and model.
type Seed struct {
	N     int
	Kinds []int
	// Drop is the arms that drop their variant (bind nothing).
	Drop map[int]bool
	// Admitted is the model's verdict: no arm drops a variant that
	// holds T.
	Admitted bool
	// Decls is the seed's union, pick and pickRes; Tests its tests.
	Decls, Tests string
	// TestNames is the seed's test names; a name ends in _pick or
	// _pickRes by the function it calls.
	TestNames []string
}

// Prelude is the declarations every seed shares.
const Prelude = `import sync

type Res { id int } implements Closer

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

func Close(r Res) { say("close \(r.id)") }

type gP[T own] { V T }

type gE {}

type gN[T own] { O Option[T] }

type gB[T own] { V T, K int }

type gC { R Res }

func finish(r own Option[Res]) {
	match r {
	case Some{Value: x}:
		Close(x)
	case None:
	}
}
`

// Generate is seed n.
func Generate(n int) *Seed {
	r := rand.New(rand.NewPCG(uint64(n), 0x9a42))
	s := &Seed{N: n, Drop: map[int]bool{}}
	s.Kinds = []int{Payload}
	for k := Empty; k < kinds; k++ {
		if r.IntN(2) == 0 {
			s.Kinds = append(s.Kinds, k)
		}
	}
	if len(s.Kinds) == 1 {
		s.Kinds = append(s.Kinds, Empty)
	}
	s.Admitted = true
	for _, k := range s.Kinds {
		// An empty or concrete variant binds no T: its arm drops it. A
		// variant that holds T hands it on, or, one time in four, drops it.
		s.Drop[k] = !HoldsT(k) || r.IntN(4) == 0
		if s.Drop[k] && HoldsT(k) {
			s.Admitted = false
		}
	}
	u := fmt.Sprintf("s%dU", n)
	var vs []string
	for _, k := range s.Kinds {
		if k == Empty || k == Concrete {
			vs = append(vs, names[k])
		} else {
			vs = append(vs, names[k]+"[T]")
		}
	}
	var d strings.Builder
	fmt.Fprintf(&d, "\ntype %s[T own] = %s\n", u, strings.Join(vs, " | "))
	fmt.Fprintf(&d, "\nfunc s%dpick[T own](o own %s[T]) Option[T] {\n%s}\n", n, u, s.arms())
	fmt.Fprintf(&d, "\nfunc s%dpickRes(o own %s[Res]) Option[Res] {\n%s}\n", n, u, s.arms())
	s.Decls = d.String()
	var b strings.Builder
	for i, k := range s.Kinds {
		for _, fn := range []string{"pick", "pickRes"} {
			name := fmt.Sprintf("S%dV%s_%s", n, names[k], fn)
			s.TestNames = append(s.TestNames, name)
			id := 100*n + 10 + i
			var quoted []string
			for _, l := range Journal(k, id) {
				quoted = append(quoted, strconv.Quote(l))
			}
			fmt.Fprintf(&b, "\ntest %s {\n\tjournal.Set([]string{})\n\tlet o %s[Res] = %s\n\tlet r = s%d%s(o)\n\tsay(\"picked\")\n\tfinish(r)\n\tassert journal.Get() == []string{%s}\n}\n",
				name, u, value(k, id), n, fn, strings.Join(quoted, ", "))
		}
	}
	s.Tests = b.String()
	return s
}

// Source is a program of the prelude and the seeds' declarations, with
// their tests when tests is set.
func Source(seeds []*Seed, tests bool) string {
	var b strings.Builder
	b.WriteString(Prelude)
	for _, s := range seeds {
		b.WriteString(s.Decls)
		if tests {
			b.WriteString(s.Tests)
		}
	}
	return b.String()
}

// arms is the match of pick's body.
func (s *Seed) arms() string {
	var b strings.Builder
	b.WriteString("\tmatch o {\n")
	for _, k := range s.Kinds {
		n := names[k]
		switch {
		case s.Drop[k]:
			fmt.Fprintf(&b, "\tcase %s:\n\t\tNone\n", n)
		case k == Payload, k == Record:
			fmt.Fprintf(&b, "\tcase %s{V: v}:\n\t\tSome{Value: v}\n", n)
		case k == Nested:
			fmt.Fprintf(&b, "\tcase %s{O: x}:\n\t\tx\n", n)
		}
	}
	b.WriteString("\t}\n")
	return b.String()
}

// value is a value of variant k holding the resource id.
func value(k, id int) string {
	switch k {
	case Payload:
		return fmt.Sprintf("gP[Res]{V: Res{id: %d}}", id)
	case Empty:
		return "gE"
	case Nested:
		return fmt.Sprintf("gN[Res]{O: Some[Res]{Value: Res{id: %d}}}", id)
	case Record:
		return fmt.Sprintf("gB[Res]{V: Res{id: %d}, K: 1}", id)
	}
	return fmt.Sprintf("gC{R: Res{id: %d}}", id)
}

// Journal is the model's journal of an admitted test of variant k: a
// dropped concrete variant is released in the pick, before "picked"; a
// handed-on T is closed by finish, after it; an empty variant closes
// nothing.
func Journal(k, id int) []string {
	closed := fmt.Sprintf("close %d", id)
	switch k {
	case Empty:
		return []string{"picked"}
	case Concrete:
		return []string{closed, "picked"}
	}
	return []string{"picked", closed}
}
