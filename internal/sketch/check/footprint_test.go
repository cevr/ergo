package check_test

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// show prints a footprint: its capabilities by name, then its flags, or
// "pure" when it is empty.
func show(fp check.Footprint) string {
	if fp.Empty() {
		return "pure"
	}
	var parts []string
	for _, c := range fp.Caps {
		parts = append(parts, c.(*check.Named).Obj.Name)
	}
	if fp.Opaque {
		parts = append(parts, "opaque")
	}
	if fp.Tasks {
		parts = append(parts, "tasks")
	}
	return strings.Join(parts, " ")
}
