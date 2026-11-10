package sketchfront

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// CheckedPackage is one package of a program read from a folder, as the
// loader read and checked it. A tool that reads the checker's model of a
// program, such as the law guard of scripts/examples, takes it from here
// and never parses or checks again.
type CheckedPackage struct {
	// Path is the package's import path; the root package's is its
	// folder's module path, or "main" with no module.
	Path string
	// Dir is the package folder, absolute.
	Dir string
	// Files are the package's files in name order.
	Files []CheckedFile
	// Package is the checked package; nil when the package, or a package
	// it imports, has diagnostics, or when it was not checked.
	Package *check.Package
}

// CheckedFile is one source file of a CheckedPackage.
type CheckedFile struct {
	// Rel is the slash-separated path relative to the module root, as a
	// diagnostic names it.
	Rel string
	// Syntax is the parsed file; nil when it does not parse.
	Syntax *syntax.File
}

// Root is the program's root package, from a folder or from one source
// text (whose file has no Rel); its Package is nil when it did not check.
func (a Analysis) Root() CheckedPackage {
	if ps := a.Packages(); len(ps) > 0 {
		return ps[len(ps)-1]
	}
	c := CheckedPackage{Path: "main", Files: []CheckedFile{{Syntax: a.file}}}
	if len(a.Diagnostics) == 0 {
		c.Package = a.pkg
	}
	return c
}

// Checked are the checked packages of the program in import dependency
// order, the root last: the one package of a source text (Check), or the
// checked packages of a folder (Packages); nil when the program does not
// check.
func (a Analysis) Checked() []*check.Package {
	if a.program == nil {
		if a.pkg == nil || len(a.Diagnostics) > 0 {
			return nil
		}
		return []*check.Package{a.pkg}
	}
	var out []*check.Package
	for _, p := range a.Packages() {
		if p.Package == nil {
			return nil
		}
		out = append(out, p.Package)
	}
	return out
}

// Packages are the packages of a program read from a folder (CheckDir) in
// import dependency order, the root package last; nil for one source text
// (Check).
func (a Analysis) Packages() []CheckedPackage {
	if a.program == nil || a.program.root == nil {
		return nil
	}
	var out []CheckedPackage
	for _, p := range append(append([]*loadedPackage{}, a.program.imports...), a.program.root) {
		c := CheckedPackage{Path: p.path, Dir: p.dir}
		if !p.broken {
			c.Package = p.pkg
		}
		for _, f := range p.files {
			c.Files = append(c.Files, CheckedFile{Rel: f.rel, Syntax: f.syntax})
		}
		out = append(out, c)
	}
	return out
}
