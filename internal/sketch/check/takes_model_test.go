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
// inference and copy sites (takes.go). It generates a package's call
// graph: functions, exported (a written takes clause, chosen at random)
// and unexported (none: inferred), whose bodies lend a parameter, hand it
// off (to a function that takes it, through a function value whose type
// takes it, or on one path only), call another generated function (any,
// so recursion and mutual recursion), and name one as a value; and
// generic functions that move their T, copy it, or call another generic
// function, each called with a resource and with an int. A reference
// fixpoint in the test, written from plan 1.1 and 1.3, computes each
// inferred clause, each copy site and every S108 position (a use after a
// hand-off, a lent parameter handed off, a function value whose clause
// differs from the expected type's, a generic call that binds a copied T
// to a resource); they must equal the checker's. ERGO_SEED=<n> replays one
// seed; a failure prints the Ergo source and both results. A two-package
// case checks that the facts cross an import.
//
// Mutants it kills that no other test does (ledger: takes.go): inference
// misses a take two calls deep; mutual recursion stops after one round;
// an exported function that takes without the clause admitted; function
// types equal without the clause; a call through a function value does
// not move; a copy site not propagated through a generic callee; a copy
// site reported at a call with an int; a bodiless generic outside the
// table treated as moving.
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
		g := tkGen(rand.New(rand.NewPCG(seed, 0x197a3)))
		src, want, wantTakes := g.print(), g.reference(), g.inferred()
		c, diags := checkFor(src)
		var got []string
		for _, d := range diags {
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
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestTakesInferenceModel ./internal/sketch/check)\nsource:\n%s\nchecker diagnostics: %v\nmodel diagnostics:   %v\n%s\nchecker clauses and copies: %v\nmodel clauses and copies:   %v",
				seed, seed, ofNumbered(src), got, want, strings.Join(full, "\n"), gotTakes, wantTakes)
		}
	}
	tkTwoPackages(t)
}

const tkPrelude = `type Res { id int }

func Close(r Res) {}

func open(n int) Res { Res{id: n} }

func lend(r Res) {}

func keep(r Res) takes r {}

func cond(n int) bool { n > 2 }

func use(f func(n int, a Res, b Res, g func(r Res) takes r)) {}

func first[T](a T, b T) T takes a, b { a }

func keyed[K comparable](m Map[K, int], x K) K takes x { x }

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

// tkGeneric is h<i>[T](x T) T: body 0 moves x, 1 copies it
// (first(x, x)), 2 calls h<callee>(x), 3 keys a map by T (resources R40:
// S112 at a call with a resource).
type tkGeneric struct {
	body, callee int
	callLine     int // the line of `let v<i> = h<i>(open(…))`
}

type tkModel struct {
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
		m.generics = append(m.generics, &tkGeneric{body: r.IntN(4), callee: r.IntN(k)})
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
		clause := ""
		if f.exported {
			var names []string
			for x := range 2 {
				if f.written[x] {
					names = append(names, tkParam(x))
				}
			}
			if len(names) > 0 {
				clause = " takes " + strings.Join(names, ", ")
			}
		}
		emit(fmt.Sprintf("func %s(n int, a Res, b Res, g func(r Res) takes r)%s {", m.name(i), clause))
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
			body = "first(x, x)"
		case 2:
			body = fmt.Sprintf("h%d(x)", h.callee)
		default:
			body = "keyed(Map[T, int]{}, x)"
		}
		emit(fmt.Sprintf("func h%d[T comparable](x T) T { %s }", i, body))
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
	for changed := true; changed; {
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

// copies is the reference fixpoint of plan 1.3 over the generic functions;
// key marks a copy site that is a map key (S112 at the call, R40).
func (m *tkModel) copies() (out, key []bool) {
	out, key = make([]bool, len(m.generics)), make([]bool, len(m.generics))
	for changed := true; changed; {
		changed = false
		for i, h := range m.generics {
			c := h.body == 1 || h.body == 3 || h.body == 2 && out[h.callee]
			if c && !out[i] {
				out[i] = true
				key[i] = h.body == 3 || h.body == 2 && key[h.callee]
				changed = true
			}
		}
	}
	return out, key
}

// reference is every diagnostic's position and code: the flow of each
// function's body over its parameters' states, given the clauses; a
// function value whose clause is not empty (an inferred one is refused as
// a value, a written one is not use's parameter type); a generic call
// with a resource whose callee copies.
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
	cp, key := m.copies()
	for i, h := range m.generics {
		switch {
		case key[i]:
			at(h.callLine, 11, "S112")
		case cp[i]:
			at(h.callLine, 11, "S108")
		}
	}
	return out
}

// inferred is the reference's inferred clauses and copy sites, as text.
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
	cp, _ := m.copies()
	for i, c := range cp {
		if c {
			out = append(out, fmt.Sprintf("h%d copies T", i))
		}
	}
	// h<i> takes x when its body hands x off: it moves it, gives it to
	// first, or to a generic callee that takes it.
	takes := make([]bool, len(m.generics))
	for changed := true; changed; {
		changed = false
		for i, h := range m.generics {
			if !takes[i] && (h.body != 2 || takes[h.callee]) {
				takes[i] = true
				changed = true
			}
		}
	}
	for i, t := range takes {
		if t {
			out = append(out, fmt.Sprintf("h%d takes 0", i))
		}
	}
	slices.Sort(out)
	return out
}

// tkInferred is the checker's Info.Takes and Info.Copies, as text, for the
// generated functions (the prelude's first infers nothing: it writes).
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
	for f, cs := range c.pkg.Info.Copies {
		if f.Name == "first" || f.Name == "keyed" {
			continue
		}
		for _, s := range cs {
			out = append(out, fmt.Sprintf("%s copies %s", f.Name, f.Sig.TParams[s.TParam].Name))
		}
	}
	slices.Sort(out)
	return out
}

// tkTwoPackages checks that the facts cross an import: main hands a value
// to lib's exported Keep (written clause) and to lib's inferred keeper
// through Hold, and calls lib's generic Dup, which copies its T, with a
// resource (refused) and with an int (admitted); std slices.Clone, an extern
// generic outside runtime/v1's moving table, copies its T.
func tkTwoPackages(t *testing.T) {
	lib := `type Res { id int }

func Close(r Res) {}

func Open(n int) Res { Res{id: n} }

type Holder { r Res }

func Keep(r Res) Holder takes r { Holder{r: r} }

func hold(r Res) Holder { Keep(r) }

func Hold(r Res) Holder takes r { hold(r) }

func Dup[T](x T) (T, T) { (x, x) }

func Pass[T](x T) T takes x { x }
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
	if got := lp.Info.Takes; len(got) != 1 {
		t.Fatalf("lib: inferred clauses %v, want hold's only", got)
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
