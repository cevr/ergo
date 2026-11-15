package check

import (
	"ergo.local/prototype/internal/sketch/derive"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Derived types (patterns.md, Derivation). Before the checker meets a
// package's files, derive.Expand evaluates each `type T derive E`, splices
// the bundle T owns after it, and removes the package's plan functions,
// which exist at check time only. An imported package's plan functions
// come from its derive record (Package.Derive). The bundle is then checked
// as written code.

// expandDerives runs the expansion and returns its diagnostics, each with
// File as the index of its file in files.
func expandDerives(path string, files []*syntax.File, imp Importer) (*derive.Package, []Diagnostic) {
	lookup := func(p string) *derive.Package {
		pkg, err := imp.Import(p)
		if err != nil || pkg == nil {
			return nil
		}
		return pkg.Derive
	}
	rec := derive.Expand(path, files, lookup)
	var ds []Diagnostic
	for _, d := range rec.Diags {
		index := -1
		for i, f := range files {
			if f == d.File {
				index = i
			}
		}
		if index < 0 {
			continue
		}
		line, col := d.File.Position(d.Pos)
		ds = append(ds, Diagnostic{Code: CodeType, Message: d.Msg, File: index, Offset: int(d.Pos), Length: int(d.End - d.Pos), Line: line, Column: col})
	}
	return rec, ds
}
