package eval

import (
	"io/fs"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestEveryPureStdExternHasANative: each pure Go function a std body
// calls has a native of the check-time evaluator, so a law or test over it
// runs at ergo check, not only at ergo test (STD-SURVEY ST4; PKG G1, G2).
// A pure Go function is a std extern whose runtime/v1 body is marked pure
// (check.BodyOf(f).Pure; stdlib.md, Pure bodies), or a function of a Go
// package of v1.PureImports that a std package reaches through import go,
// less the members v1.PureImportsExcept() names. A call of one with no
// native stops the evaluation as "a Go function (name), even a pure one".
// The callees are read from std's calls, so a pure extern counts wherever
// it is declared: in a std package, in the prelude (the built-in types'
// operations, b25), or in a package it moves to.
// Mutants only it kills (testdata/mutants.json): st4-native-missing,
// prelude-append-string-native.
func TestEveryPureStdExternHasANative(t *testing.T) {
	missing := map[string]bool{}
	for _, path := range stdPaths(t) {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range pkg.Funcs {
			if check.BodyOf(f).Pure && nativeOf(f) == nil {
				missing[path+"."+f.Name] = true
			}
		}
		for _, c := range pkg.Info.Calls {
			f := c.Func
			if f == nil || f.Decl == nil || f.Decl.Extern == nil || f.Pkg == nil {
				continue
			}
			if f.Pkg.Std() {
				if check.BodyOf(f).Pure && !check.IsUnreachable(c) && !runsExtern(f) {
					missing[f.Pkg.Path+"."+f.Name+" (called from "+path+")"] = true
				}
				continue
			}
			goPath, name := f.Decl.Extern.Split()
			if goPath == "" || !slices.Contains(v1.PureImports, goPath) || slices.Contains(v1.PureImportsExcept()[goPath], name) {
				continue
			}
			if goNativeOf(f) == nil {
				missing[goPath+"."+name+" (import go, from "+path+")"] = true
			}
		}
	}
	var out []string
	for k := range missing {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 0 {
		t.Errorf("%d pure Go functions std reaches have no evaluator native:\n%s", len(out), strings.Join(out, "\n"))
	}
}

// runsExtern reports whether the evaluator runs the extern f without its
// Go body, as planOf chooses: a built-in collection operation, Go's
// append, or a native.
func runsExtern(f *check.Func) bool {
	return preludeCollection(f) || check.AppendsString(f) || check.GoBuiltin(f) == "append" || nativeOf(f) != nil || goNativeOf(f) != nil
}

// stdPaths are the std package paths of the embedded sources, gostd's
// generated bindings left out: a std package reaches them through import
// go, which the test reads from its calls.
func stdPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(std.Sources, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == "." {
			return err
		}
		if p == "gostd" || strings.HasPrefix(p, "gostd/") {
			return fs.SkipDir
		}
		entries, err := fs.ReadDir(std.Sources, p)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".ergo") && !strings.HasSuffix(e.Name(), "_test.ergo") {
				out = append(out, p)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
