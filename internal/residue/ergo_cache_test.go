package residue_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// TestTestsUseAPrivateErgoCache: a test process, and every ergo command it
// starts with its environment, resolves the emission cache to a directory
// of its own under the temporary directory, never to the user's cache.
// Every test package's TestMain calls testinputs.Main
// (internal/testinputs/guard), which sets build.RootVariable, so this one
// process stands for all of them. Before, tests and gate steps wrote 18,357
// entries (20 GB) into ~/.cache/ergo/modules in a day (measured
// 2026-11-12). Mutants: testinputs sets no private cache
// (cache-bound-tests-share-user-cache); DefaultRoot ignores the variable
// (cache-bound-root-variable-ignored).
func TestTestsUseAPrivateErgoCache(t *testing.T) {
	private, err := build.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(build.RootVariable, "")
	user, err := build.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(private)
	if err != nil {
		t.Fatalf("the test process's cache %s: %v", private, err)
	}
	if private == user || !strings.HasPrefix(resolved, temp+string(filepath.Separator)) {
		t.Fatalf("the test process resolves the emission cache to %s (the user's is %s); a test must not reach the user's cache", private, user)
	}
}
