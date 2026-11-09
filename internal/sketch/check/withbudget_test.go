package check

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The with budget (withbudget.go; E5 round 2, #229 done-when 1).

// checkWithBudget checks src with the package's with budget replaced by
// limit, or the real budget when limit is nil.
func checkWithBudget(t *testing.T, src string, limit func(int) int) ([]Diagnostic, *checker) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.withLimit = limit
	c.run([]*syntax.File{file})
	return c.sorted(), c
}

// withRefusals is the number of with-budget S102s in diags.
func withRefusals(diags []Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Code == CodeMissingCap && d.Message == withBudgetMessage {
			n++
		}
	}
	return n
}

// TestWithBudgetRefuses: the shapes whose pushes each change the binder of
// n capabilities check clean when small, and past the budget are S102
// once, at an operand of their with, within the budget: the meter never
// passes its limit, and a second check gives the same diagnostic.
func TestWithBudgetRefuses(t *testing.T) {
	for _, sc := range []struct {
		shape string
		n     int
	}{{"pairs", 300}, {"fan", 700}} {
		shape, n := sc.shape, sc.n
		diags, c := checkWithBudget(t, withShape(shape, 12), nil)
		if len(diags) > 0 {
			t.Errorf("%s n=12: %v", shape, diags[0])
		}
		if c.withs.used == 0 {
			t.Errorf("%s n=12: no push work charged", shape)
		}
		src := withShape(shape, n)
		diags, c = checkWithBudget(t, src, nil)
		if len(diags) != 1 || withRefusals(diags) != 1 {
			t.Fatalf("%s n=%d: %d diagnostics %v, want one with-budget S102", shape, n, len(diags), diags)
		}
		if c.withs.used > c.withs.limit {
			t.Errorf("%s n=%d: %d push steps charged, over the limit %d", shape, n, c.withs.used, c.withs.limit)
		}
		if want := withBudgetPerByte*len(src) + withBudgetFloor; c.withs.limit != want {
			t.Errorf("%s: limit %d, want %d", shape, c.withs.limit, want)
		}
		if _, ok := enclosingWith(c, c.withRefused.at); !ok || diags[0].Offset != int(c.withRefused.at.Pos()) {
			t.Errorf("%s n=%d: the S102 at %d:%d is not at an operand of a with", shape, n, diags[0].Line, diags[0].Column)
		}
		again, _ := checkWithBudget(t, src, nil)
		if !slices.Equal(again, diags) {
			t.Errorf("%s n=%d: a second check gives %v, the first %v", shape, n, again, diags)
		}
		t.Logf("%s n=%d: refused at %d:%d after %d of %d steps", shape, n, diags[0].Line, diags[0].Column, c.withs.used, c.withs.limit)
	}
}

// enclosingWith finds the with call one of whose operands is at.
func enclosingWith(c *checker, at syntax.Node) (*syntax.CallExpr, bool) {
	var found *syntax.CallExpr
	for _, f := range c.files {
		syntax.Inspect(f.syntax, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok && c.pkg.Info.Withs[x] != nil {
				for _, a := range x.Args {
					if syntax.Node(a) == at {
						found = x
					}
				}
			}
			return found == nil
		})
	}
	return found, found != nil
}

// TestWithBudgetPrefixes: at every with budget from zero to the push work
// the full check charges, the meter stays within the budget, and the
// verdict is the full check's when the work fits, and otherwise one
// with-budget S102: a with scope the checker did not compute is never
// accepted (D16), a program with a missing capability included.
func TestWithBudgetPrefixes(t *testing.T) {
	missing := `type Store { Get() string }
type Log { Put(s string) }
type Mem { V string }

func Get(m Mem) string { return m.V }

let A = context { Store: Mem{V: "a"} }

func f() string needs Store {
	let b = context { Store: Mem{V: "b"} }
	return with A, b, context { Store: Mem{V: "c"} } { Store.Get() + g() }
}

func g() string needs Store | Log {
	Log.Put("x")
	return Store.Get()
}
`
	srcs := map[string]string{
		"missing":           missing,
		"alternating":       withShape("alternating", 6),
		"local alternating": withShape("local alternating", 6),
		"pairs":             withShape("pairs", 5),
		"fan":               withShape("fan", 5),
		"nested":            withShape("nested", 6),
		"several":           withShape("several", 6),
	}
	for name, src := range srcs {
		full, c := checkWithBudget(t, src, func(int) int { return 1 << 40 })
		work := c.withs.used
		if work == 0 {
			t.Errorf("%s: no push work charged", name)
		}
		for limit := 0; limit <= work+1; limit++ {
			diags, c := checkWithBudget(t, src, func(int) int { return limit })
			if c.withs.used > limit {
				t.Fatalf("%s limit %d: %d steps charged", name, limit, c.withs.used)
			}
			if limit >= work {
				if !slices.Equal(diags, full) {
					t.Fatalf("%s limit %d: %v, the full check %v", name, limit, diags, full)
				}
				continue
			}
			if withRefusals(diags) != 1 {
				t.Fatalf("%s limit %d of %d: %d with-budget S102s in %v, want one", name, limit, work, withRefusals(diags), diags)
			}
		}
	}
}

// TestWithBudgetHeadroom: the corpus, the fixtures and the examples use
// under 1% of their with budget, and each shape TestWithSiteWorkLinear
// counts, at its larger size, under 25%, and checks clean.
func TestWithBudgetHeadroom(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/classf/*.ergo",
		"testdata/totality/*.ergo",
		"../testdata/*.ergo",
		"../testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
		"../../../examples/*/*/*.ergo",
		"../../../docs/research/ef-migrate/*.ergo",
	} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	if len(files) < 300 {
		t.Fatalf("found %d sources", len(files))
	}
	worst, worstFile, pushed := 0.0, "", 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := usesKeyword.ReplaceAllString(string(data), "needs")
		if _, err := syntax.Parse(src); err != nil {
			continue
		}
		diags, c := checkWithBudget(t, src, nil)
		if withRefusals(diags) > 0 {
			t.Errorf("%s: refused by the with budget", f)
		}
		if c.withs.used == 0 {
			continue
		}
		pushed++
		share := float64(c.withs.used) / float64(c.withs.limit)
		if share >= 0.01 {
			t.Errorf("%s: %d of %d push steps (%.2f%%), want under 1%%", f, c.withs.used, c.withs.limit, 100*share)
		}
		if share > worst {
			worst, worstFile = share, f
		}
	}
	t.Logf("%d sources, %d with push work; the largest share is %.3f%% (%s)", len(files), pushed, 100*worst, worstFile)
	for _, shape := range []string{"wowned", "nested", "several", "stack", "local many", "local chain", "alternating", "local alternating"} {
		sizes := GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, withShape(shape, n)) })
		n := sizes[len(sizes)-1]
		diags, c := checkWithBudget(t, withShape(shape, n), nil)
		if len(diags) > 0 {
			t.Errorf("%s n=%d: %v", shape, n, diags[0])
			continue
		}
		share := float64(c.withs.used) / float64(c.withs.limit)
		if share >= 0.25 {
			t.Errorf("%s n=%d: %d of %d push steps (%.1f%%), want under 25%%", shape, n, c.withs.used, c.withs.limit, 100*share)
		}
		t.Logf("%s n=%d: %d of %d push steps (%.2f%%)", shape, n, c.withs.used, c.withs.limit, 100*share)
	}
}
