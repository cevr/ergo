// Package lower turns a checked Ergo package into Go source. It reads only
// what the checker decided: the checker's Package and Info for the syntax
// files it checked. It never parses, infers or re-checks (spec D1).
//
// This file holds the checked unit and its source anchors. A source anchor
// names an admitted source file in a //line directive, so the Go compiler,
// the debugger and a panic's stack trace report the .ergo file, not the
// generated Go. module.go assembles the emitted Go module; decls.go,
// body.go and expr.go lower declarations, statements and expressions.
//
// Prior art: Gleam and ReScript lower a typed tree to host source and keep
// source positions on it; Borgo emits Go with //line directives; Go's own
// cgo and yacc outputs use //line the same way.
package lower

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Source is one admitted source file: the absolute path that diagnostics and
// //line directives name, and the bytes the checker read from it.
type Source struct {
	Path string
	Text string
}

// Unit is the checked input of lowering: the checker's package and the
// syntax files and sources it checked, in the package's file order.
// Files[i] was parsed from Sources[i].Text.
//
// Imports are the units of the user packages the program reaches, directly
// or through another import, in any order; each has no Imports of its own.
// A std package has no unit: the lowering reads its declarations from the
// checker. A program that reaches a user package with no unit is an error.
type Unit struct {
	Package *check.Package
	Files   []*syntax.File
	Sources []Source
	Imports []Unit
}

// Validate reports a unit whose files and sources are not aligned, whose
// source paths cannot be anchored or are admitted twice, or whose imports
// are not distinct user packages with flat units.
func (u Unit) Validate() error {
	seen := map[string]bool{}
	if err := u.validateFiles(seen); err != nil {
		return err
	}
	pkgs := map[*check.Package]bool{u.Package: true}
	for _, imp := range u.Imports {
		switch {
		case imp.Package == nil:
			return fmt.Errorf("lower: an imported unit has no package")
		case imp.Package.Std():
			return fmt.Errorf("lower: package %q is std and has no unit", imp.Package.Path)
		case pkgs[imp.Package]:
			return fmt.Errorf("lower: package %q is admitted twice", imp.Package.Path)
		case len(imp.Imports) > 0:
			return fmt.Errorf("lower: the unit of package %q has imports of its own; the root unit lists every import", imp.Package.Path)
		}
		pkgs[imp.Package] = true
		if err := imp.validateFiles(seen); err != nil {
			return err
		}
	}
	return nil
}

// validateFiles validates u's own files and sources against the paths seen.
func (u Unit) validateFiles(seen map[string]bool) error {
	if len(u.Files) == 0 || len(u.Files) != len(u.Sources) {
		return fmt.Errorf("lower: %d syntax files for %d sources", len(u.Files), len(u.Sources))
	}
	for i, s := range u.Sources {
		if u.Files[i] == nil {
			return fmt.Errorf("lower: source %s has no syntax file", s.Path)
		}
		if err := ValidatePath(s.Path); err != nil {
			return err
		}
		if seen[s.Path] {
			return fmt.Errorf("lower: source %s is admitted twice", s.Path)
		}
		seen[s.Path] = true
	}
	return nil
}

// ValidatePath reports whether path can be named in a //line directive.
// The directive has no quoting: it ends at the line end, and Go source must
// be valid UTF-8 without a byte order mark. So a path with a control
// character (a line feed would end the comment and turn the rest of the
// name into Go code), invalid UTF-8 or U+FEFF is refused, as is a relative
// or unclean path, which the Go compiler would resolve against the
// generated file's directory instead of the source's.
func ValidatePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("source path is empty")
	case !utf8.ValidString(path):
		return fmt.Errorf("source path %q is not valid UTF-8, so a //line directive cannot name it", path)
	case strings.ContainsFunc(path, func(r rune) bool { return unicode.IsControl(r) || r == 0xFEFF }):
		return fmt.Errorf("source path %q contains a control character, so a //line directive cannot name it", path)
	case strings.Contains(path, "*/"):
		return fmt.Errorf("source path %q contains */, so an inline /*line*/ directive cannot name it", path)
	case !filepath.IsAbs(path) || filepath.Clean(path) != path:
		return fmt.Errorf("source path %q is not absolute and clean", path)
	}
	return nil
}

// Directive is the //line directive that maps the next Go line to line of
// path, and to column when column is positive. It starts in column 1, as the
// Go compiler requires, and ends with a line feed. The path must satisfy
// ValidatePath.
func Directive(path string, line, column int) string {
	if column > 0 {
		return fmt.Sprintf("//line %s:%d:%d\n", path, line, column)
	}
	return fmt.Sprintf("//line %s:%d\n", path, line)
}

// InlineDirective is the inline /*line*/ directive that maps the character
// after it to line and column of path, wherever it stands in a Go line
// (cmd/compile, Compiler directives). The path must satisfy ValidatePath,
// which refuses a path that holds */.
func InlineDirective(path string, line, column int) string {
	return fmt.Sprintf("/*line %s:%d:%d*/", path, line, column)
}
