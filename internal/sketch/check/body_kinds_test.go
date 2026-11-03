package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// D-C1 and D-C2 hold for every body kind (stage C round 1 review M4 and
// m1): a function body, a with block, a trailing block and a catch arm
// yield their last statement, a final if or match included, and a body
// with no value discards its tail. A path without a value where one is
// needed is S107; S108 is only a no-else if in a value position.
func TestEveryBodyKindYields(t *testing.T) {
	const decls = "let W = context {}\ntype E {}\nfunc g() fails E { E }\nfunc n() int fails E { 1 }\nfunc use(f func() int) int { f() }\n"
	for _, tc := range []struct{ name, src, want string }{
		// with blocks
		{"with-if", "func f(b bool) int { with W { if b { 1 } else { 2 } } }", ""},
		{"with-match", "func f(b bool) int { with W { match b { case true: 1; case false: 2 } } }", ""},
		{"with-nested", "func f(b bool, c bool) int {\n\twith W {\n\t\tif b { if c { 1 } else { 2 } } else { 3 }\n\t}\n}", ""},
		{"with-statement", "func f(b bool) {\n\twith W {\n\t\tif b { g() catch { case E: h() } }\n\t}\n}\nfunc h() int { 1 }", ""},
		{"with-missing-else", "func f(b bool) int {\n\twith W {\n\t\tif b { 1 }\n\t}\n}", "S107:9"},
		{"with-int8-literals", "func f(b bool) int8 { with W { if b { 1 } else { 2 } } }", ""},
		// trailing blocks
		{"trailing-if", "func f(b bool) int { use() { if b { 1 } else { 2 } } }", ""},
		{"trailing-missing-else", "func f() int {\n\tuse() {\n\t\tif true { 1 }\n\t}\n}", "S107:9"},
		{"trailing-empty", "func f() int {\n\tuse() {\n\t}\n}", "S107:8"},
		// catch arms
		{"catch-void-tail", "func f() {\n\tg() catch { case E: h() }\n}\nfunc h() int { 1 }", ""},
		{"catch-void-value-tail", "func f() {\n\tg() catch { case E: 1 }\n}", "S110:7"},
		{"catch-arm-if", "func f(b bool) int { n() catch { case E: if b { 1 } else { 2 } } }", ""},
		{"catch-arm-missing-else", "func f(b bool) int {\n\tn() catch {\n\tcase E:\n\t\tif b { 1 }\n\t}\n}", "S107:9"},
		{"catch-arm-statement", "func f(b bool) int {\n\tn() catch {\n\tcase E:\n\t\tlet x = 1\n\t\t_ = x\n\t}\n}", "S107:10"},
		{"catch-arm-returns", "func f() int {\n\tn() catch {\n\tcase E:\n\t\treturn 2\n\t}\n}", ""},
		// value positions
		{"value-if-without-else", "func f() int {\n\tlet x = if true { 1 }\n\tx\n}", "S108:7"},
		{"body-if-without-else", "func f() int {\n\tif true { 1 }\n}", "S107:8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
			if tc.want == "" {
				if gaps := check.InfoGaps(src); len(gaps) != 0 {
					t.Fatalf("Info gaps: %v", gaps)
				}
			}
		})
	}
}

// An if or match in value position holds value references (D-C2): the
// reference walk visits them as typing does, so a row inferred through one
// reaches its caller whatever the declaration order. The first source is
// the FuzzCheck input of stage C round 2 (merged head).
func TestBranchExpressionsAreReferences(t *testing.T) {
	for _, src := range []string{
		"func A(){0%match A{case 0:\"\"}%match 0{case 0:0}}",
		"type E {}\nfunc h(b bool) int { 1 + if b { try g() } else { 2 } }\nfunc g() int { base() }\nfunc base() int fails E { E }\n",
		"type E {}\nfunc h() int { 1 + match try g() { case 1: 2; case _: 3 } }\nfunc g() int { base() }\nfunc base() int fails E { E }\n",
	} {
		if p := check.RefWalkProblems(src); len(p) > 0 {
			t.Errorf("%q: reference walk: %s", src, strings.Join(p, "; "))
		}
		if p := check.OrderProblems(src, 2); len(p) > 0 {
			t.Errorf("%q: declaration order: %s", src, p[0])
		}
	}
	const src = "type E {}\nfunc h() int { 1 + match try g() { case 1: 2; case _: 3 } }\nfunc g() int { base() }\nfunc base() int fails E { E }\nfunc F() int { h() }\n"
	if got, d := codeLines(src); got != "S101:5" {
		t.Fatalf("want [S101:5]: h's inferred row reaches F; got %s", describe(d))
	}
}
