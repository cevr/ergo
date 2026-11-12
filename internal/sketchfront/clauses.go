package sketchfront

import (
	"runtime"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
)

// CheckDirOptions checks the package in the folder dir like CheckDir, in
// the checker modes opts selects, with contents replacing the text of
// existing files (absolute, clean keys) as CheckDirOverlay does. ergo fix
// reads it.
func CheckDirOptions(dir string, contents map[string]string, opts check.Options) (Analysis, error) {
	return checkDirWith(overlayFS{contents: contents}, dir, gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, opts)
}

// Clause is an inferred clause of a checked function (check.Options.Clauses):
// the edit that writes it into File, the path relative to the module root
// for a program read from a folder, empty for one source text.
type Clause struct {
	File string
	Fix  report.Fix
}

// Clauses lists the inferred clauses of every checked package of the
// analysis, in dependency order, under check.Options.Clauses.
func (a Analysis) Clauses() []Clause {
	var out []Clause
	if a.program == nil {
		if a.pkg != nil {
			for i := range a.pkg.Clauses {
				out = append(out, Clause{Fix: *reportFix(&a.pkg.Clauses[i])})
			}
		}
		return out
	}
	pkgs := append(append([]*loadedPackage(nil), a.program.imports...), a.program.root)
	for _, p := range pkgs {
		if p == nil || p.pkg == nil {
			continue
		}
		for i := range p.pkg.Clauses {
			fix := &p.pkg.Clauses[i]
			out = append(out, Clause{File: p.files[fix.File].rel, Fix: *reportFix(fix)})
		}
	}
	return out
}

// SourcePaths maps the module-relative path of every source file of the
// program's loaded packages (a Diagnostic's File) to its admitted absolute
// path; nil for one source text.
func (a Analysis) SourcePaths() map[string]string {
	if a.program == nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range append([]*loadedPackage{a.program.root}, a.program.imports...) {
		if p == nil {
			continue
		}
		for _, f := range p.files {
			out[f.rel] = f.abs
		}
	}
	return out
}

// reportFix projects a checker clause edit onto the report's fix.
func reportFix(f *check.ClauseFix) *report.Fix {
	if f == nil {
		return nil
	}
	return &report.Fix{
		Clause:    f.Clause,
		Function:  f.Function,
		Row:       f.Row,
		InsertAt:  f.InsertAt,
		Text:      f.Text,
		Imports:   f.Imports,
		Signature: f.Signature,
	}
}
