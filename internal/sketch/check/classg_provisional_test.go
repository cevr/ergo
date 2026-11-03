package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The generic catch arm `case G as g` takes the instance the caught row
// holds, so while the component's rows are inferred (the silent pass whose
// record class G reads) g's type waits on a row: it is provisional, not
// invalid. A callback frame that typing skips under it keeps its class G
// uses, so the member is refused as when the arm writes `case G[int]`
// (round 9 review M1; rows.md, Capability rows 6).
const garmHeader = "type Cap { M() }\ntype G[T] {}\nfunc load() fails G[int] { return G[int] }\n"

var provisionalRows = []struct {
	name, src string
	want      []string // line:col code message
}{
	{
		name: "garm-classg (function literal, capability row)",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tg.keep(func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the function literal at 9:10 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "garm-classg-errors (error row, try w() catch)",
		src: "type Bang {}\ntype G[T] {}\nfunc mark() fails Bang { return Bang }\n" +
			"func load() fails G[int] { return G[int] }\nfunc keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\ttry mark()\n\ttry w() catch {\n\tcase G as g:\n\t\tg.keep(func() { try walk() })\n\t}\n}\n" +
			"func w() { try load(); try walk() }\nfunc main() {}\n",
		want: []string{"6:6 S108 walk is used as a value through the function literal at 10:10 inside its own recursive group; write its error row: func walk() fails …"},
	},
	{
		name: "garm-classg-block (trailing block)",
		src: garmHeader + "func run[T, R](g G[T], body func() needs R) needs R { body() }\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tg.run() { walk() }\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the trailing block at 9:11 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "garm-classg-control (case G[int] as g)",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G[int] as g:\n\t\tg.keep(func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the function literal at 9:10 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	// The same frame reached by other routes: an argument of a generic
	// call that fails on g, a local bound to g, a value derived from g, a
	// frame inside the skipped frame, a trailing block of a valid callee.
	{
		name: "argument of a call that fails on g",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tkeep(g, func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the function literal at 9:11 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "local bound to g",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tlet h = g\n\t\th.keep(func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the function literal at 10:10 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "local bound to a call on g",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\nfunc id[T](x T) T { return x }\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tlet h = id(g)\n\t\th.keep(func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"6:6 S108 walk is used as a value through the function literal at 11:10 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "frame inside the skipped frame",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tg.keep(func() { let k = func() { walk() }\n\t\t\tk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the function literal at 9:27 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	{
		name: "trailing block of a valid callee",
		src: garmHeader + "func run[T, R](g G[T], body func() needs R) needs R { body() }\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\trun(g) { walk() }\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"5:6 S108 walk is used as a value through the trailing block at 9:10 inside its own recursive group; write its capability row: func walk() needs …"},
	},
	// A frame under a callee invalid for a diagnosed reason stays invalid
	// beside a provisional value in the same body: only the S104.
	{
		name: "invalid frame beside a provisional value",
		src: garmHeader + "func keep[T, F](g G[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G as g:\n\t\tkeep(g, 1)\n\t}\n\tlet x = nope\n\tx.m(func() { walk() })\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"11:10 S104 unknown name nope"},
	},
	// The row never holds the arm's instance (v7): the pin keeps the use
	// in the provisional frame, the reporting pass finds the frame
	// invalid-diagnosed (the arm's own error), and reportGroup drops the
	// use, so only the arm's error is reported (round 10 ruling).
	{
		name: "v7: the row never holds the arm's instance",
		src: "type Cap { M() }\ntype G[T] {}\ntype H[T] {}\nfunc load() fails G[int] { return G[int] }\n" +
			"func keep[T, F](g H[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G[int]:\n\tcase H as h:\n\t\th.keep(func() { walk() })\n\t}\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{"10:7 S108 generic type main.H needs type arguments"},
	},
	// The same with a use of walk that stays: the S108 names that use.
	{
		name: "v7 with a value use that stays",
		src: "type Cap { M() }\ntype G[T] {}\ntype H[T] {}\nfunc load() fails G[int] { return G[int] }\n" +
			"func keep[T, F](g H[T], f F) {}\n" +
			"func walk() {\n\tCap.M()\n\tw() catch {\n\tcase G[int]:\n\tcase H as h:\n\t\th.keep(func() { walk() })\n\t}\n\tlet k = walk\n\t_ = k\n}\n" +
			"func w() { try load(); walk() }\nfunc main() {}\n",
		want: []string{
			"6:6 S108 walk is used as a value at 13:10 inside its own recursive group; write its capability row: func walk() needs …",
			"10:7 S108 generic type main.H needs type arguments",
		},
	},
	// The arm catches a row from outside the component (b5): the row is
	// final, so the arm's error is reported at once, its frame is
	// invalid-diagnosed in the silent pass, and walk is not pinned: no
	// "cannot fail" follows (round 11 review M1).
	{
		name: "b5: the arm catches a non-member's final row",
		src: `type E {}
type G[T] {}
type H[T] {}
func load() fails G[int] { return G[int] }
func boom() fails E { return E }
func keep[T, F](g H[T], f F) {}
func walk() {
	try boom()
	load() catch {
	case G[int]:
	case H as h:
		h.keep(func() { walk() })
	}
	try w()
}
func w() { try walk() }
func outer() {
	w() catch {
	case E:
	}
}
func main() {}
`,
		want: []string{"11:7 S108 generic type main.H needs type arguments"},
	},
	// The arm catches a member's row (b4): the pin keeps the use and the
	// S108 is dropped. walk's row stays pinned, so outer's Catch 2 on E
	// is a follow-on of the arm's error: it disappears when the arm is
	// fixed (b4-fixed is clean), and base17 gives it too (round 13 ruling;
	// rows.md, Capability rows 6).
	{
		name: "b4: a dropped S108 leaves a follow-on Catch 2",
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
	case G[int]:
	case E:
	}
}
func main() {}
`,
		want: []string{
			"11:7 S108 generic type main.H needs type arguments",
			"22:7 S108 main.E is not in the caught row {main.G[int]} (rows.md, Catch 2)",
		},
	},
	// A local bound to a refused member is a callee whose frame the
	// reporting pass records refused-by-class-G, so walk keeps its S108
	// (round 11 review T1).
	{
		name: "f1: a refused member reaches the callee through a local",
		src: `type Cap { M() }
func pick[T](x T) T { return x }
func walk() {
	Cap.M()
	let h = pick(other)
	h(func() { walk() })
	w()
}
func other(f func()) { Cap.M(); walk() }
func w() { walk() }
func main() {}
`,
		want: []string{
			"3:6 S108 walk is used as a value through the function literal at 6:4 inside its own recursive group; write its capability row: func walk() needs …",
			"9:6 S108 other is used as a value at 5:15 inside its own recursive group; write its capability row: func other(…) needs …",
		},
	},
	// The arm catches the row of a refused member (b2, b3): the binding is
	// undiagnosed, its frame refused-by-class-G, so walk keeps its S108;
	// the arm reports nothing from the invalid operand's empty row (round
	// 11 review T2 and O1).
	{
		name: "b2: the arm catches a refused member",
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
}
func w() {
	try load()
	walk()
	let k = w
	_ = k
}
func main() {}
`,
		want: []string{
			"5:6 S108 walk is used as a value through the function literal at 9:10 inside its own recursive group; write its capability row: func walk() needs …",
			"12:6 S108 w is used as a value at 15:10 inside its own recursive group; write its error row: func w() fails …",
		},
	},
	{
		name: "b3: the arm catches a refused member, its only use",
		src: `type G[T] {}
func load() fails G[int] { return G[int] }
func keep[T, F](g G[T], f F) {}
func walk() {
	w() catch {
	case G as g:
		g.keep(func() { w() })
	}
}
func w() {
	try load()
	walk()
}
func main() {}
`,
		want: []string{"10:6 S108 w is used as a value through the function literal at 7:10 inside its own recursive group; write its error row: func w() fails …"},
	},
}

func TestClassGProvisionalFrame(t *testing.T) {
	for _, settleOnly := range []bool{false, true} {
		for _, tc := range provisionalRows {
			name := fmt.Sprintf("%s (settle-only %v)", tc.name, settleOnly)
			restore := func() {}
			if settleOnly {
				restore = check.SelfSettleOnly()
			}
			_, diags := check.CheckSource(tc.src)
			restore()
			var have []string
			for _, d := range diags {
				have = append(have, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			if strings.Join(have, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("%s:\nhave %q\nwant %q", name, have, tc.want)
			}
		}
	}
	for _, tc := range provisionalRows {
		if p := check.RefWalkProblems(tc.src); len(p) > 0 {
			t.Errorf("%s: RefWalkProblems:\n\t%s", tc.name, strings.Join(p, "\n\t"))
		}
		if p := check.OrderProblems(tc.src, 8); len(p) > 0 {
			t.Errorf("%s: OrderProblems:\n\t%s", tc.name, strings.Join(p, "\n\t"))
		}
	}
}
