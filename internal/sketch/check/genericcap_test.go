package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestGenericCapabilityDiagnostics: the two refusals of a generic
// operation type in a capability row name the form to write (types.md,
// Capabilities 7). Rows for the text only: TestGenericCapabilitiesAgree
// (lower) holds the rule that each concrete instance is its own
// capability.
func TestGenericCapabilityDiagnostics(t *testing.T) {
	const store = "// law-exempt: a diagnostic row.\ntype Store[T] {\n\tPut(v T),\n}\n"
	rows := []struct{ name, src, want string }{
		{"bare generic name", store + "func f() needs Store {\n}\n", "S108:5 generic capability Store needs type arguments: write Store[…]"},
		{"type parameter argument", store + "func f[T]() needs Store[T] {\n}\n", "S108:5 a capability's type arguments are concrete types, and Store[T] names a type parameter"},
		{"bare generic context key", store + "func f() {\n\twith context { Store: 1 } {\n\t}\n}\n", "S108:6 generic capability Store needs type arguments"},
		// One instance's binding never discharges another's: the
		// capability set keys an instance by its arguments (capset.go).
		{"another instance's binding", store + "type Box { n int }\nfunc Put(b Box, v string) {}\nfunc f() needs Store[int] {\n\tStore[int].Put(1)\n}\nfunc main() {\n\twith context { Store[string]: Box{} } {\n\t\tf()\n\t}\n}\n", "S102:12 Store[int]"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
