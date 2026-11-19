package check

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTakesInferenceModel (#197 L8 A3) is a seeded property of takes
// inference (takes.go) and of the own mark (types.md, Generics 9; EXM2).
// It generates a package's call graph: functions, exported (a written
// takes clause, chosen at random) and unexported (none: inferred), whose
// bodies lend a parameter, hand it off (to a function that takes it,
// through a function value whose type takes it, or on one path only),
// call another generated function (any, so recursion and mutual
// recursion), and name one as a value; and generic functions, each marked
// [T own] or not, that move their T, copy it, call another generic
// function, or key a map by it, each called with a resource and with an
// int. A reference in the test, written from plan 1.1 and Generics 9,
// computes each inferred clause and every diagnostic: a use after a
// hand-off, a lent parameter handed off, a function value whose clause
// differs from the expected type's; a marked body that copies its T, binds
// it to an unmarked callee, or keys a map by it (S112); a call that binds
// a resource to an unmarked T. They must equal the checker's (a generic
// body's line by its codes, the rest by position). ERGO_SEED=<n> replays
// one seed; a failure prints the Ergo source and both results. A
// two-package case checks that the facts cross an import. Each seed runs
// twice: under ergo fix's clause writer (Options.Infer), the only place
// the inference lives (q137, EX3), against the fixpoint; and in the
// default mode against the model with no inferred clause, where an
// unexported function's hand-off of a parameter is S108.
//
// Mutants it kills that no other test does (ledgers: takes.go,
// resource.go): inference misses a take two calls deep; mutual recursion
// stops after one round; an exported function that takes without the
// clause admitted; function types equal without the clause; a call
// through a function value does not move; a marked body that copies is
// accepted (modes5-own-copy-admitted); a call through a generic callee does
// not propagate the mark (modes5-own-arg-unchecked); an own map key
// admitted (modes5-own-key-admitted); a resource binds an unmarked T
// (modes5-unmarked-admits-resource).
func TestTakesInferenceModel(t *testing.T) {
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
		for _, explicit := range []bool{false, true} {
			tkSeed(t, seed, explicit)
		}
	}
	tkTwoPackages(t)
}

// tkSeed checks one seed: under ergo fix's clause writer (Options.Infer)
// against the inference model, and in the default mode (explicit) against
// the model in which every clause is the written one (q137, EX3).
func tkSeed(t *testing.T, seed uint64, explicit bool) {
	t.Helper()
	{
		g := tkGen(rand.New(rand.NewPCG(seed, 0x197a3)))
		g.explicit = explicit
		src, want, wantTakes := g.print(), g.reference(), g.inferred()
		check := checkForInfer
		if explicit {
			check = checkFor
		}
		c, diags := check(src)
		hLines := map[int]bool{}
		for _, h := range g.generics {
			hLines[h.line] = true
		}
		var got []string
		for _, d := range diags {
			if hLines[d.Line] {
				// A generic body's line, by its codes (ownCodes).
				if k := fmt.Sprintf("L%d %s", d.Line, d.Code); !slices.Contains(got, k) {
					got = append(got, k)
				}
				continue
			}
			got = append(got, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Code))
		}
		gotTakes := tkInferred(c)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) || !slices.Equal(gotTakes, wantTakes) {
			var full []string
			for _, d := range diags {
				full = append(full, d.String())
			}
			t.Fatalf("seed %d, explicit %v (replay: ERGO_SEED=%d go test -run TestTakesInferenceModel ./internal/sketch/check)\nsource:\n%s\nchecker diagnostics: %v\nmodel diagnostics:   %v\n%s\nchecker clauses: %v\nmodel clauses:   %v",
				seed, explicit, seed, ofNumbered(src), got, want, strings.Join(full, "\n"), gotTakes, wantTakes)
		}
	}
}

const tkPrelude = `type Res { id int } implements Closer

func Close(r Res) {}

func open(n int) Res { Res{id: n} }

func lend(r Res) {}

func keep(r own Res) {}

func cond(n int) bool { n > 2 }

func use(f func(n int, a Res, b Res, g func(r own Res))) {}

func keyed[K comparable](m Map[K, int], x own K) K { x }

`

// tkStmt kinds of a generated function's body.
const (
	tkLend   = iota // lend(X)
	tkKeep          // keep(X)
	tkValue         // g(X): through the function value g, which takes r
	tkIfKeep        // if cond(n) { keep(X) }
	tkCall          // fJ(n, X, open(k), g) or fJ(n, open(k), X, g)
	tkUse           // use(fJ)
)

type tkStmt struct {
	kind, x, callee int // x: 0 a, 1 b; callee: a function's index
	xFirst          bool
	line, col       int // the position of X, or of fJ for tkUse
}

type tkFunc struct {
	exported bool
	written  [2]bool // exported: the written clause over a, b
	body     []*tkStmt
}

// tkGeneric is h<i>[T own?](x own T) T: body 0 moves x, 1 copies it
// (let (a, _) = (x, x)), 2 calls h<callee>(x), 3 keys a map by T through
// keyed, whose K is unmarked.
type tkGeneric struct {
	body, callee int
	marked       bool
	line         int // the line of h<i>'s declaration
	callLine     int // the line of `let v<i> = h<i>(open(…))`
}

type tkModel struct {
	// explicit is the default mode (q137): no clause is inferred, so an
	// unexported function's clause is its written one, none.
	explicit bool
	r        *rand.Rand
	funcs    []*tkFunc
	generics []*tkGeneric
}

func tkGen(r *rand.Rand) *tkModel {
	m := &tkModel{r: r}
	n := 2 + r.IntN(5)
	for range n {
		f := &tkFunc{exported: r.IntN(3) == 0}
		if f.exported {
			f.written = [2]bool{r.IntN(2) == 0, r.IntN(2) == 0}
		}
		for range 1 + r.IntN(4) {
			s := &tkStmt{kind: r.IntN(6), x: r.IntN(2), callee: r.IntN(n), xFirst: r.IntN(2) == 0}
			f.body = append(f.body, s)
		}
		m.funcs = append(m.funcs, f)
	}
	k := 1 + r.IntN(4)
	for range k {
		m.generics = append(m.generics, &tkGeneric{body: r.IntN(4), callee: r.IntN(k), marked: r.IntN(2) == 0})
	}
	return m
}

func (m *tkModel) name(i int) string {
	if m.funcs[i].exported {
		return fmt.Sprintf("F%d", i)
	}
	return fmt.Sprintf("f%d", i)
}

func tkParam(x int) string { return [2]string{"a", "b"}[x] }

func (m *tkModel) print() string {
	var b strings.Builder
	b.WriteString(tkPrelude)
	line := strings.Count(tkPrelude, "\n") + 1
	emit := func(s string) {
		b.WriteString(s)
		b.WriteString("\n")
		line++
	}
	for i, f := range m.funcs {
		mode := [2]string{"", ""}
		if f.exported {
			for x := range 2 {
				if f.written[x] {
					mode[x] = "own "
				}
			}
		}
		emit(fmt.Sprintf("func %s(n int, a %sRes, b %sRes, g func(r own Res)) {", m.name(i), mode[0], mode[1]))
		for k, s := range f.body {
			s.line = line
			x := tkParam(s.x)
			var text, prefix string
			switch s.kind {
			case tkLend:
				prefix, text = "lend(", "lend("+x+")"
			case tkKeep:
				prefix, text = "keep(", "keep("+x+")"
			case tkValue:
				prefix, text = "g(", "g("+x+")"
			case tkIfKeep:
				prefix, text = "if cond(n) { keep(", "if cond(n) { keep("+x+") }"
			case tkCall:
				callee := m.name(s.callee)
				if s.xFirst {
					prefix = callee + "(n, "
					text = fmt.Sprintf("%s%s, open(%d), g)", prefix, x, k)
				} else {
					prefix = fmt.Sprintf("%s(n, open(%d), ", callee, k)
					text = fmt.Sprintf("%s%s, g)", prefix, x)
				}
			case tkUse:
				prefix, text = "use(", "use("+m.name(s.callee)+")"
			}
			s.col = 1 + len(prefix) + 1
			emit("\t" + text)
		}
		emit("}")
		emit("")
	}
	for i, h := range m.generics {
		var body string
		switch h.body {
		case 0:
			body = "x"
		case 1:
			body = "let (a, _) = (x, x); a"
		case 2:
			body = fmt.Sprintf("h%d(x)", h.callee)
		default:
			body = "keyed(Map[T, int]{}, x)"
		}
		mark := ""
		if h.marked {
			mark = " own"
		}
		h.line = line
		emit(fmt.Sprintf("func h%d[T%s comparable](x own T) T { %s }", i, mark, body))
		emit("")
	}
	emit("func callers() {")
	for i, h := range m.generics {
		h.callLine = line
		emit(fmt.Sprintf("\tlet v%d = h%d(open(%d))", i, i, i))
		emit(fmt.Sprintf("\tlet _ = h%d(%d)", i, i))
	}
	emit("}")
	emit("")
	emit("func main() {}")
	return b.String()
}

// clauses is the reference fixpoint of plan 1.1: an exported function's
// clause is written; an unexported one takes a parameter when some
// statement hands it off (keep, the function value g, if-keep, or a call
// of a function whose clause takes that position), until nothing changes.
func (m *tkModel) clauses() [][2]bool {
	out := make([][2]bool, len(m.funcs))
	for i, f := range m.funcs {
		if f.exported {
			out[i] = f.written
		}
	}
	for changed := !m.explicit; changed; {
		changed = false
		for i, f := range m.funcs {
			if f.exported {
				continue
			}
			for _, s := range f.body {
				if m.handsOff(s, out) && !out[i][s.x] {
					out[i][s.x] = true
					changed = true
				}
			}
		}
	}
	return out
}

func (m *tkModel) handsOff(s *tkStmt, cl [][2]bool) bool {
	switch s.kind {
	case tkKeep, tkValue, tkIfKeep:
		return true
	case tkCall:
		pos := 1 // the callee's b
		if s.xFirst {
			pos = 0
		}
		return cl[s.callee][pos]
	}
	return false
}

// ownCodes is the reference of Generics 9 for h<i>'s body: the codes on
// its line. An unmarked T is plain data; under the mark a copy is S108, a
// call of an unmarked callee is S108, and a map key is S112 with S108 for
// keyed's unmarked K (and for the map it drops).
func (m *tkModel) ownCodes(h *tkGeneric) []string {
	if !h.marked {
		return nil
	}
	switch h.body {
	case 1:
		return []string{"S108"}
	case 2:
		if !m.generics[h.callee].marked {
			return []string{"S108"}
		}
	case 3:
		return []string{"S108", "S112"}
	}
	return nil
}

// reference is every diagnostic's position and code: the flow of each
// function's body over its parameters' states, given the clauses; a
// function value whose clause is not empty (an inferred one is refused as
// a value, a written one is not use's parameter type); a generic body's
// codes on its line (ownCodes); a call with a resource whose callee's T is
// unmarked.
func (m *tkModel) reference() []string {
	cl := m.clauses()
	var out []string
	at := func(line, col int, code string) {
		out = append(out, fmt.Sprintf("%d:%d %s", line, col, code))
	}
	const owned, moved, maybe, lent = 0, 1, 2, 3
	for i, f := range m.funcs {
		var st [2]int
		for x := range 2 {
			if !cl[i][x] {
				st[x] = lent
			}
		}
		for _, s := range f.body {
			x := s.x
			switch {
			case s.kind == tkUse:
				if cl[s.callee] != [2]bool{} {
					at(s.line, s.col, "S108")
				}
			case m.handsOff(s, cl):
				switch st[x] {
				case moved, maybe:
					at(s.line, s.col, "S108")
					st[x] = moved
				case lent:
					at(s.line, s.col, "S108")
				default:
					if s.kind == tkIfKeep {
						st[x] = maybe
					} else {
						st[x] = moved
					}
				}
			default: // a lend
				if st[x] == moved || st[x] == maybe {
					at(s.line, s.col, "S108")
				}
			}
		}
	}
	for _, h := range m.generics {
		for _, code := range m.ownCodes(h) {
			out = append(out, fmt.Sprintf("L%d %s", h.line, code))
		}
		if !h.marked {
			at(h.callLine, 11, "S108")
		}
	}
	return out
}

// inferred is the reference's inferred clauses, as text.
func (m *tkModel) inferred() []string {
	cl := m.clauses()
	var out []string
	for i, f := range m.funcs {
		if f.exported || cl[i] == [2]bool{} {
			continue
		}
		var ix []string
		for x := range 2 {
			if cl[i][x] {
				ix = append(ix, strconv.Itoa(x+1))
			}
		}
		out = append(out, m.name(i)+" takes "+strings.Join(ix, ","))
	}
	slices.Sort(out)
	return out
}

// tkInferred is the checker's Info.Takes, as text, for the generated
// functions (h<i> writes own on x: it infers nothing).
func tkInferred(c *checker) []string {
	if c == nil {
		return nil
	}
	var out []string
	for f, ix := range c.pkg.Info.Takes {
		var s []string
		for _, i := range ix {
			s = append(s, strconv.Itoa(i))
		}
		out = append(out, f.Name+" takes "+strings.Join(s, ","))
	}
	slices.Sort(out)
	return out
}

// tkTwoPackages checks that the facts cross an import: main hands a value
// to lib's exported Keep and to lib's keeper hold through Hold (written
// clauses); calls lib's generic Dup, whose T is unmarked, with a resource
// (refused) and with an int (admitted), and lib's Pass, marked [T own],
// with a resource (admitted); std slices.Clone writes no mark.
func tkTwoPackages(t *testing.T) {
	lib := `type Res { id int } implements Closer

func Close(r Res) {}

func Open(n int) Res { Res{id: n} }

type Holder { r Res }

func Keep(r own Res) Holder { Holder{r: r} }

func hold(r own Res) Holder { Keep(r) }

func Hold(r own Res) Holder { hold(r) }

func Dup[T](x T) (T, T) { (x, x) }

func Pass[T own](x own T) T { x }
`
	main := `import lib
import slices

func main() {
	let a = lib.Open(1)
	let h = lib.Keep(a)
	a.Close()
	let b = lib.Open(2)
	let k = lib.Hold(b)
	b.Close()
	let (x, y) = lib.Dup(lib.Open(3))
	let (i, j) = lib.Dup(1)
	let p = lib.Pass(lib.Open(4))
	let q = slices.Clone([]lib.Res{lib.Open(5)})
	let r = slices.Clone([]int{1})
}
`
	lf, err := syntax.Parse(lib)
	if err != nil {
		t.Fatal(err)
	}
	lp, ds := Check("lib", []*syntax.File{lf}, Std)
	if len(ds) != 0 {
		t.Fatalf("lib: %v", ds)
	}
	if got := lp.Info.Takes; len(got) != 0 {
		t.Fatalf("lib: inferred clauses %v, want none (q137)", got)
	}
	mf, err := syntax.Parse(main)
	if err != nil {
		t.Fatal(err)
	}
	_, ds = Check("main", []*syntax.File{mf}, canonImporter{"lib": lp})
	var got []string
	for _, d := range ds {
		if d.Code != CodeUnused {
			got = append(got, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Code))
		}
	}
	want := []string{"7:2 S108", "10:2 S108", "11:15 S108", "14:10 S108"}
	if !slices.Equal(got, want) {
		var full []string
		for _, d := range ds {
			full = append(full, d.String())
		}
		t.Fatalf("two packages: got %v, want %v\n%s", got, want, strings.Join(full, "\n"))
	}
}
