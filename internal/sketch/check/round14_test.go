package check

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

// admissionGroup is one generated recursive admission graph (round 14,
// class G) with the verdict its source decides.
type admissionGroup struct {
	src  string
	want []string // CODE@LINE, sorted
	// pinned and written count the value-used members without and with
	// written rows; grew counts the pinned members whose body needs a row.
	pinned, written, grew int
	// literal counts the admission calls through function literals, blocks
	// those with a trailing block.
	literal, blocks int
}

// recursiveAdmission writes one recursive group f0…f(n-1): a ring of
// references makes it one strongly connected component, and extra edges
// are calls, value uses (_ = fj) and admission calls whose type argument
// is a member's function type (Choose(BoxOf(fx), BoxOf(fy))), the round-13
// witnesses' shape. Every value-used member either writes its row or not;
// the oracle is the source rule alone (rows.md, Capability rows 6):
//   - a member used as a value inside the group has the rows it writes,
//     and each row it does not write is empty;
//   - every other member's row is the least fixpoint over its calls;
//   - a value-used member with an unwritten row whose body needs more than
//     the empty row gets one S108 at its declaration; nothing else is
//     reported.
//
// An admission call may also pass function literals that call the members
// (round 14 O1): a literal that is a value refers to them through its
// inferred rows, so it is a value use of each, at the literal. A literal
// that writes its capability row is no value use of a capability row.
//
// Every body may also hold a nested literal whose parameter or local is
// spelled as a member (lexical shadowing, round 14 M1): no reference.
//
// Admission calls pair members of the same kind (both written or both
// unwritten), so every one is admitted and no type mismatch is expected.
// In the error family every member fails (try mark()), and a call
// statement (try fj()) only reaches a member whose row is certain to be
// non-empty; in the capability family a member charges Cap through Cap.M()
// or Choose, or through a call, so a pinned member may need nothing and is
// accepted unwritten.
func recursiveAdmission(seed uint64, errors bool) admissionGroup {
	rng := rand.New(rand.NewPCG(seed, seed^0x51ab))
	shadows := rand.New(rand.NewPCG(seed, seed^0x5ad0))
	lits := rand.New(rand.NewPCG(seed, seed^0x11f7))
	n := 2 + rng.IntN(5)
	var g admissionGroup
	var b strings.Builder
	line := 0
	emit := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
		line++
	}
	if errors {
		emit("type Bang {}")
		// The fixed signatures are ordinary functions with written rows
		// (a user extern takes basic types only, C26).
		emit("func mark() fails Bang { return Bang }")
		emit("type Box[T] { M(T) }")
		emit("func BoxOf[T](x T) Box[T] { BoxOf(x) }")
		emit("func Choose[T](x T, y T) fails Bang { return Bang }")
		// E is an error row by its position after fails.
		emit("func Run[E, T](x T, y T, callback func() fails E) fails E { return callback() }")
	} else {
		emit("type Cap { M() }")
		emit("type Box[T] { M(T) }")
		emit("func BoxOf[T](x T) Box[T] { BoxOf(x) }")
		emit("func Choose[T](x T, y T) needs Cap { Cap.M() }")
		emit("func Same[T](x T, y T) {}")
		emit("func Run[T, R](x T, y T, callback func() needs R) needs R { callback() }")
	}
	// Which members are used as values, and which of those write rows.
	value := make([]bool, n)
	written := make([]bool, n)
	for i := range n {
		value[i] = rng.IntN(3) != 0
	}
	allWritten := rng.IntN(3) == 0
	for i := range n {
		if value[i] {
			written[i] = allWritten || rng.IntN(2) == 0
		}
	}
	type stmt struct {
		call, use []int // members called, members used as values
		seed      bool  // the body fails or charges by itself
		choose    bool  // an admission call through Choose (fails or charges)
		// block: an admission call through Run whose trailing block calls
		// member block-1 (round 15 M1); the block is a value of the row
		// Run infers, so a value use of that member.
		block int
	}
	bodies := make([][]stmt, n)
	// partner picks a value-used member of the same kind as x.
	partner := func(x int) int {
		var same []int
		for j := range n {
			if value[j] && written[j] == written[x] {
				same = append(same, j)
			}
		}
		return same[rng.IntN(len(same))]
	}
	callable := func(j int) bool {
		// A try reaches only a member whose row is not pinned empty.
		return !errors || !value[j] || written[j]
	}
	for i := range n {
		next := (i + 1) % n
		var ring stmt
		switch {
		case value[next] && (rng.IntN(2) == 0 || !callable(next)):
			if rng.IntN(2) == 0 {
				ring = stmt{use: []int{next}}
			} else {
				ring = stmt{use: []int{next, partner(next)}, choose: errors || rng.IntN(2) == 0}
			}
		case callable(next):
			ring = stmt{call: []int{next}}
		default:
			ring = stmt{use: []int{next}}
		}
		bodies[i] = append(bodies[i], ring)
		for range rng.IntN(3) {
			j := rng.IntN(n)
			switch {
			case value[j] && rng.IntN(2) == 0:
				bodies[i] = append(bodies[i], stmt{use: []int{j, partner(j)}, choose: errors || rng.IntN(2) == 0})
			case value[j]:
				bodies[i] = append(bodies[i], stmt{use: []int{j}})
			case callable(j):
				bodies[i] = append(bodies[i], stmt{call: []int{j}})
			}
		}
		if errors || rng.IntN(3) == 0 {
			bodies[i] = append(bodies[i], stmt{seed: true})
		}
	}
	// Admission calls with a trailing block (round 15 M1), from their own
	// random stream so the other statements stay as they were.
	blocks := rand.New(rand.NewPCG(seed, seed^0xb10c))
	var marked []int
	for j := range n {
		if value[j] {
			marked = append(marked, j)
		}
	}
	for i := range n {
		if len(marked) == 0 || blocks.IntN(2) == 0 {
			continue
		}
		x := marked[blocks.IntN(len(marked))]
		var same []int
		for _, j := range marked {
			if written[j] == written[x] {
				same = append(same, j)
			}
		}
		y := same[blocks.IntN(len(same))]
		bodies[i] = append(bodies[i], stmt{use: []int{x, y}, block: blocks.IntN(n) + 1})
		g.blocks++
	}
	// A member that is called but never used as a value stays inferred; in
	// the error family it is fallible, since every body fails.
	used := make([]bool, n) // a value use, at any line: class G is lexical (round 16)
	declLine := make([]int, n)
	for i := range n {
		row := ""
		if written[i] {
			if errors {
				row = " fails Bang"
			} else {
				row = " needs Cap"
			}
		}
		emit("func f%d()%s {", i, row)
		declLine[i] = line
		for _, s := range bodies[i] {
			switch {
			case s.seed && errors:
				emit("\ttry mark()")
			case s.seed:
				emit("\tCap.M()")
			case len(s.call) > 0 && errors:
				emit("\ttry f%d()", s.call[0])
			case len(s.call) > 0:
				emit("\tf%d()", s.call[0])
			case s.block > 0 && errors:
				emit("\ttry Run(BoxOf(f%d), BoxOf(f%d)) { try f%d() }", s.use[0], s.use[1], s.block-1)
			case s.block > 0:
				emit("\tRun(BoxOf(f%d), BoxOf(f%d)) { f%d() }", s.use[0], s.use[1], s.block-1)
			case len(s.use) == 1:
				emit("\t_ = f%d", s.use[0])
			default:
				fn := "Same"
				if s.choose {
					fn = "Choose"
				}
				prefix := ""
				if errors {
					prefix = "try "
				}
				switch {
				case lits.IntN(2) == 0:
					emit("\t%s%s(BoxOf(f%d), BoxOf(f%d))", prefix, fn, s.use[0], s.use[1])
				case errors:
					// Function literals that call the members and are values
					// (round 14 O1): value uses of them, at the literal.
					emit("\ttry %s(BoxOf(func() { try f%d() }), BoxOf(func() { try f%d() }))", fn, s.use[0], s.use[1])
					g.literal++
				default:
					emit("\t%s(BoxOf(func() { f%d() }), BoxOf(func() { f%d() }))", fn, s.use[0], s.use[1])
					g.literal++
				}
			}
			for _, j := range s.use {
				used[j] = true
			}
			if s.block > 0 {
				used[s.block-1] = true
			}
		}
		// A literal whose capability row is written carries no member's
		// capability row: no value use of it.
		if !errors && lits.IntN(3) == 0 {
			emit("\tSame(BoxOf(func() needs Cap { f%d() }), BoxOf(func() needs Cap { f%d() }))", lits.IntN(n), lits.IntN(n))
		}
		// Lexical shadowing (round 14 M1): a parameter or a local of a
		// nested literal spelled as a member is no reference to it.
		if r := shadows.IntN(3); r < 2 {
			id := fmt.Sprintf("f%d", shadows.IntN(n))
			if shadows.IntN(4) == 0 {
				id = "local"
			}
			if r == 0 {
				emit("\tfunc(%s func()) { %s() }(func() {})", id, id)
			} else {
				emit("\tfunc() { let _ as %s = func() {}; %s() }()", id, id)
			}
		}
		emit("}")
	}
	// The checker reads the formatted text, whose lines may differ: each
	// member's declaration line is read again there.
	g.src = decided(b.String())
	for i, l := range strings.Split(g.src, "\n") {
		for k := range n {
			if strings.HasPrefix(l, fmt.Sprintf("func f%d(", k)) {
				declLine[k] = i + 1
			}
		}
	}
	// A member marked as a value may get no value use: it is then an
	// ordinary member, inferred or written.
	pinned := make([]bool, n)
	for i := range n {
		pinned[i] = used[i] && !written[i]
		switch {
		case pinned[i]:
			g.pinned++
		case used[i]:
			g.written++
		}
	}

	// The oracle: a written row is the written one, a pinned row is empty,
	// and every other row is the least fixpoint over the calls.
	needs := make([]bool, n) // the body needs a non-empty row
	for changed := true; changed; {
		changed = false
		for i := range n {
			if needs[i] {
				continue
			}
			for _, s := range bodies[i] {
				hit := s.seed || s.choose || s.block > 0 && written[s.block-1]
				for _, j := range s.call {
					if written[j] || needs[j] && !pinned[j] {
						hit = true
					}
				}
				if hit {
					needs[i], changed = true, true
					break
				}
			}
		}
	}
	for i := range n {
		if pinned[i] && needs[i] {
			g.grew++
			g.want = append(g.want, fmt.Sprintf("S108@%d", declLine[i]))
		}
	}
	sort.Strings(g.want)
	return g
}
