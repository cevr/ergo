package check

import (
	"fmt"
	"strings"
	"testing"
)

// TestClassGMessage: the class G S108 names the value use, or the literal
// that carries it, and the row to write with the parameters elided (round
// 16 m2).
func TestClassGMessage(t *testing.T) {
	for _, tc := range []struct{ src, at, want string }{
		// Decided sources (#196): mark and keep are ordinary functions, and
		// keep is generic, since there is no any type; each line moves
		// up by the package clause.
		{
			src: "type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc keep[T](f T) {}\n" +
				"func walk(n int) { try mark(); keep(func() { try walk(n) }) }\n",
			at:   "func() { try walk",
			want: "walk is used as a value through the function literal at 4:%d inside its own recursive group; write its error row: func walk(…) fails …",
		},
		{
			src:  "type Bang {}\nfunc mark() fails Bang { return Bang }\nfunc walk() { try mark(); let g = walk; _ = g }\n",
			at:   "walk; _",
			want: "walk is used as a value at 3:%d inside its own recursive group; write its error row: func walk() fails …",
		},
		// run checks its block (its row R is open): a block typing never
		// checks, as one an arity error refuses, carries no class G use
		// (round 8 review M2).
		{
			src: "type Cap { M() }\nfunc run[R](n int, body func() needs R) needs R { body() }\n" +
				"func walk(n int) { Cap.M(); run(n) { walk(n) } }\n",
			at:   "{ walk(n) }",
			want: "walk is used as a value through the trailing block at 3:%d inside its own recursive group; write its capability row: func walk(…) needs …",
		},
	} {
		line := tc.src[strings.LastIndex(tc.src[:strings.Index(tc.src, tc.at)], "\n")+1:]
		want := fmt.Sprintf(tc.want, strings.Index(line, tc.at)+1)
		_, diags := CheckSource(tc.src)
		found := false
		for _, d := range diags {
			if d.Message == want {
				found = true
			}
		}
		if !found {
			t.Errorf("want %q, got %v", want, diags)
		}
	}
}
