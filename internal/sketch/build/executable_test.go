package build

import (
	"bytes"
	"strings"
	"testing"
)

// TestLinkCachedLinksOnce: a module linked into its executable entry is
// held there until Release, and linked again with the same toolchain runs
// no link action; another toolchain (here a build tag) has its own entry.
// It kills bins-relinked, bins-unheld and bins-variant-shared.
// It builds Go because whether the go command relinks shows only in the -x
// trace of a real link.
func TestLinkCachedLinksOnce(t *testing.T) {
	needLocks(t)
	if testing.Short() {
		t.Skip("real links run in the full tier")
	}
	root := t.TempDir()
	c := Cache{Root: root}
	in := baseInput()
	tc := hostToolchain()
	tc.GOFLAGS = "-x"
	link := func(tc Toolchain) (Module, int) {
		t.Helper()
		m, err := c.Module(in, emitFor(in, nil))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Release()
		var trace bytes.Buffer
		linked, err := c.LinkCached(m, tc, "app.test", &trace)
		if err != nil {
			t.Fatalf("%v\n%s", err, trace.String())
		}
		return linked, strings.Count(trace.String(), "/link -o ")
	}
	first, n := link(tc)
	if n != 1 {
		t.Fatalf("the first link ran %d link actions", n)
	}
	entry := strings.TrimSuffix(first.Executable, "/app.test")
	if free(t, entry) {
		t.Fatal("the linked executable's entry is not held")
	}
	want := runProgram(t, first.Executable)
	first.Release()
	if !free(t, entry) {
		t.Fatal("the entry is still held after Release")
	}
	second, n := link(tc)
	defer second.Release()
	if n != 0 || second.Executable != first.Executable {
		t.Fatalf("the second link ran %d link actions into %s; want none into %s", n, second.Executable, first.Executable)
	}
	if got := runProgram(t, second.Executable); got != want {
		t.Fatalf("the reused executable printed %q; want %q", got, want)
	}
	tagged := tc
	tagged.Tags = []string{"ergotag"}
	other, _ := link(tagged)
	defer other.Release()
	if other.Executable == first.Executable {
		t.Fatal("a link with other build tags shares the entry")
	}
}
