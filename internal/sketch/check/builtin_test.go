package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestBuiltinBindingShapes holds the diagnostics of a Go builtin binding
// (builtin.go): one row per refusal, its text. An accepted binding of each
// builtin is TestBuiltinsAgree's prelude (lower), which runs it.
func TestBuiltinBindingShapes(t *testing.T) {
	for _, row := range []struct{ src, want string }{
		{"extern go append func push[T](xs []T, x []T) []T\n", "the extern push binds Go's builtin append, whose shape is func [T](xs []T, x T) []T"},
		{"extern go copy func into[T](dst []T, src []T) int\n", "the extern into binds Go's builtin copy, whose shape is func [T](dst *[]T, src []T) int"},
		{"extern go delete func drop[K comparable, V](m Map[K, V], k V) Map[K, V]\n", "the extern drop binds Go's builtin delete, whose shape is func [K comparable, V](m Map[K, V], k K) Map[K, V]"},
		{"extern go min func least(a bool, b bool) bool\n", "the extern least binds Go's builtin min, whose shape is func (a T, b T) T, T an ordered basic type"},
		{"extern go len func size[T](xs []T) int fails Stop\n\ntype Stop {}\n", "the extern size binds Go's builtin len, whose shape is func with no fails or needs row"},
		{"extern go print func say(s string)\n", "an extern target is an import path, a dot and a function name, or a Go builtin"},
		{"extern go len let Size int\n", "a Go builtin is bound by a func"},
	} {
		_, diags := check.CheckSource(row.src)
		found := false
		for _, d := range diags {
			found = found || strings.Contains(d.Message, row.want)
		}
		if !found {
			t.Errorf("%q: want a diagnostic containing %q, got %v", row.src, row.want, diags)
		}
	}
}
