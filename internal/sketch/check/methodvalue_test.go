package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestMethodValueRefusals (types.md, Methods 2; CV1): x.f not called is
// the function f on x's type with x bound, except where the copy it keeps
// of x would alias, move or duplicate x, or where f has a type parameter
// x's type does not bind; each is one S108 at f's name that names the
// closure to write. Each row is the one that catches its refusal's
// removal (cv-method-value-ptr-allowed, cv-method-value-own-allowed,
// cv-method-value-generic-allowed in the package ledger); a value that is
// admitted is the f_method_value row.
func TestMethodValueRefusals(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		text      string
	}{
		{"f_method_value", "type Acc { N int }\nfunc Add(a Acc, b int) int { a.N + b }\nfunc f(a Acc) int {\n\tlet g = a.Add\n\tg(2)\n}\n", nil, ""},
		{"f_method_value_ptr", "type Acc { N int }\nfunc Bump(a mut Acc, b int) { a.N = a.N + b }\nfunc f(a Acc) {\n\tlet g = a.Bump\n\tg(2)\n}\n", []string{"S108:4"}, "a method value of Bump borrows a (mut Acc); write the closure func(…) { a.Bump(…) }"},
		{"f_method_value_own", "type File {} implements Closer\nfunc Close(_ File) {}\nfunc Name(f File) string { \"f\" }\nfunc use(f File) string {\n\tlet g = f.Name\n\tg()\n}\n", []string{"S108:5"}, "would keep f, a resource"},
		{"f_method_value_generic", "type Box { N int }\nfunc Pick[T](b Box, x T) T { x }\nfunc f(b Box) {\n\tlet g = b.Pick\n\t_ = g\n}\n", []string{"S108:4"}, "leaves its type parameter T unbound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("want %v, got %s", tc.want, describe(d))
			}
			if tc.text != "" && !strings.Contains(d[0].Message, tc.text) {
				t.Errorf("message %q does not name %q", d[0].Message, tc.text)
			}
		})
	}
}
