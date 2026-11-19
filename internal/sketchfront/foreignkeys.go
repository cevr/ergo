package sketchfront

import (
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// Foreign keys (lower/foreignkey.go; LAWFFI r2 §3.5, LFL): the ledger's
// pin of the Go code a law reaches. The keys are read from the module's
// go.mod and go.sum as the build reads them (gomod.go), the pinned
// toolchain, and, for a local replacement or go.mod's own module, the
// package's Go and assembly files, whose declarations the function
// reaches are keyed (lower/goclosure.go). Prior art: Go's own build cache
// keys an action by the toolchain and the hashes of its inputs, and go.sum
// pins a module by the h1: hash of its files; the ledger takes the same
// keys.

// goToolchain is the toolchain a key of Go's standard library names; a
// test replaces it, as a toolchain upgrade would.
var goToolchain = toolchain.Version

// foreignKeys are the foreign keys of a's program, its files read through
// fsys (the host's when nil).
func foreignKeys(a Analysis, fsys FS) lower.ForeignKeys {
	src := lower.ForeignKeySource{Toolchain: goToolchain}
	if a.program != nil {
		src.Module = a.program.goModule
	}
	if fsys == nil {
		fsys = hostFS{}
	}
	src.ReadDir = func(dir string) []lower.GoSourceFile { return readGoDir(fsys, dir) }
	return src.Keys()
}

// readGoDir is the Go and assembly files of the package in dir, its tests
// excluded; nil when dir cannot be read.
func readGoDir(fsys FS, dir string) []lower.GoSourceFile {
	entries, err := fsys.ReadDir(filepath.FromSlash(dir))
	if err != nil {
		return nil
	}
	out := []lower.GoSourceFile{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasSuffix(n, "_test.go") || !strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, ".s") {
			continue
		}
		data, err := fsys.ReadFile(filepath.Join(filepath.FromSlash(dir), n))
		if err != nil {
			return nil
		}
		out = append(out, lower.GoSourceFile{Name: n, Data: data})
	}
	return out
}
