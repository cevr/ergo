package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A refused name still binds an invalid placeholder. Reading that name in
// the arm must not resolve the package object and add a second diagnostic.
func TestRefusedPatternNameBinds(t *testing.T) {
	for _, tc := range []struct{ name, decl string }{
		{"helper", `func helper() string { return "" }`},
		{"len", ""},
		{"timeout", `import timeout`},
		{"shared", `let shared = 1`},
		{"Finished", `type Finished { N int }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("%s\ntype R { Value string }\nfunc F(r R) string { match r { case R{Value: %s}: return %s } }\n", tc.decl, tc.name, tc.name)
			_, d := check.CheckSource(src)
			want := tc.name + " already names"
			switch tc.name {
			case "Finished":
				want = "Finished does not match"
			case "len":
				// D-D3 in every binder kind (stage D1 round 1 review m2).
				want = "len is a prelude name, which cannot be redeclared"
			}
			if len(d) != 1 || d[0].Code != check.CodeType || !strings.Contains(d[0].Message, want) {
				t.Fatalf("want one refused-name diagnostic, got %s", describe(d))
			}
		})
	}
}
