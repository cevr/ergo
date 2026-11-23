package sketchfront

import (
	"fmt"
	"path"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// stdRootDir is the folder a std package's files are named under when the
// package is the root of a test build (CheckStd): /std/<import path>/<file>.
const stdRootDir = "/std"

// CheckStd is the std package at importPath as the root of a program: the
// package as std is checked (check.Std, in-package, with std's authority),
// so a test build of the analysis runs the package's own laws and tests
// natively, as `ergo test` runs a user package's (NATIVE-LAWS slice 2: a
// std law runs on its own source). A folder check of a std package checks
// it as a user package, which std's own declarations refuse (S108). The
// files are named stdRootDir/<importPath>/<file>; the other std packages it
// imports stay std, which every program imports.
func CheckStd(importPath string) (Analysis, error) {
	var sources map[string]string
	var pkg *check.Package
	if importPath == stdlib.PreludePath {
		// No import names the prelude: its checked package is the one
		// every package sees (NIH-75).
		sources, pkg = stdlib.Prelude(), check.PreludePackage()
	} else {
		var ok bool
		if sources, ok = stdlib.Package(importPath); !ok {
			return Analysis{}, fmt.Errorf("sketchfront: %s is no std package", importPath)
		}
		var err error
		if pkg, err = check.Std.Import(importPath); err != nil {
			return Analysis{}, err
		}
	}
	dir := path.Join(stdRootDir, importPath)
	root := &loadedPackage{path: importPath, dir: dir, pkg: pkg}
	texts := map[string]string{}
	for _, f := range pkg.Files() {
		text, ok := sources[f.Name]
		if !ok {
			return Analysis{}, fmt.Errorf("sketchfront: std %s has no source for its file %q", importPath, f.Name)
		}
		rel := path.Join(importPath, f.Name)
		root.files = append(root.files, loadedFile{rel: rel, abs: path.Join(dir, f.Name), text: text, syntax: f})
		texts[rel] = text
	}
	p := &program{root: root, texts: texts, dir: stdRootDir, platform: hostPlatform()}
	return Analysis{program: p, pkg: pkg}, nil
}
