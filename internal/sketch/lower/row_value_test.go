package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Row values as data (owner q47; rows.md, Error rows 8; types.md, Generics
// 1): an error row parameter in a field, `type Box[F] { Tag int, Last F }`,
// and a row value passed to a type parameter, `Exit[int, E0 | E1]`.
//
// TestRowValueFieldsAgree is a seeded property against a model. Each seed
// declares three or four error types in a shuffled row: one with no field
// (Go-comparable), one with an int (Go-comparable) and one with a slice
// (equatable, not Go-comparable), and maybe a fourth of a random kind. Its
// run program builds Box values two ways, by a member literal (assignment
// clause 7) and by a `case err:` binding of a failure, and Exit values
// through a generic exitOf over `fails F`; it matches each (code, total
// over the row) and compares pairs (==, on the record, on the row value
// field and on the Exit union). Its test build has one property per member,
// "Last is never Ej", that the generator must refute, and the refuting
// literal must name that member and check as Ergo source.
//
// A Go binary runs because the claims are about the lowering: the Go
// error's type switch, Go's == on an error against the generated union
// equality, and the generator and literal functions. go/types alone cannot
// tell which member a value holds at run time.
//
// Replay: go test -run '^TestRowValueFieldsAgree$' ./internal/sketch/lower -args -rv.seed=N
var rvSeed = flag.Uint64("rv.seed", 0, "run TestRowValueFieldsAgree on this seed alone")

const rvCases = 40

// rvVal is a model value of the row: member m with payload n (the int, or
// the slice's length; 0 for a member with no field).
type rvVal struct{ m, n int }

// rvShape is one seed's row: kinds[i] is member Ei's kind, 0 no field, 1
// an int, 2 a slice; order is the row as written.
type rvShape struct {
	kinds []int
	order []int
}

func (s rvShape) row() string {
	parts := make([]string, len(s.order))
	for i, m := range s.order {
		parts[i] = fmt.Sprintf("E%d", m)
	}
	return strings.Join(parts, " | ")
}

func (s rvShape) lit(v rvVal) string {
	switch s.kinds[v.m] {
	case 0:
		return fmt.Sprintf("E%d", v.m)
	case 1:
		return fmt.Sprintf("E%d{N: %d}", v.m, v.n)
	}
	items := make([]string, v.n)
	for i := range items {
		items[i] = "7"
	}
	return fmt.Sprintf("E%d{Items: []int{%s}}", v.m, strings.Join(items, ", "))
}

func (s rvShape) code(v rvVal) int { return 100*v.m + v.n }

func (s rvShape) draw(r *rand.Rand) rvVal {
	m := r.IntN(len(s.kinds))
	if s.kinds[m] == 0 {
		return rvVal{m, 0}
	}
	return rvVal{m, r.IntN(3)}
}

func (s rvShape) decls() string {
	var b strings.Builder
	row := s.row()
	for m, k := range s.kinds {
		switch k {
		case 0:
			fmt.Fprintf(&b, "type E%d {}\n", m)
		case 1:
			fmt.Fprintf(&b, "type E%d { N int }\n", m)
		default:
			fmt.Fprintf(&b, "type E%d { Items []int }\n", m)
		}
	}
	b.WriteString("\ntype Box[F] { Tag int, Last F }\n\ntype Exit[T, F] = Done{Value T} | Failed{Error F}\n\n")
	fmt.Fprintf(&b, "func code(b Box[%s]) int {\n\tmatch b.Last {\n", row)
	for _, m := range s.order {
		switch s.kinds[m] {
		case 0:
			fmt.Fprintf(&b, "\tcase E%d:\n\t\t%d\n", m, 100*m)
		case 1:
			fmt.Fprintf(&b, "\tcase E%d as e:\n\t\t%d + e.N\n", m, 100*m)
		default:
			fmt.Fprintf(&b, "\tcase E%d as e:\n\t\t%d + len(e.Items)\n", m, 100*m)
		}
	}
	b.WriteString("\t}\n}\n\n")
	fmt.Fprintf(&b, "func member(b Box[%s]) int {\n\tmatch b.Last {\n", row)
	for _, m := range s.order {
		fmt.Fprintf(&b, "\tcase E%d:\n\t\t%d\n", m, m)
	}
	b.WriteString("\t}\n}\n\n")
	// fail fails with member m, payload n, or returns n when m is past the row.
	fmt.Fprintf(&b, "func fail(m int, n int) int fails %s {\n", row)
	for m, k := range s.kinds {
		fmt.Fprintf(&b, "\tif m == %d {\n", m)
		switch k {
		case 0:
			fmt.Fprintf(&b, "\t\treturn E%d\n", m)
		case 1:
			fmt.Fprintf(&b, "\t\treturn E%d{N: n}\n", m)
		default:
			fmt.Fprintf(&b, "\t\tlet mut xs = []int{}\n\t\tfor _ in 0..n {\n\t\t\txs = xs.Append(7)\n\t\t}\n\t\treturn E%d{Items: xs}\n", m)
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("\tn\n}\n\n")
	return b.String()
}

func (s rvShape) runProgram(r *rand.Rand, cases *[]rvCase) string {
	row := s.row()
	var b strings.Builder
	b.WriteString(s.decls())
	b.WriteString(`func exitOf[T, F](body func() T fails F) Exit[T, F] {
	let v = body() catch {
	case err:
		return Failed{Error: err}
	}
	Done{Value: v}
}

`)
	fmt.Fprintf(&b, `func caught(m int, n int) Box[%[1]s] {
	let v = fail(m, n) catch {
	case err:
		return Box[%[1]s]{Tag: 1, Last: err}
	}
	Unreachable("fail always fails here: \(v)")
}

func exitCode(x Exit[int, %[1]s]) int {
	match x {
	case Done[int] as d:
		-1 - d.Value
	case Failed[%[1]s] as f:
		code(Box[%[1]s]{Tag: 0, Last: f.Error})
	}
}

func check(i int, a Box[%[1]s], b Box[%[1]s], x Exit[int, %[1]s], y Exit[int, %[1]s]) string {
	"\(i) \(code(a)) \(code(b)) \(a == b) \(a.Last == b.Last) \(x == y) \(x != y) \(exitCode(x))"
}

func main() needs Console {
`, row)
	box := func(c *rvBox) string {
		if c.caught {
			return fmt.Sprintf("caught(%d, %d)", c.v.m, c.v.n)
		}
		return fmt.Sprintf("Box[%s]{Tag: %d, Last: %s}", row, c.tag, s.lit(c.v))
	}
	exit := func(e rvExit) string {
		m := e.v.m
		if e.done {
			m = len(s.kinds)
		}
		return fmt.Sprintf("exitOf(func() int fails %s { fail(%d, %d) })", row, m, e.v.n)
	}
	for i := range rvCases {
		var c rvCase
		c.a = s.box(r)
		if r.IntN(2) == 0 {
			c.b = c.a
			c.b.caught = r.IntN(2) == 0 // the same value, built the other way
			if c.b.caught {
				c.b.tag = 1
			}
		} else {
			c.b = s.box(r)
		}
		c.x = s.exit(r)
		if r.IntN(2) == 0 {
			c.y = c.x
		} else {
			c.y = s.exit(r)
		}
		*cases = append(*cases, c)
		fmt.Fprintf(&b, "\tConsole.Println(check(%d, %s, %s, %s, %s))\n", i, box(&c.a), box(&c.b), exit(c.x), exit(c.y))
	}
	b.WriteString("}\n")
	return b.String()
}

type rvBox struct {
	tag    int
	v      rvVal
	caught bool
}

type rvExit struct {
	done bool
	v    rvVal // the Done value is v.n
}

type rvCase struct {
	a, b rvBox
	x, y rvExit
}

func (s rvShape) box(r *rand.Rand) rvBox {
	c := rvBox{v: s.draw(r), caught: r.IntN(2) == 0}
	if c.caught {
		c.tag = 1
	} else {
		c.tag = r.IntN(2)
	}
	return c
}

func (s rvShape) exit(r *rand.Rand) rvExit {
	if r.IntN(3) == 0 {
		return rvExit{done: true, v: rvVal{0, r.IntN(3)}}
	}
	return rvExit{v: s.draw(r)}
}

// want is the model's line for case i.
func (s rvShape) want(i int, c rvCase) string {
	ab := c.a.tag == c.b.tag && c.a.v == c.b.v
	xy := c.x == c.y
	xc := -1 - c.x.v.n
	if !c.x.done {
		xc = s.code(c.x.v)
	}
	return fmt.Sprintf("%d %d %d %v %v %v %v %d", i, s.code(c.a.v), s.code(c.b.v), ab, c.a.v == c.b.v, xy, !xy, xc)
}

func newShape(r *rand.Rand) rvShape {
	kinds := []int{0, 1, 2}
	if r.IntN(2) == 0 {
		kinds = append(kinds, r.IntN(3))
	}
	r.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })
	order := r.Perm(len(kinds))
	return rvShape{kinds: kinds, order: order}
}

func (s rvShape) testProgram() string {
	var b strings.Builder
	b.WriteString(s.decls())
	b.WriteString("func main() {}\n\n")
	for m := range s.kinds {
		fmt.Fprintf(&b, "test Never%d(b Box[%s]) {\n\tassert member(b) != %d\n}\n\n", m, s.row(), m)
	}
	return b.String()
}

// TestRowValueFieldsAgree is the seeded property of row values in fields
// (q47): each seed's run program prints its cases' codes and comparisons,
// which match the model's lines, and its property tests over Box[row]
// draw every member. It builds Go because the values under test are the
// lowered program's (Go error values in fields, their ==, and the
// generators of the built test binary); Ergo has no reference interpreter.
func TestRowValueFieldsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if *rvSeed != 0 {
		seeds = []uint64{*rvSeed}
	}
	shapes := map[uint64]rvShape{}
	cases := map[uint64][]rvCase{}
	var programs []*lowered
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7276))
		s := newShape(r)
		shapes[seed] = s
		var cs []rvCase
		programs = append(programs,
			mustLower(t, fmt.Sprintf("rvrun%d", seed), s.runProgram(r, &cs)),
			lowerTestBuild(t, fmt.Sprintf("rvtest%d", seed), s.testProgram()))
		cases[seed] = cs
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the row value programs do not build (seeds %v)", seeds)
	}
	reached := map[string]bool{}
	refuted := regexp.MustCompile(`^FAIL Never(\d+) \(case \d+ of 100, seed \d+, size \d+: b = (.*?)(?:; shrunk from: .*)?\): assert failed`)
	for k, seed := range seeds {
		s := shapes[seed]
		replay := fmt.Sprintf("Replay: go test -run '^TestRowValueFieldsAgree$' ./internal/sketch/lower -args -rv.seed=%d", seed)
		run := programs[2*k]
		stdout, stderr, code := runExe(t, exes[run.name])
		if code != 0 {
			t.Fatalf("seed %d: exit %d\n%s\n%s\n%s", seed, code, firstLines(stderr, 8), replay, run.src)
		}
		got := lines(stdout)
		if len(got) != rvCases {
			t.Fatalf("seed %d: %d lines\n%s", seed, len(got), stdout)
		}
		for i, c := range cases[seed] {
			if want := s.want(i, c); got[i] != want {
				t.Errorf("seed %d, case %d: got %q, want %q (i code(a) code(b) a==b a.Last==b.Last x==y x!=y exitCode(x))\n%s\n%s", seed, i, got[i], want, replay, run.src)
			}
			reached[fmt.Sprintf("kind %d", s.kinds[c.a.v.m])] = true
			reached[fmt.Sprintf("caught %v", c.a.caught)] = true
			reached[fmt.Sprintf("equal %v", c.a.tag == c.b.tag && c.a.v == c.b.v)] = true
			reached[fmt.Sprintf("done %v", c.x.done)] = true
		}
		// Every member is generated, and its literal names it and checks.
		tests := programs[2*k+1]
		stdout, stderr, _ = runExeArgs(t, exes[tests.name], "--seed", "1")
		lits := map[int]string{}
		for _, l := range lines(stdout) {
			if m := refuted.FindStringSubmatch(l); m != nil {
				var j int
				fmt.Sscan(m[1], &j)
				lits[j] = m[2]
			}
		}
		var src strings.Builder
		src.WriteString(s.decls())
		for m := range s.kinds {
			lit, ok := lits[m]
			if !ok {
				t.Errorf("seed %d: Never%d held for 100 cases: the generator never drew E%d\n%s\n%s", seed, m, m, stdout, firstLines(stderr, 4))
				continue
			}
			if !strings.HasPrefix(lit, "Box["+s.row()+"]{") || !strings.Contains(lit, fmt.Sprintf("Last: E%d", m)) {
				t.Errorf("seed %d: Never%d's argument %s does not name Box[%s] and E%d", seed, m, lit, s.row(), m)
			}
			fmt.Fprintf(&src, "func lit%d() Box[%s] { %s }\n", m, s.row(), lit)
		}
		f, err := syntax.Parse(src.String())
		if err != nil {
			t.Fatalf("seed %d: the printed literals do not parse: %v\n%s", seed, err, src.String())
		}
		if _, ds := check.Check("main", []*syntax.File{f}, nil); len(ds) > 0 {
			t.Errorf("seed %d: the printed literals do not check: %v\n%s", seed, ds, src.String())
		}
	}
	if *rvSeed == 0 {
		for _, k := range []string{"kind 0", "kind 1", "kind 2", "caught true", "caught false", "equal true", "equal false", "done true", "done false"} {
			if !reached[k] {
				t.Errorf("the fixed seeds never reach %s (reached %v)", k, reached)
			}
		}
	}
}
