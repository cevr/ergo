package syntax_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/syntax"
)

func TestFactClausesParseAndFormatRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want func(*testing.T, *syntax.File, string)
	}{
		{
			name: "function with given and gives",
			src: `func At[T](xs []T, i int) T given InRange(xs, i) gives it == xs[i] {
	xs[i]
}
`,
			want: func(t *testing.T, f *syntax.File, src string) {
				fn := f.Decls[0].(*syntax.FuncDecl)
				if len(fn.Type.Facts) != 2 {
					t.Fatalf("got %d fact clauses, want 2", len(fn.Type.Facts))
				}
				c0, c1 := fn.Type.Facts[0], fn.Type.Facts[1]
				if c0.Kind != syntax.FactGiven || c1.Kind != syntax.FactGives {
					t.Fatalf("kinds = (%d, %d), want (%d, %d)", c0.Kind, c1.Kind, syntax.FactGiven, syntax.FactGives)
				}
				if got := src[c0.Pos():c0.End()]; got != "given InRange(xs, i)" {
					t.Fatalf("c0 span = %q", got)
				}
				if got := src[c1.Pos():c1.End()]; got != "gives it == xs[i]" {
					t.Fatalf("c1 span = %q", got)
				}
			},
		},
		{
			name: "keeps with and without when and defects before given",
			src: `func Free[T](a mut Of[T], r Ref[T]) T fails Stale defects given Valid(*a) keeps Live(*a, x) when x != r {
	try pop(a, r)
}

func Update[T](a mut Of[T], r Ref[T], f func(v mut T)) fails Stale keeps Live(*a, _) {
	try apply(a, r, f)
}
`,
			want: func(t *testing.T, f *syntax.File, src string) {
				fn0 := f.Decls[0].(*syntax.FuncDecl)
				if !fn0.Type.Defects || len(fn0.Type.Facts) != 2 {
					t.Fatalf("fn0 Defects=%v Facts=%d", fn0.Type.Defects, len(fn0.Type.Facts))
				}
				keeps := fn0.Type.Facts[1]
				if keeps.Kind != syntax.FactKeeps || keeps.When == nil {
					t.Fatalf("expected keeps with when guard, got kind=%d when=%v", keeps.Kind, keeps.When)
				}
				if got := src[keeps.Pos():keeps.End()]; got != "keeps Live(*a, x) when x != r" {
					t.Fatalf("keeps span = %q", got)
				}
				fn1 := f.Decls[1].(*syntax.FuncDecl)
				if len(fn1.Type.Facts) != 1 || fn1.Type.Facts[0].When != nil {
					t.Fatalf("expected single keeps without when, got %+v", fn1.Type.Facts)
				}
			},
		},
		{
			name: "extern operation type function literal function types and implements",
			src: `extern go strings.ToUpper func Upper(s string) string given len(s) >= 0 gives len(it) == len(s)

type Queue {
	Pop() int fails Empty given Len() > 0,
	Push(v int) gives Len() > 0,
}

type Filter func(x int) bool given x >= 0

type Holder {
	Check func(a int) int given a > 0,
}

func use(f func(int) given _ >= 0) func() gives true {
	let g = func(x int) int given x > 0 gives it > 0 { x }
	_ = g
	func() gives true {}
}

func Impl(a int) int keeps Valid(a) when a > 0 implements Marker {
	a
}
`,
		},
		{
			name: "composite literal in clause and contextual words inside expr",
			src: `func MatchPoint(p Point, given int, when bool) bool given p == (Point{X: 1}) keeps when && given > 0 {
	when && given > 0
}
`,
		},
		{
			name: "record where invariants derive nothing",
			src: `type Empty {} where true

type Ascending[T ordered] {
	xs []T,
} where Ordered(it.xs)

type Checked {
	v int,
} where it.v > 0 implements Sized

type Port int where 1 <= it && it <= 65535
`,
			want: func(t *testing.T, f *syntax.File, _ string) {
				// Three record types + one defined type (Port) + two derived (ParsePort, PortWhere).
				if len(f.Decls) != 6 {
					t.Fatalf("got %d decls, want 6 (records must not derive Parse/Where)", len(f.Decls))
				}
				for i := range 3 {
					td := f.Decls[i].(*syntax.TypeDecl)
					if _, ok := td.Spec.(*syntax.StructType); !ok || td.Where == nil {
						t.Fatalf("decl %d is not a record with where", i)
					}
				}
				if syntax.IsDerived(f.Decls[0]) || syntax.IsDerived(f.Decls[1]) || syntax.IsDerived(f.Decls[2]) {
					t.Fatal("record decl unexpectedly marked derived")
				}
				if !syntax.IsDerived(f.Decls[4]) || !syntax.IsDerived(f.Decls[5]) {
					t.Fatal("defined type Port must still derive ParsePort and PortWhere")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.Parse(tc.src)
			if err != nil {
				t.Fatalf("Parse failed: %v\n%s", err, tc.src)
			}
			if tc.want != nil {
				tc.want(t, f, tc.src)
			}
			out, err := format.Source(tc.src)
			if err != nil {
				t.Fatalf("format.Source failed: %v", err)
			}
			if out != tc.src {
				t.Fatalf("not canonical:\n--- want\n%s--- got\n%s", tc.src, out)
			}
			again, err := format.Source(out)
			if err != nil {
				t.Fatalf("second format.Source failed: %v", err)
			}
			if again != out {
				t.Fatalf("not idempotent:\n%s", again)
			}
		})
	}
}

func TestFactWordsStayIdentifiersEverywhereElse(t *testing.T) {
	repl := [][2]string{
		{"given", "gavex"},
		{"gives", "givex"},
		{"keeps", "keexs"},
	}
	places := []string{
		"func $(x int) int { x }\n",
		"func f($ int, x $) int { $ }\n",
		"func ret() $ { ${} }\n",
		"func retFail() $ fails E { ${} }\n",
		"func retNeed() $ needs C { ${} }\n",
		"func retTake(a own $ ) $ { a }\n",
		"func retDef() $ defects { ${} }\n",
		"func retImpl() $ implements Contract { ${} }\n",
		"func retClause() $ @CLAUSE true { ${} }\n",
		"func retGeneric() $[int] { $[int]{} }\n",
		"func retQual() $.T { $.T{} }\n",
		"func retParam(f func() $, x int) {}\n",
		"type $ { $ int }\n",
		"type Op { $() $, Next() }\n",
		"let $ = 1\n",
		"func body() int {\n\tlet mut $ = R{$: 1}\n\t$.$ = 2\n\tmatch $ {\n\tcase $:\n\t\t1\n\tcase _ as $:\n\t\t2\n\t}\n}\n",
		"func calls($ func(int) int) int { $(1) }\n",
	}

	treeSig := func(src string) string {
		f, err := syntax.Parse(src)
		if err != nil {
			return "error: " + err.Error()
		}
		var b strings.Builder
		syntax.Inspect(f, func(n syntax.Node) bool {
			if n != nil {
				fmt.Fprintf(&b, "%T@%d ", n, n.Pos())
			}
			return true
		})
		return b.String()
	}

	// Deterministic all-each check for each contextual word, plus seeded mixes.
	for _, pair := range repl {
		clause := "gives"
		if pair[0] == "gives" {
			clause = "given"
		}
		var b strings.Builder
		for _, pl := range places {
			s := strings.ReplaceAll(pl, "@CLAUSE", clause)
			b.WriteString(strings.ReplaceAll(s, "$", pair[0]))
		}
		src := b.String()
		other := strings.ReplaceAll(src, pair[0], pair[1])
		if got, want := treeSig(src), treeSig(other); got != want {
			t.Fatalf("%s vs %s differed:\nwith %s:\n%s\nwith %s:\n%s", pair[0], pair[1], pair[0], got, pair[1], want)
		}
		if _, err := syntax.Parse(src); err != nil {
			t.Fatalf("%s did not parse cleanly: %v\n%s", pair[0], err, src)
		}
	}

	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x6661))
		var srcB, otherB strings.Builder
		for i, pl := range places {
			if r.IntN(2) == 0 {
				continue
			}
			pair := repl[r.IntN(len(repl))]
			name, alt := pair[0], pair[1]
			if r.IntN(4) == 0 {
				name = fmt.Sprintf("n%04d", i)
				alt = name
			}
			s := strings.ReplaceAll(pl, "@CLAUSE", "gives")
			srcB.WriteString(strings.ReplaceAll(s, "$", name))
			otherB.WriteString(strings.ReplaceAll(s, "$", alt))
		}
		src := srcB.String()
		other := otherB.String()
		if got, want := treeSig(src), treeSig(other); got != want {
			t.Fatalf("seed %d: parse differed\n%s\ngot:  %s\nwant: %s", seed, src, got, want)
		}
	}
}

func TestFactClausesMalformed(t *testing.T) {
	bad := []struct {
		name string
		src  string
	}{
		{"where-on-operation-type", "type Op { Get() int } where true\n"},
		{"empty-given", "func f() int given {}\n"},
		{"empty-gives", "func f() int gives {}\n"},
		{"empty-keeps", "func f() int keeps {}\n"},
		{"empty-keeps-when", "func f() int keeps Live(x) when {}\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := syntax.Parse(tc.src); err == nil {
				t.Fatalf("expected parse error for %s", tc.src)
			}
		})
	}
}
