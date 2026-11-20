package build

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCommandNamesTheIRByTheModule: go build's -ergoir flag (Command, the
// fallback when the direct path refuses, as under GOFLAGS) names each
// package's IR file relative to the module's folder, where go build runs
// the compile, so the same module written into another folder, as each
// edit writes it, gives the same flags, and go build reuses every
// unchanged package's compile. The flag had the absolute path, so an edit
// of a split program compiled every package again (GP-SPLIT-EDIT). The IR's
// content reaches go build's key through its package's Go file
// (lower.irKeyed, TestIRDigestInItsPackage). It checks in process.
//
// Mutant: ir-flag-absolute (the flag names the IR file's absolute path).
func TestCommandNamesTheIRByTheModule(t *testing.T) {
	module := func(dir string) Module {
		for _, rel := range []string{".", "pkg/x"} {
			if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, rel, IRFile), []byte(`{"v":1}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return Module{Dir: dir}
	}
	tc := Toolchain{Go: "go"}
	a := module(filepath.Join(t.TempDir(), "modules", "one"))
	b := module(filepath.Join(t.TempDir(), "modules", "two"))
	ca, cb := tc.Command(a, "/out/app"), tc.Command(b, "/out/app")
	if !slices.Equal(ca.Args, cb.Args) {
		t.Fatalf("one module in two folders gives two commands:\n%q\n%q", ca.Args, cb.Args)
	}
	flags := 0
	for _, arg := range ca.Args {
		if !strings.HasPrefix(arg, "-gcflags=") {
			continue
		}
		flags++
		_, file, ok := strings.Cut(arg, "-ergoir=")
		file = strings.TrimSuffix(file, "'")
		if !ok || filepath.IsAbs(file) {
			t.Fatalf("flag %q names no IR file relative to the module", arg)
		}
		if _, err := os.Stat(filepath.Join(a.Dir, file)); err != nil {
			t.Fatalf("flag %q: the IR file is not at that path from the module's folder: %v", arg, err)
		}
	}
	if flags != 2 {
		t.Fatalf("%d -ergoir flags for 2 IR files: %q", flags, ca.Args)
	}
}
