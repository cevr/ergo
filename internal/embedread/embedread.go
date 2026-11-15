// Package embedread makes a read of an embedded source file visible to the
// mutant runner (GATE2 GT8b). The runner's coverage pass selects, for a
// row of a file, the tests that open that file (scripts/mutants, reads.go:
// the go command's test log of each os.Open). A file a package embeds
// (std's .ergo sources, which every check of a program reads) is read
// from the binary, not opened, so no test log names it. With Variable set,
// as only the coverage pass sets it, Note opens and closes the checkout
// file the embedded one was built from, so the test log names it. Without
// it Note does nothing, so builds and runs are as before.
package embedread

import (
	"os"
	"path/filepath"
	"runtime"
)

// Variable turns Note on. The mutant runner's coverage pass sets it.
const Variable = "ERGO_MUTANTS_EMBED_READS"

var on = os.Getenv(Variable) != ""

// Dir is the directory of the calling package's source file, or "" when
// the binary was built with -trimpath (no checkout path to name).
func Dir() string {
	_, file, _, ok := runtime.Caller(1)
	if !ok || !filepath.IsAbs(file) {
		return ""
	}
	return filepath.Dir(file)
}

// Note records a read of the embedded file name (slash-separated, relative
// to dir, the embedding package's directory) when Variable is set.
func Note(dir, name string) {
	if !on || dir == "" {
		return
	}
	if f, err := os.Open(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
		f.Close()
	}
}
