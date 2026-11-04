package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A bare generic catch arm with no instance in its row is an error of its
// own. In a recursive component, class G may then drop its S108, and the
// member's error row stays pinned: a lower bound of its real row. A check
// that reports an absence on such a row (Catch 1, the row half of Catch 2,
// "try on an expression that cannot fail", a bare generic arm with no
// instance) may then report a follow-on diagnostic. The round 13 ruling
// allows a follow-on when (a) the program reports the arm's own error,
// (b) the follow-on disappears when the arm is fixed, and (c) main
// (base17) gives it too; check-v1 (196r) owns removing them (rows.md,
// Capability rows 6). A real independent error is never missing: every
// diagnostic of the fixed program is reported in the broken one, except on
// a catch whose caught row is the pinned one: there a follow-on Catch 1
// reports, and the elements the fixed program names reach that row only
// through the pinned member.
//
// Outside the component, a reference to the refused member reads an error
// row that holds invalid (stage D1 round 4, rows.md Inference 6). A check
// that holds only for some values of that row is silent there, so the
// later-component rows (q1b, q2b, q5, q9) have no follow-on, and the
// fixed program's diagnostics on such a catch are listed as hidden with
// the catch in unknown. A check that holds for every value of the row
// (a duplicate arm) still reports.
//
// Each row names the review probe it comes from: p (round 12 review), q
// (round 13 review), k1 and kept (round 13 request). Condition (c) is
// measured with the base17 binary (round-14-receipts/probes.log); the test
// checks (a), (b) and the missing-error property against the fixed
// program.

// armHeader is the b4 component: walk's arm `case H as h` has no instance
// in w's row, so it is an error, the frame of h.keep is invalid-diagnosed,
// and walk's S108 is dropped, which leaves walk's error row pinned empty.
const armHeader = `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func quit() fails Q { return Q }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
`

// armLines is the wrong arm; the fixed program is the source without it,
// unless a row gives its own.
const armLines = "\tcase H as h:\n\t\th.keep(func() { walk() })\n"

const (
	armError13 = "13:7 S108 generic type main.H needs type arguments"
	armError12 = "12:7 S108 generic type main.H needs type arguments"
	armError11 = "11:7 S108 generic type main.H needs type arguments"
)

type followOnRow struct {
	name, src string
	want      []string // every diagnostic, "L:C CODE message", in order
	followOn  []string // the lines of want that are follow-ons
	fixed     string   // the program with the arm fixed; "" = src without armLines
	// hidden: diagnostics of the fixed program, at its positions, on a
	// catch whose row in the broken program is the pinned one: there a
	// follow-on Catch 1 replaces them. The elements they name reach the
	// row only through the pinned member, so they are not independent of
	// the arm's error.
	// It also holds on a catch listed in unknown.
	hidden []string
	// unknown: the lines where catches start whose caught row in the
	// broken program comes from a refused member outside the component,
	// so it holds invalid (rows.md, Inference 6). The checks on that row
	// (Catch 1, Catch 2's row half, Catch 4, a bare generic arm) stay
	// silent, because each holds only for some values of the unknown row.
	unknown []int
	// noFixed: the row has no arm error to fix (a kept S108).
	noFixed bool
}

var followOnRows = []followOnRow{
	{
		// Catch 4 reports presence: G[int] comes from load, a final
		// callee, so it is in w's real row. Catch 2 on E reads walk's
		// pinned row.
		name: "p1: a missing arm for an element of a pinned row",
		src: `type E {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
func outer() {
	w() catch {
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError11,
			"20:6 S103 this catch does not handle {main.G[int]}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"21:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
		followOn: []string{"21:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)"},
	},
	{
		name: "p2: a reader in another component misses another callee's element",
		src: armHeader + `func both() {
	try w()
	try quit()
}
func outer() {
	both() catch {
	case G[int]:
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError13,
			"26:9 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)",
		},
		followOn: []string{"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)"},
	},
	{
		name: "p3: the same through a generic reader",
		src: armHeader + `func gen[T](x T) {
	try w()
	try quit()
}
func outer() {
	gen(1) catch {
	case G[int]:
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError13,
			"26:9 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)",
		},
		followOn: []string{"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)"},
	},
	{
		name: "p4: a local function literal reads the pinned row",
		src: armHeader + `func outer() {
	let f = func() {
		try w()
		try quit()
	}
	f() catch {
	case G[int]:
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError13,
			"26:6 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)",
		},
		followOn: []string{"28:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)"},
	},
	{
		name: "p5: a local and a package let bound to the pinned function",
		src: `type E {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
let hook = w
func outer() {
	let k = w
	k() catch {
	case G[int]:
	case E:
	}
	hook() catch {
	case G[int]:
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError11,
			"24:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
			"28:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
		followOn: []string{
			"24:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
			"28:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
	},
	{
		name: "p6: callers with declared rows",
		src: armHeader + `func Outer() fails Q {
	w() catch {
	case G[int]:
	case E:
	}
	try quit()
}
func main() {
	w() catch {
	case E:
	}
}
`,
		want: []string{
			armError13,
			"24:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
			"29:6 S103 this catch does not handle {main.G[int]}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"30:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
		followOn: []string{
			"24:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
			"30:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
	},
	{
		// x2's catch is total, so x1's try on it really cannot fail. The
		// unrelated y1/y2 keep every diagnostic.
		name: "p7: a second component, and a total catch",
		src: armHeader + `func x2() {
	x1() catch {
	case G[int]:
	case E:
	}
}
func x1() {
	try w()
	try quit()
	try x2()
}
func y1() {
	try quit()
	try y2()
}
func y2() {
	y1() catch {
	case E:
	}
}
func main() {}
`,
		want: []string{
			armError13,
			"22:7 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"24:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)",
			"30:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
			"34:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
			"37:7 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"38:7 S108 main.E is not in the caught row {main.Q} (rows.md, Catch 2)",
		},
		followOn: []string{"24:7 S108 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)"},
	},
	{
		// walk's S108 is kept (its capability row); Q from quit is really
		// unhandled. The fixed program writes walk's row.
		name: "p8: a kept S108 beside a missing arm",
		src: `type Cap { M() }
type Q {}
type G[T] {}
func load() fails G[int] { return G[int] }
func quit() fails Q { return Q }
func keep[T, F](g G[T], f F) {}
func walk() {
	Cap.M()
	w() catch {
	case G as g:
		g.keep(func() { walk() })
	}
}
func w() { try load(); try quit(); walk() }
func main() {}
`,
		fixed: `type Cap { M() }
type Q {}
type G[T] {}
func load() fails G[int] { return G[int] }
func quit() fails Q { return Q }
func keep[T, F](g G[T], f F) {}
func walk() needs Cap {
	Cap.M()
	w() catch {
	case G as g:
		g.keep(func() { walk() })
	}
}
func w() { try load(); try quit(); walk() }
func main() {}
`,
		want: []string{
			"7:6 S108 walk is used as a value through the function literal at 11:10 inside its own recursive group; write its capability row: func walk() needs …",
			"9:6 S103 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
		},
	},
	{
		name: "p9: an absence check inside the component, S108 kept",
		src: `type Cap { M() }
type G[T] {}
func load() fails G[int] { return G[int] }
func keep[T, F](g G[T], f F) {}
func walk() {
	Cap.M()
	w() catch {
	case G as g:
		g.keep(func() { walk() })
	}
	try z()
}
func w() { try load(); walk() }
func z() {
	w() catch {
	case G[int]:
	}
	walk()
}
func main() {}
`,
		fixed: `type Cap { M() }
type G[T] {}
func load() fails G[int] { return G[int] }
func keep[T, F](g G[T], f F) {}
func walk() needs Cap {
	Cap.M()
	w() catch {
	case G as g:
		g.keep(func() { walk() })
	}
	try z()
}
func w() { try load(); walk() }
func z() {
	w() catch {
	case G[int]:
	}
	walk()
}
func main() {}
`,
		want: []string{
			"5:6 S108 walk is used as a value through the function literal at 9:10 inside its own recursive group; write its capability row: func walk() needs …",
			"11:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
		},
	},
	{
		// The duplicate arm is a fact of the arms: the follow-on Catch 1
		// on w2's pinned row does not hide it (round 13 review M1). The
		// fixed program's Catch 2 on Q reads the full row; here Catch 1
		// reports on that catch instead.
		name: "q1: a duplicate arm beside a Catch 1, inside the component",
		src: `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
	w2() catch {
	case Q:
	case Q:
	}
}
func w() {
	try load()
	try walk()
}
func w2() {
	try walk()
}
func main() {}
`,
		want: []string{
			armError12,
			"15:2 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"17:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		},
		followOn: []string{"15:2 S108 catch on an expression that cannot fail (rows.md, Catch 1)"},
		hidden: []string{
			"13:7 S103 this catch does not handle {main.E}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"14:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)",
		},
	},
	{
		name: "q1b: the same catch in a later component",
		src: `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
func w2() {
	try walk()
}
func outer() {
	w2() catch {
	case Q:
	case Q:
	}
}
func main() {}
`,
		// w2 reads the refused member walk, so its row holds invalid
		// (stage D1 round 4): Catch 1, Catch 2's row half and Catch 4
		// stay silent on outer's catch. The duplicate arm holds for every
		// row and stays.
		want: []string{
			armError12,
			"26:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		},
		hidden: []string{
			"22:7 S103 this catch does not handle {main.E}; a catch without try handles every error of the row (rows.md, Catch 4)",
			"23:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)",
		},
		unknown: []int{24},
	},
	{
		// Q is in no real row, so its Catch 2 is real, and so is the
		// duplicate.
		name: "q2: a duplicate arm beside a Catch 2, inside the component",
		src: `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
	w2() catch {
	case E:
	case Q:
	case Q:
	}
}
func w() {
	try load()
	try walk()
}
func w2() {
	try boom()
	try walk()
}
func main() {}
`,
		want: []string{
			armError12,
			"17:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)",
			"18:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		},
	},
	{
		name: "q2b: the same catch in a later component",
		src: `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
func w2() {
	try boom()
	try walk()
}
func outer() {
	w2() catch {
	case E:
	case Q:
	case Q:
	}
}
func main() {}
`,
		// w2 reads the refused member walk, so its row {main.E} also
		// holds invalid (stage D1 round 4): whether Q is in it is
		// unknown, so Catch 2's row half is silent. The duplicate arm
		// holds for every row and stays.
		want: []string{
			armError12,
			"28:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		},
		hidden: []string{
			"25:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)",
		},
		unknown: []int{25},
	},
	{
		// The row reaches outer through a generic row parameter.
		name: "q5: a generic callee's row parameter",
		src: `type E {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try load()
	try walk()
}
func w2() {
	try walk()
}
func run[R](f func() fails R) fails R {
	try f()
}
func outer() {
	run(func() { try w2() }) catch {
	case E:
	}
}
func main() {}
`,
		// w2 reads the refused member walk, so its row holds invalid
		// (stage D1 round 4). The literal's try can fail, R binds a row
		// that holds invalid, and outer's catch is not known to be empty.
		want: []string{armError11},
	},
	{
		// The partial catch in m reads w2's pinned row, so its Catch 1
		// replaces the fixed program's Catch 2 on Q, and the tries on m
		// follow.
		name: "q8b: a partial catch's Catch 1 and the tries after it",
		src: `type E {}
type Q {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
	try m()
}
func w() {
	try load()
	try walk()
}
func w2() {
	try walk()
}
func m() {
	try w2() catch {
	case Q:
	}
}
func outer() {
	try m()
}
func main() {}
`,
		want: []string{
			armError12,
			"15:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
			"25:6 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"30:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
		},
		followOn: []string{
			"15:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
			"25:6 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"30:2 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
		},
		hidden: []string{"24:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)"},
	},
	{
		// A bare generic arm on w2's row, which reads the refused member:
		// whether it has an instance is unknown.
		name: "q9: a bare generic arm on a pinned row",
		src: `type E {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try load()
	try w() catch {
	case E:
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() {
	try boom()
	try walk()
}
func w2() {
	try walk()
}
func outer() {
	w2() catch {
	case G as g:
		_ = g
	}
}
func main() {}
`,
		// w2 reads the refused member walk, so its row holds invalid
		// (stage D1 round 4): the catch is not known to be empty, and the
		// bare arm's instance is unknown, so both checks are silent.
		want: []string{armError11},
	},
	{
		name: "k1: try and catch on a pinned row that is empty",
		src: `type E {}
type H[T] {}
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case H as h:
		h.keep(func() { walk() })
	}
}
func w() { try walk() }
func outer() { try w() }
func main() {}
`,
		fixed: `type E {}
type H[T] {}
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case E:
	}
}
func w() { try walk() }
func outer() { try w() }
func main() {}
`,
		want: []string{
			"7:6 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"8:7 S108 generic type main.H needs type arguments",
			"13:16 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
		},
		followOn: []string{
			"7:6 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"13:16 S108 try on an expression that cannot fail (rows.md, Error rows 6)",
		},
	},
	{
		// walk's S108 is reported, so nothing is pinned without a
		// diagnostic: the absence checks on w's row are real.
		name:    "kept: a kept error-row S108 and the checks on its row",
		noFixed: true,
		src: `type E {}
type Q {}
type G[T] {}
func boom() fails E { return E }
func load() fails G[int] { return G[int] }
func keep[T, F](g G[T], f F) {}
func walk() {
	try boom()
	try w() catch {
	case G[int] as g:
		g.keep(func() { try walk() })
	case Q:
	}
}
func w() { try load(); try walk() }
func outer() {
	w() catch {
	case G[int]:
	case Q:
	}
}
func main() {}
`,
		want: []string{
			"7:6 S108 walk is used as a value through the function literal at 11:10 inside its own recursive group; write its error row: func walk() fails …",
			"12:7 S108 main.Q is not in the caught row {main.G[int]} (rows.md, Catch 2)",
			"19:7 S108 main.Q is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
	},
}

// absenceCheck reports whether a diagnostic says that a row lacks
// something, which only the full row can show.
func absenceCheck(line string) bool {
	for _, s := range []string{
		"catch on an expression that cannot fail",
		"try on an expression that cannot fail",
		"is not in the caught row",
		"needs type arguments",
	} {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func diagLines(src string) []string {
	_, diags := check.CheckSource(src)
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
	}
	return out
}

// lineMap maps a line of fixed to the same line of src: the lines before
// the first and after the last line that differ correspond one to one.
// ok is false for a line inside the changed region. changed reports
// whether a line of src lies inside it.
func lineMap(src, fixed string) (mapLine func(int) (int, bool), changed func(int) bool) {
	a, b := strings.Split(src, "\n"), strings.Split(fixed, "\n")
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mapLine = func(l int) (int, bool) {
		switch {
		case l <= pre:
			return l, true
		case l > len(b)-suf:
			return l + len(a) - len(b), true
		}
		return 0, false
	}
	changed = func(l int) bool { return l > pre && l <= len(a)-suf }
	return mapLine, changed
}

// inFollowOnCatch1 reports whether line lies inside a catch expression of
// src whose first line has a follow-on Catch 1 or is listed in unknown.
func inFollowOnCatch1(src string, line int, followOn []string, unknown []int) bool {
	f, err := syntax.Parse(src)
	if err != nil {
		return false
	}
	lineAt := func(p syntax.Pos) int { return strings.Count(src[:p], "\n") + 1 }
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		ce, ok := n.(*syntax.CatchExpr)
		if !ok || found {
			return !found
		}
		first, last := lineAt(ce.Pos()), lineAt(ce.End()-1)
		if line < first || line > last {
			return true
		}
		if slices.Contains(unknown, first) {
			found = true
		}
		for _, d := range followOn {
			if l, _ := lineOf(d); l == first && strings.Contains(d, "Catch 1") {
				found = true
			}
		}
		return true
	})
	return found
}

// lineOf is the line number of a "L:C CODE message" diagnostic, and the
// text after it.
func lineOf(d string) (int, string) {
	var l int
	fmt.Sscanf(d, "%d:", &l)
	return l, d[strings.Index(d, ":"):]
}

func TestFollowOnRows(t *testing.T) {
	for _, settleOnly := range []bool{false, true} {
		for _, tc := range followOnRows {
			restore := func() {}
			if settleOnly {
				restore = check.SelfSettleOnly()
			}
			have := diagLines(tc.src)
			restore()
			if strings.Join(have, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("%s (settle-only %v):\nhave %q\nwant %q", tc.name, settleOnly, have, tc.want)
			}
		}
	}
	for _, tc := range followOnRows {
		for _, f := range tc.followOn {
			if !slices.Contains(tc.want, f) || !absenceCheck(f) {
				t.Errorf("%s: follow-on %q is not an absence check among the diagnostics", tc.name, f)
			}
		}
		if tc.noFixed {
			if len(tc.followOn) > 0 {
				t.Errorf("%s: a row without an arm error has no follow-on", tc.name)
			}
			continue
		}
		fixed := tc.fixed
		if fixed == "" {
			fixed = strings.Replace(tc.src, armLines, "", 1)
		}
		if fixed == tc.src {
			t.Fatalf("%s: no fixed program", tc.name)
		}
		mapLine, changed := lineMap(tc.src, fixed)
		real := map[string]bool{}
		fixedDiags := diagLines(fixed)
		for _, h := range tc.hidden {
			if !slices.Contains(fixedDiags, h) {
				t.Errorf("%s: hidden %q is not a diagnostic of the fixed program", tc.name, h)
			}
		}
		for _, d := range fixedDiags {
			l, rest := lineOf(d)
			m, ok := mapLine(l)
			if !ok {
				t.Errorf("%s: the fixed program's %q lies in the changed lines", tc.name, d)
				continue
			}
			if slices.Contains(tc.hidden, d) {
				if !inFollowOnCatch1(tc.src, m, tc.followOn, tc.unknown) {
					t.Errorf("%s: hidden %q is not on a catch whose Catch 1 is a follow-on or whose row is unknown (at %d)", tc.name, d, m)
				}
				continue
			}
			md := fmt.Sprint(m) + rest
			real[md] = true
			// A real independent error is never missing.
			if !slices.Contains(tc.want, md) {
				t.Errorf("%s: the fixed program reports %q, the broken one does not (at %d)", tc.name, d, m)
			}
		}
		own := false
		for _, d := range tc.want {
			l, _ := lineOf(d)
			switch {
			case changed(l):
				own = true // (a) the arm's own error
			case real[d]:
				if slices.Contains(tc.followOn, d) {
					t.Errorf("%s: follow-on %q stays when the arm is fixed", tc.name, d)
				}
			case !slices.Contains(tc.followOn, d):
				// (b) every other diagnostic disappears with the arm.
				t.Errorf("%s: %q is neither the arm's error, nor in the fixed program, nor a listed follow-on", tc.name, d)
			}
		}
		if len(tc.followOn) > 0 && !own {
			t.Errorf("%s: follow-ons without the arm's own error", tc.name)
		}
	}
	for _, tc := range followOnRows {
		if p := check.RefWalkProblems(tc.src); len(p) > 0 {
			t.Errorf("%s: RefWalkProblems:\n\t%s", tc.name, strings.Join(p, "\n\t"))
		}
		if p := check.OrderProblems(tc.src, 8); len(p) > 0 {
			t.Errorf("%s: OrderProblems:\n\t%s", tc.name, strings.Join(p, "\n\t"))
		}
	}
}

// A duplicate arm reads the arms alone, so a Catch 1, a Catch 2 on a type
// the row lacks, or an invalid caught expression does not hide it (rows.md,
// Catch 2; round 13 review M1, round 14 review T1). These programs have no
// class G at all.
func TestDuplicateArmWhateverRow(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		{"beside a Catch 1", `type Q {}
func f() {}
func main() {
	f() catch {
	case Q:
	case Q:
	}
}
`, []string{
			"4:2 S108 catch on an expression that cannot fail (rows.md, Catch 1)",
			"6:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		}},
		{"beside a type the row lacks", `type E {}
type Q {}
func f() fails E { return E }
func main() {
	f() catch {
	case E:
	case Q:
	case Q:
	}
}
`, []string{
			"7:7 S108 main.Q is not in the caught row {main.E} (rows.md, Catch 2)",
			"8:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		}},
		{"in the row", `type E {}
func f() fails E { return E }
func main() {
	f() catch {
	case E:
	case E:
	}
}
`, []string{"6:7 S108 main.E is caught twice (rows.md, Catch 2)"}},
		// The round 14 review's probe r2 (mutant DUP3): the caught
		// expression is invalid, and the duplicate is still reported.
		{"on an invalid caught expression", `type Q {}
func main() {
	nope() catch {
	case Q:
	case Q:
	}
}
`, []string{
			"3:2 S104 unknown name nope",
			"5:7 S108 main.Q is caught twice (rows.md, Catch 2)",
		}},
	} {
		if have := diagLines(tc.src); strings.Join(have, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\nhave %q\nwant %q", tc.name, have, tc.want)
		}
	}
}

// The component in one file and its readers in another give the same
// diagnostics in both file orders, and two checks of one source in one
// process agree (round 12 review, TestR12ReviewMultiFile). File B's
// follow-on is the Catch 2 on E at 8:7; base17 gives it too.
func TestFollowOnTwoFiles(t *testing.T) {
	fileA := armHeader
	fileB := `func both() {
	try w()
	try quit()
}
func outer() {
	both() catch {
	case G[int]:
	case E:
	}
}
func y1() {
	try quit()
	try y2()
}
func y2() {
	y1() catch {
	case E:
	}
}
func main() {}
`
	wantA := []string{"S108@13:7 generic type main.H needs type arguments"}
	wantB := []string{
		"S103@6:9 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
		"S108@8:7 main.E is not in the caught row {main.G[int] | main.Q} (rows.md, Catch 2)",
		"S108@13:2 try on an expression that cannot fail (rows.md, Error rows 6)",
		"S103@16:7 this catch does not handle {main.Q}; a catch without try handles every error of the row (rows.md, Catch 4)",
		"S108@17:7 main.E is not in the caught row {main.Q} (rows.md, Catch 2)",
	}
	for _, aFirst := range []bool{true, false} {
		order := []string{fileA, fileB}
		if !aFirst {
			order = []string{fileB, fileA}
		}
		var files []*syntax.File
		for _, s := range order {
			f, err := syntax.Parse(s)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		_, diags := check.Check("main", files, nil)
		byFile := map[int][]string{}
		for _, d := range diags {
			byFile[d.File] = append(byFile[d.File], fmt.Sprintf("%s@%d:%d %s", d.Code, d.Line, d.Column, d.Message))
		}
		a, b := 0, 1
		if !aFirst {
			a, b = 1, 0
		}
		if strings.Join(byFile[a], "\n") != strings.Join(wantA, "\n") || strings.Join(byFile[b], "\n") != strings.Join(wantB, "\n") {
			t.Errorf("A first %v:\nhave A %q\nwant A %q\nhave B %q\nwant B %q", aFirst, byFile[a], wantA, byFile[b], wantB)
		}
	}
	var runs [2]string
	for i := range runs {
		_, diags := check.CheckSource(fileA + fileB)
		var have []string
		for _, d := range diags {
			have = append(have, fmt.Sprintf("%s@%d:%d", d.Code, d.Line, d.Column))
		}
		runs[i] = strings.Join(have, " ")
	}
	if runs[0] != runs[1] {
		t.Errorf("two checks of one source differ:\n%s\n%s", runs[0], runs[1])
	}
}
