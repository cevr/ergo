package lower

import (
	"go/build"
	"strings"
	"testing"
)

// TestStaleNoteNamesUntypedStd (LEDGER-SUPERSEDE): where this process
// cannot type Go's standard library (no GOROOT, or one with no std source),
// a missing or stale entry's note says so, since its keys then differ from
// a ledger written where std was typed though nothing changed; where std is
// typed the note is the plain one.
//
// Mutant (lower mutants.json): lsd-untyped-unsaid.
func TestStaleNoteNamesUntypedStd(t *testing.T) {
	saved := hostStd
	t.Cleanup(func() { hostStd = saved })
	root := func(dir string) func() *stdTypes {
		return func() *stdTypes {
			ctx := build.Default
			ctx.GOROOT = dir
			return &stdTypes{ctx: ctx}
		}
	}
	for _, c := range []struct {
		name, goroot, want string
	}{
		{"no GOROOT", "", "no GOROOT: Go's standard library is read untyped"},
		{"a GOROOT with no std source", "/nonexistent/loop-probe-x", "has no source at GOROOT /nonexistent/loop-probe-x"},
	} {
		hostStd = root(c.goroot)
		v := judgeFunc("m.L", "", LedgerEntry{}, false, "h1:x", "", "ergo.laws: ", func() {})
		if v.State != "missing" || !strings.Contains(v.Why, c.want) || !strings.HasSuffix(v.Why, "; run ergo test") {
			t.Errorf("%s: the note is %q; want it to say %q", c.name, v.Why, c.want)
		}
	}
	hostStd = saved
	if hostStd().ctx.GOROOT != "" {
		if v := judgeFunc("m.L", "", LedgerEntry{}, false, "h1:x", "", "ergo.laws: ", func() {}); v.Why != "ergo.laws: m.L is missing; run ergo test" {
			t.Errorf("with std typed, the note is %q; want the plain one", v.Why)
		}
	}
}
