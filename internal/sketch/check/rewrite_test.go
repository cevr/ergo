package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRewriteLawShapes pins the text and position of each shape rule of a
// rewrite law (types.md, Laws 14), one row per rule: each row's law breaks
// only that rule. The verdicts of well-formed laws are
// lower/TestRewriteLawVerdicts's.
func TestRewriteLawShapes(t *testing.T) {
	const head = "type W = Low{} | High{}\n\ntype C {\n\tAdd(n int),\n\tInc(),\n\tFind(n int) int fails Missing,\n\tMode() W,\n\tGet() int,\n\n"
	for _, tc := range []struct {
		law      string
		line, at int
		msg      string
	}{
		{"law L(a int) { Add(a) } == { Add(a); Add(a) }", 10, 43, "parameter a passes through the right side twice; a rewrite passes a value through once (linear)"},
		{"law L(a int, b int) { Add(a) } == { Add(a) }", 10, 15, "parameter b is not bound by the rewrite's left side; pass it to a left operation"},
		{"law L(a int) { Add(a); Add(a) } == { Add(a + a) }", 10, 29, "parameter a is bound twice by the left side; a rewrite's left side names each parameter once"},
		{"law L(a int) { Add(a + 1); Inc() } == { Inc() }", 10, 21, "a rewrite law's left side passes parameters only; name this value as a parameter of L"},
		{"law L() { Inc(); Inc(); Inc(); Inc() } == { Inc() }", 10, 33, "a rewrite law's left side has at most 3 calls"},
		{"law L() {} == { Inc() }", 10, 10, "a rewrite law's left side has at least one call"},
		{"law L() { Inc() } == { let _ = Get() }", 10, 25, "a rewrite law's right side is calls of C's own operations, one per statement"},
		{"law L() { Inc() } == { Inc(); Inc() }", 10, 6, "rewrite law L unfolds Inc into itself: an unfold law's right side unfolds again (a cycle)"},
		{"law L() when Get() > 0 { Inc(); Inc() } == { Inc() }", 10, 15, "a rewrite law's guard is R() == C, R an operation of C with no parameters that reads one value"},
		{"law L(a int) { Add(a); Inc() } == { Add(a); try Find(0); Inc() }", 10, 36, "the sides of rewrite law L fail differently: {} on the left, {Missing} on the right; a rewrite keeps the failures"},
		{"law L(a int) { Add(a); Inc() } == { Add(a + Get()); Inc() }", 10, 42, "a computed argument of a rewrite's right side calls no operation and has no try; the lowering computes it at compile time"},
	} {
		src := head + "\t" + tc.law + ",\n}\n\ntype Missing {}\n"
		_, diags := check.CheckSource(src)
		if len(diags) != 1 || diags[0].Message != tc.msg || diags[0].Line != tc.line || diags[0].Column != tc.at {
			t.Errorf("%s: want one diagnostic %d:%d %q, got\n\t%s", tc.law, tc.line, tc.at, tc.msg, describe(diags))
		}
	}
}
