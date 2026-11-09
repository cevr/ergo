package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
)

// TestGoStdBindingsCheckClean resolves every declaration of every embedded
// binding of Go's standard library, as a use of each would: a binding that
// ergo bind go wrote checks clean (IG1's acceptance, moved to IG2 with the
// checker that reads bindings). Mutant it alone catches: a binding form
// the checker does not read (a receiver, a host field type, go.Ref, a Go
// import alias such as gocontext).
func TestGoStdBindingsCheckClean(t *testing.T) {
	set := check.NewGoBindings(nil)
	paths := gostd.Paths()
	if len(paths) == 0 {
		t.Fatal("no embedded binding")
	}
	for _, path := range paths {
		if !set.ResolveAll(path) {
			t.Errorf("%s: listed but has no binding", path)
		}
	}
	// One line per distinct message, with its count and first place.
	first, count := map[string]string{}, map[string]int{}
	var order []string
	for _, d := range set.Diagnostics() {
		msg := d[strings.Index(d, " ")+1:]
		msg = msg[strings.Index(msg, " ")+1:]
		if count[msg] == 0 {
			first[msg] = d
			order = append(order, msg)
		}
		count[msg]++
	}
	for _, msg := range order {
		t.Errorf("%d× %s", count[msg], first[msg])
	}
}
