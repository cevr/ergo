package check

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// Seeded property and concrete repros for lender borrow exclusivity across
// callbacks (NOU-LIVE-LENDER-EXCLUSIVITY; types.md, Borrows 2).
//
// While a call holds a mutable borrow of a place P and passes a borrow (or a
// callee-provided capability) into a callback F, F (and any nested closure or
// local frame it calls) cannot also access any place overlapping P, nor invoke
// an opaque non-borrowing, non-escaping closure parameter that could have
// captured P's root from outside. Non-overlapping places (distinct roots,
// distinct struct fields, distinct constant indexes, or guarded immutable
// indexes) and re-entrant non-borrowing callbacks (such as Scope(a, func() {
// ... })) stay admitted.
//
// Replay: go test -run '^TestBorrowCallbackExclusivityModel$' ./internal/sketch/check -args -lender.seed=N

var lenderSeed = flag.Uint64("lender.seed", 0, "run TestBorrowCallbackExclusivityModel on this seed alone")

const lenderPrelude = `type Box { n int }

type Pair { a Box, b Box }

type Cell {
	mut Set(v int),
	mut WithMut(f func(v mut int)),
}

type Entry { n int } implements Cell

func Set(e mut Entry, v int) {
	e.n = v
}

func WithMut(e mut Entry, f func(v mut int)) {
	f(&e.n)
}

func WithMutR[R](e mut Entry, f func(v mut int) needs R) needs R {
	f(&e.n)
}

func Free(b mut Box) {
	b.n = 0
}

func Read(b Box) int {
	b.n
}

func Update(b mut Box, f func(v mut int)) {
	f(&b.n)
}

func UpdatePair(p mut Pair, f func(v mut int)) {
	f(&p.a.n)
}

func Scope(b mut Box, f func()) {
	f()
}

func Run(f func()) {
	f()
}

func WithCell(e mut Entry, f func() needs Cell) {
	with context { Cell: &e } {
		f()
	}
}
`

// TestBorrowCallbackExclusivityRepros pins the concrete NOU-LIVE-LENDER-EXCLUSIVITY
// soundness bug shapes and their diagnostic positions/messages, alongside
// non-aliasing and re-entrant controls.
func TestBorrowCallbackExclusivityRepros(t *testing.T) {
	base := strings.Count(lenderPrelude, "\n") + 1
	at := func(relLine, col int, msg string) string {
		return fmt.Sprintf("S108@%d:%d %s", base+relLine, col, msg)
	}
	cases := []struct {
		name string
		src  string
		want string // "" for admitted, else exact prefix including S108@line:col
	}{
		{
			name: "invalidate-direct-mut-param",
			src: `func invalidate(b mut Box) {
	b.Update(func(v mut int) {
		b.Free()
		*v = 7
	})
}
`,
			want: at(3, 3, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-direct-let-mut",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	b.Update(func(v mut int) {
		b.Free()
		*v = 7
	})
}
`,
			want: at(4, 3, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-explicit-amp",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	Update(&b, func(v mut int) {
		let _ = Read(b)
		*v = 7
	})
}
`,
			want: at(4, 16, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-nested-closure",
			src: `func invalidate(b mut Box) {
	b.Update(func(v mut int) {
		Run(func() {
			b.Free()
		})
		*v = 7
	})
}
`,
			want: at(4, 4, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-local-frame-arg",
			src: `func invalidate(b mut Box) {
	let cb = func(v mut int) {
		b.Free()
		*v = 7
	}
	b.Update(cb)
}
`,
			want: at(6, 11, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-local-frame-called-inside",
			src: `func invalidate(b mut Box) {
	let kill = func() {
		b.Free()
	}
	b.Update(func(v mut int) {
		kill()
		*v = 7
	})
}
`,
			want: at(6, 3, "b is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-borrow-alias",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	let r = &b
	r.Update(func(v mut int) {
		b.Free()
		*v = 7
	})
}
`,
			want: at(5, 3, "r is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-borrow-alias-pattern",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	let (r, _) = (&b, 1)
	r.Update(func(v mut int) {
		b.Free()
		*v = 7
	})
}
`,
			want: at(5, 3, "r is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-borrow-converted-op-recv",
			src: `func invalidate() {
	let mut e = Entry{n: 10}
	let c Cell = &e
	c.WithMut(func(v mut int) {
		e.n = 23
		*v = 7
	})
}
`,
			want: at(5, 3, "c is borrowed by this call, so the callback cannot also access e.n"),
		},
		{
			name: "invalidate-borrow-converted-op-inside",
			src: `func invalidate() {
	let mut e = Entry{n: 10}
	let c Cell = &e
	e.WithMut(func(v mut int) {
		c.Set(23)
		*v = 7
	})
}
`,
			want: at(5, 3, "e is borrowed by this call, so the callback cannot also access c"),
		},
		{
			name: "invalidate-with-context-borrow-provider",
			src: `func invalidate() {
	let mut e = Entry{n: 10}
	with context { Cell: &e } {
		e.WithMutR(func(v mut int) needs Cell {
			Cell.Set(23)
			*v = 7
		})
	}
}
`,
			want: at(5, 4, "e is borrowed by this call, so the callback cannot also access Cell.Set"),
		},
		{
			name: "invalidate-direct-call-local-frame",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	let cb = func(v mut int) {
		b.Free()
		*v = 7
	}
	cb(&b.n)
}
`,
			want: at(7, 2, "b.n is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-direct-call-inline-lit",
			src: `func invalidate() {
	let mut b = Box{n: 10}
	(func(v mut int) {
		b.Free()
		*v = 7
	})(&b.n)
}
`,
			want: at(4, 3, "b.n is borrowed by this call, so the callback cannot also access b"),
		},
		{
			name: "invalidate-callee-provided-cap",
			src: `func invalidate() {
	let mut e = Entry{n: 10}
	WithCell(&e, func() needs Cell {
		e.n = 23
		Cell.Set(7)
	})
}
`,
			want: at(4, 3, "e is borrowed by this call, so the callback cannot also access e.n"),
		},
		{
			name: "invalidate-callee-provided-cap-trailing",
			src: `func invalidate() {
	let mut e = Entry{n: 10}
	WithCell(&e) {
		e.n = 23
		Cell.Set(7)
	}
}
`,
			want: at(4, 3, "e is borrowed by this call, so the callback cannot also access e.n"),
		},
		{
			name: "invalidate-opaque-non-borrowing-closure-param",
			src: `func sneaky(b mut Box, side func()) {
	b.Update(func(v mut int) {
		side()
		*v = 7
	})
}
`,
			want: at(3, 3, "b is borrowed by this call, and side is a non-escaping closure that may also capture it"),
		},
		{
			name: "invalidate-aliased-non-borrowing-closure-param",
			src: `func sneaky(b mut Box, side func()) {
	let h = side
	b.Update(func(v mut int) {
		h()
		*v = 7
	})
}
`,
			want: at(4, 3, "b is borrowed by this call, and h is a non-escaping closure that may also capture it"),
		},
		{
			name: "invalidate-outer-frame-non-borrowing-closure-param",
			src: `func withSide(f func(), g func(side func())) {
	g(f)
}
func sneaky(b mut Box) {
	withSide(
		func() { b.Free() },
		func(side func()) {
			b.Update(func(v mut int) {
				side()
				*v = 7
			})
		},
	)
}
`,
			want: at(9, 5, "b is borrowed by this call, and side is a non-escaping closure that may also capture it"),
		},
		{
			name: "invalidate-prefix-slice-append",
			src: `func invalidate() {
	let mut xs = []Box{Box{n: 1}, Box{n: 2}}
	xs[0].Update(func(v mut int) {
		xs = xs.Append(Box{n: 3})
		*v = 7
	})
}
`,
			want: at(4, 3, "xs[0] is borrowed by this call, so the callback cannot also access xs"),
		},
		{
			name: "invalidate-unguarded-dynamic-index",
			src: `func invalidate(i int, j int) {
	let mut xs = []Box{Box{n: 1}, Box{n: 2}}
	xs[i].Update(func(v mut int) {
		xs[j].Free()
		*v = 7
	})
}
`,
			want: at(4, 3, "xs[i] is borrowed by this call and the callback may access the same place through xs[j], since i may equal j"),
		},
		{
			name: "valid-disjoint-root",
			src: `func ok() {
	let mut a = Box{n: 1}
	let mut b = Box{n: 2}
	a.Update(func(v mut int) {
		b.Free()
		*v = 7
	})
}
`,
			want: "",
		},
		{
			name: "valid-disjoint-fields",
			src: `func ok() {
	let mut p = Pair{a: Box{n: 1}, b: Box{n: 2}}
	p.a.Update(func(v mut int) {
		p.b.Free()
		*v = 7
	})
}
`,
			want: "",
		},
		{
			name: "valid-disjoint-const-indexes",
			src: `func ok() {
	let mut xs = []Box{Box{n: 1}, Box{n: 2}}
	xs[0].Update(func(v mut int) {
		xs[1].Free()
		*v = 7
	})
}
`,
			want: "",
		},
		{
			name: "valid-guarded-dynamic-indexes",
			src: `func ok(i int, j int) {
	let mut xs = []Box{Box{n: 1}, Box{n: 2}}
	if i != j {
		xs[i].Update(func(v mut int) {
			xs[j].Free()
			*v = 7
		})
	}
}
`,
			want: "",
		},
		{
			name: "valid-reentrant-non-borrowing-callback",
			src: `func ok(b mut Box) {
	b.Scope(func() {
		b.Free()
	})
}
`,
			want: "",
		},
		{
			name: "valid-borrowing-callback-relay",
			src: `func relay(b mut Box, edit func(v mut int)) {
	b.Update(func(v mut int) {
		edit(v)
	})
}
`,
			want: "",
		},
		{
			name: "valid-local-lender-with-param-closure",
			src: `func ok(side func()) {
	let mut b = Box{n: 1}
	b.Update(func(v mut int) {
		side()
		*v = 7
	})
}
`,
			want: "",
		},
		{
			name: "valid-local-frame-reused-on-disjoint-lender",
			src: `func ok() {
	let mut a = Box{n: 1}
	let mut b = Box{n: 2}
	let cb = func(v mut int) {
		a.Free()
		*v = 7
	}
	b.Update(cb)
}
`,
			want: "",
		},
		{
			name: "valid-distinct-local-inside-callback",
			src: `func ok(b mut Box) {
	b.Update(func(v mut int) {
		let mut other = Box{n: 99}
		other.Free()
		*v = 7
	})
}
`,
			want: "",
		},
		{
			name: "valid-arg-evaluated-before-call",
			src: `func UpdateAt(b mut Box, k int, f func(v mut int)) {
	let _ = k
	f(&b.n)
}
func ok(b mut Box) {
	b.UpdateAt(Read(*b), func(v mut int) {
		*v = 7
	})
}
`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := lenderPrelude + "\n" + tc.src
			_, diags := CheckSource(src)
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s@%d:%d %s", d.Code, d.Line, d.Column, d.Message))
			}
			joined := strings.Join(got, "\n")
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("unexpected diagnostics:\n%s\nsource:\n%s", joined, src)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(joined, tc.want) {
				t.Errorf("got (%d):\n%s\nwant substring:\n%s\nsource:\n%s", len(got), joined, tc.want, src)
			}
		})
	}
}

// TestBorrowCallbackExclusivityModel is a seeded property over generated
// lender + callback aliasing shapes compared against a reference model.
func TestBorrowCallbackExclusivityModel(t *testing.T) {
	seeds := make([]uint64, 0, 250)
	for s := range uint64(250) {
		seeds = append(seeds, s+1)
	}
	if *lenderSeed != 0 {
		seeds = []uint64{*lenderSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, want := lenderProgram(seed)
		_, diags := CheckSource(src)
		got := map[int]string{}
		for _, d := range diags {
			switch {
			case d.Code == "S108" && strings.Contains(d.Message, "may access the same place through"):
				got[d.Line] = "may"
			case d.Code == "S108" && strings.Contains(d.Message, "cannot also access"):
				got[d.Line] = "overlap"
			case d.Code == "S108" && strings.Contains(d.Message, "non-escaping closure that may also capture"):
				got[d.Line] = "closure"
			default:
				got[d.Line] = d.String()
			}
		}
		if !mapsEqual(got, want) {
			t.Errorf("seed %d: checker=%v reference=%v\nReplay: go test -run '^TestBorrowCallbackExclusivityModel$' ./internal/sketch/check -args -lender.seed=%d\n%s", seed, got, want, seed, src)
		}
		for _, v := range want {
			reached[v] = true
		}
		if len(want) == 0 {
			reached["admitted"] = true
		}
	}
	for _, v := range []string{"overlap", "may", "closure", "admitted"} {
		if *lenderSeed == 0 && !reached[v] {
			t.Errorf("no seed reached %q", v)
		}
	}
}

func lenderProgram(seed uint64) (string, map[int]string) {
	r := rand.New(rand.NewPCG(seed, 0x1e4de2))
	var b strings.Builder
	b.WriteString(lenderPrelude)
	line := strings.Count(lenderPrelude, "\n")
	emit := func(depth int, format string, args ...any) int {
		line++
		b.WriteString(strings.Repeat("\t", depth))
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\n")
		return line
	}
	want := map[int]string{}

	// Helper exercising parameter lender + non-borrowing vs borrowing closure param.
	emit(0, "")
	emit(0, "func paramCase(b mut Box, side func(), edit func(v mut int)) {")
	switch r.IntN(4) {
	case 0:
		emit(1, "b.Update(func(v mut int) {")
		at := emit(2, "side()")
		emit(2, "*v = 1")
		emit(1, "})")
		want[at] = "closure"
	case 1:
		emit(1, "b.Update(func(v mut int) {")
		emit(2, "edit(v)")
		emit(1, "})")
	case 2:
		emit(1, "b.Scope(func() {")
		emit(2, "side()")
		emit(2, "b.Free()")
		emit(1, "})")
	case 3:
		emit(1, "let mut local = Box{n: 1}")
		emit(1, "local.Update(func(v mut int) {")
		emit(2, "side()")
		emit(2, "*v = 1")
		emit(1, "})")
	}
	emit(0, "}")

	emit(0, "")
	emit(0, "func main() {")
	emit(1, "let mut xs = []Box{Box{n: 0}, Box{n: 0}, Box{n: 0}}")
	emit(1, "let mut p = Pair{a: Box{n: 0}, b: Box{n: 0}}")
	emit(1, "let mut q = Pair{a: Box{n: 0}, b: Box{n: 0}}")
	emit(1, "let i = len(xs) - 3")
	emit(1, "let j = len(xs) - 2")
	emit(1, "let mut k = 0")
	emit(1, "k = len(xs) - 1")
	emit(1, "xs[0].n = 1")
	emit(1, "p.a.n = 1")
	emit(1, "q.a.n = 1")

	for n := range 2 + r.IntN(4) {
		idx := strconv.Itoa(n)
		switch r.IntN(6) {
		case 0: // slice element vs slice element with optional guard
			x := borrowIndexes[r.IntN(len(borrowIndexes))]
			y := borrowIndexes[r.IntN(len(borrowIndexes))]
			g := borrowIndexes[r.IntN(len(borrowIndexes))]
			h := borrowIndexes[r.IntN(len(borrowIndexes))]
			var guard []string
			depth := 1
			if r.IntN(2) == 0 {
				emit(1, "if %s != %s {", g, h)
				guard = []string{g, h}
				depth = 2
			}
			useRead := r.IntN(2) == 0
			emit(depth, "xs[%s].Update(func(v mut int) {", x)
			var at int
			if useRead {
				at = emit(depth+1, "let _ = Read(xs[%s])", y)
			} else {
				at = emit(depth+1, "xs[%s].Free()", y)
			}
			emit(depth+1, "*v = 1")
			emit(depth, "})")
			if guard != nil {
				emit(1, "}")
			}
			switch borrowVerdict(x, y, guard) {
			case "one":
				want[at] = "overlap"
			case "may":
				want[at] = "may"
			}
		case 1: // struct field vs struct field
			fields := []string{"p.a", "p.b", "q.a"}
			f1, f2 := fields[r.IntN(3)], fields[r.IntN(3)]
			nested := r.IntN(2) == 0
			emit(1, "%s.Update(func(v mut int) {", f1)
			var at int
			if nested {
				emit(2, "Run(func() {")
				at = emit(3, "%s.Free()", f2)
				emit(2, "})")
			} else {
				at = emit(2, "%s.Free()", f2)
			}
			emit(2, "*v = 2")
			emit(1, "})")
			if f1 == f2 {
				want[at] = "overlap"
			}
		case 2: // whole struct vs field
			roots := []string{"p", "q"}
			r1, r2 := roots[r.IntN(2)], roots[r.IntN(2)]
			fld := []string{"a", "b"}[r.IntN(2)]
			emit(1, "UpdatePair(&%s, func(v mut int) {", r1)
			at := emit(2, "%s.%s.Free()", r2, fld)
			emit(2, "*v = 3")
			emit(1, "})")
			if r1 == r2 {
				want[at] = "overlap"
			}
		case 3: // local frame callback passed to Update
			fields := []string{"p.a", "p.b", "q.a"}
			f1, f2 := fields[r.IntN(3)], fields[r.IntN(3)]
			emit(1, "let cb%s = func(v mut int) {", idx)
			emit(2, "%s.Free()", f2)
			emit(2, "*v = 4")
			emit(1, "}")
			at := emit(1, "%s.Update(cb%s)", f1, idx)
			if f1 == f2 {
				want[at] = "overlap"
			}
		case 4: // prefix access (whole slice append) vs element lend
			x := borrowIndexes[r.IntN(len(borrowIndexes))]
			emit(1, "xs[%s].Update(func(v mut int) {", x)
			at := emit(2, "xs = xs.Append(Box{n: 9})")
			emit(2, "*v = 5")
			emit(1, "})")
			want[at] = "overlap"
		case 5: // re-entrant non-borrowing Scope always allowed
			fields := []string{"p.a", "p.b"}
			f1, f2 := fields[r.IntN(2)], fields[r.IntN(2)]
			emit(1, "%s.Scope(func() {", f1)
			emit(2, "%s.Free()", f2)
			emit(1, "})")
		}
	}
	emit(1, "let _ = (i, j, k, p.a.n, q.a.n, xs[0].n)")
	emit(0, "}")
	return b.String(), want
}
