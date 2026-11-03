package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRowTextInMessages: a message prints a row as the source writes it,
// its names separated by | (196n): a capability row in a function type and
// as a set, and an error row as a set.
func TestRowTextInMessages(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			name: "capability row wider than the destination's",
			src: "type Log { Put() }\ntype Store { Get() int }\n" +
				"func k() int needs Log | Store { Log.Put()\n\tStore.Get() }\n" +
				"func f() int needs Log | Store {\n\tlet h func() int needs Log = k\n\th()\n}\nfunc main() {}\n",
			want: "its capability row {main.Log | main.Store} is wider than {main.Log}",
		},
		{
			name: "function type text",
			src: "type Log { Put() }\ntype Store { Get() int }\n" +
				"func k() int needs Log | Store { Log.Put()\n\tStore.Get() }\n" +
				"func f() int needs Log | Store {\n\tlet h int = k\n\th\n}\nfunc main() {}\n",
			want: "func() int needs main.Log | main.Store",
		},
		{
			name: "error row as a set",
			src: "type A {}\ntype B {}\ntype C {}\nfunc g() fails A | B { return A }\n" +
				"func f() int fails C {\n\tlet x = g() catch {\n\tcase C:\n\t\t1\n\t}\n\tx\n}\nfunc main() {}\n",
			want: "{main.A | main.B}",
		},
	} {
		_, diags := check.CheckSource(tc.src)
		var have []string
		for _, d := range diags {
			have = append(have, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
		}
		if !strings.Contains(strings.Join(have, "\n"), tc.want) {
			t.Errorf("%s: no message holds %q:\n%s", tc.name, tc.want, strings.Join(have, "\n"))
		}
	}
}
