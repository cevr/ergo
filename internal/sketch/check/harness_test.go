package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// harnessLib is a user library, no std package, that declares a capability
// with a harness (check harness.go): the rule names no package, so std's
// gen is one such library among any.
const harnessLib = `// Coin is a source of flips.
type Coin {
	Flip() bool,

	// A test whose body flips runs under Toss.
	test Toss,
}

// Other is a second capability with a harness.
type Other {
	Ping() bool,
	test Echo,
}

type Run { Name string, Seed uint64, Site string, Source string }

type Failed { Message string }

type always {} implements Coin

func Flip(a always) bool { true }

type pong {} implements Other

func Ping(p pong) bool { true }

// Heads is a flip.
func Heads() bool needs Coin { Coin.Flip() }

// Pinged is a ping.
func Pinged() bool needs Other { Other.Ping() }

// Toss runs a test body that flips: its failure names the run.
func Toss(run Run, body func() fails Failed needs Coin) fails Failed {
	with context { Coin: always } { body() } catch {
	case Failed as f:
		return Failed{Message: run.Name + " seed \(run.Seed) at " + run.Site + ": " + f.Message}
	}
}

func Echo(run Run, body func() fails Failed needs Other) fails Failed {
	with context { Other: pong } { body() } catch {
	case Failed as f:
		return f
	}
}
`

// checkHarnessed checks lib, then src as package main.
func checkHarnessed(t *testing.T, lib, src string) (*check.Package, []check.Diagnostic, []check.Diagnostic) {
	t.Helper()
	lf, err := syntax.Parse(lib)
	if err != nil {
		t.Fatal(err)
	}
	lp, libDiags := check.Check("lib", []*syntax.File{lf}, nil)
	f, err := syntax.Parse("import lib\n" + src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	f.Name = "coin_test.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, libImporter{lp})
	return pkg, libDiags, ds
}

// The shape's S108s are the negative fixtures harness-*.ergo
// (testdata/fixtures/negative), which compiler/check agrees on; the
// admission below has no compiler/check counterpart yet: it checks no
// test body (S3-ROWS owns test-body rows).

// TestHarnessAdmitsTheBody: a test with no parameters whose body needs a
// capability with a harness checks clean, and its body is rewritten into
// the harness's call; every other place that reaches it is still S102.
func TestHarnessAdmitsTheBody(t *testing.T) {
	pkg, libDiags, ds := checkHarnessed(t, harnessLib, "test Flips {\n\tassert lib.Heads()\n}\n")
	if len(libDiags) != 0 || len(ds) != 0 {
		t.Fatalf("lib %v, main %v", libDiags, ds)
	}
	body := pkg.Tests[0].Decl.Body
	es, _ := body.List[0].(*syntax.ExprStmt)
	if len(body.List) != 1 || es == nil {
		t.Fatalf("the body is not rewritten: %#v", body.List)
	}
	caught, _ := es.X.(*syntax.CatchExpr)
	var call *syntax.CallExpr
	if caught != nil {
		call, _ = caught.X.(*syntax.CallExpr)
	}
	if call == nil || call.Fun.(*syntax.SelectorExpr).Sel.Name != "Toss" {
		t.Fatalf("the body is not a caught call of Toss: %#v", es.X)
	}
	for _, c := range []struct{ name, src, want string }{
		{"function", "func f() bool { lib.Heads() }\n", "S102"},
		{"parameters", "test P(n int) {\n\tassert lib.Heads() || n == n\n}\n", "S102"},
		{"law", "law L() {\n\tlib.Heads()\n}\n", "S102"},
		{"two harnesses", "test Both {\n\tassert lib.Heads()\n\tassert lib.Pinged()\n}\n", "one harness"},
	} {
		_, _, ds := checkHarnessed(t, harnessLib, c.src)
		if !strings.Contains(diagText(ds), c.want) {
			t.Errorf("%s: want %s, got %v", c.name, c.want, ds)
		}
	}
	// No member, no harness: the same library's capability is S102 (the
	// rule is the declaration, not the package).
	if _, _, ds := checkHarnessed(t, strings.Replace(harnessLib, "\n\n\t// A test whose body flips runs under Toss.\n\ttest Toss,", "", 1), "test Flips {\n\tassert lib.Heads()\n}\n"); !strings.Contains(diagText(ds), "S102") {
		t.Errorf("unmarked: want S102, got %v", ds)
	}
}

func diagText(ds []check.Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Code + " " + d.Message + "\n")
	}
	return b.String()
}
