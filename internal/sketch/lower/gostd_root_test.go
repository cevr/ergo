package lower

import (
	"go/build"
	"testing"
)

// TestStdTypedWithoutOwnGOROOT: a process with no GOROOT of its own (an
// ergo built with -trimpath, internal/ergobuild) types Go's standard
// library from the root UseStdRoot resolves, as a process with a GOROOT
// does, so its closure keys are the typed ones. Mutant:
// std-root-fallback-dropped (stdContext keeps the empty GOROOT, std is read
// untyped and every foreign key differs from a typed process's).
func TestStdTypedWithoutOwnGOROOT(t *testing.T) {
	want := build.Default.GOROOT
	if want == "" {
		t.Fatal("the test binary has no GOROOT: the test needs the one it compares against")
	}
	UseStdRoot(func() string { return want })
	defer UseStdRoot(nil)
	ctx := build.Default
	ctx.GOROOT = ""
	got := stdContext(ctx, GoTarget{GOOS: "linux", GOARCH: "amd64"})
	if got.GOROOT != want {
		t.Fatalf("std's GOROOT without one of the process's own = %q, want the resolved %q", got.GOROOT, want)
	}
	if _, err := got.Import("sync/atomic", "", build.FindOnly); err != nil {
		t.Fatalf("std is not found from the resolved GOROOT: %v", err)
	}
}
