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

// Source is one admitted source file: its absolute path and the bytes the
// checker read from it. emitName derives the emitted name from that path.
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
//
// Go is the root unit's Go requirements (IG7): the Go modules its import go
// packages outside Go's standard library come from.
type Unit struct {
	Package *check.Package
	Files   []*syntax.File
	Sources []Source
	Imports []Unit
	Go      GoModule
	// Instances are the site instances of rewrite laws that the test
	// build runs as cases of their own (rewritelaw.go); the run build
	// ignores them.
	Instances []RewriteInstance
	// Posts are post laws (rewrite.go) that the test build runs from
	// reached states: each case draws a prefix of the owner's operations
	// before the law, so a fact holds after its operation from every
	// state, as the lowering uses it. The run build ignores them.
	Posts []*check.Law
	// ImportLaws are law runs of imported units' packages that the test
	// build runs beside the root's own: the laws the run build asked of a
	// provider an import declares (a module's own codec, a cell writer),
	// which the root's test build checks at the root's site instances,
	// since a library has no sites of its own (RewriteChecks). The run
	// build ignores them.
	ImportLaws []check.LawRun
	// Rewrite is the evidence of the law runs the run build may rewrite
	// with (rewrite.go); nil rewrites nothing. The test build ignores it.
	Rewrite RewriteEvidence
	// Rewrites is ergo build's -rewrites switch for the rewrites over
	// functions of the IR path (ir_rewrite.go): "" or "on" (proved and
	// validated laws), "proved" (proved laws only), "off" (the reference:
	// every site keeps its left side), "canary[=N]" (on, and each N-th
	// call at a rewritten site checks the left side too; ParseRewrites).
	// The test build ignores it: it shadows every matched site.
	Rewrites string
	// AllLaws asks RewriteChecks for every rewrite and post law of the
	// unit, not only those its run build's sites use: std's ledger
	// (FreshStdLedger) holds each law proved once, for every program.
	AllLaws bool
	// Root is the directory that the emitted source names are relative to
	// (emitName): the module root, the directory of ergo.mod, or for a
	// program of one file its directory. Empty keeps the admitted paths.
	// The root unit's Root holds for its imports too.
	Root string
	// Copies, when set, receives the form of each Append and owned-form
	// site the lowering decides (handover.go; ergo build -report copies).
	Copies *CopyReport
	// Raw writes the emitted Go as the lowerer lays it out, without
	// go/format, for the dev and test profile (test-speed FT5): go/format
	// is the largest part of a lowering (LW1), and its output is the
	// input's tokens laid out again. The //line mapping is the same
	// (anchorLines runs on either).
	Raw bool
	// CoverMarks writes a cover mark (CoverMark) where a std body would
	// write a //line directive if it had them (GATE2 GT7b): a full-line
	// comment, which changes no Go position, so the mutant runner's
	// coverage pass can map a std body's Go lines to its .ergo lines. Only
	// a coverage build sets it (internal/sketch/build CoverVariable); its
	// emission key holds CoverMarkFlag. Without it the emitted Go is as
	// before, byte for byte.
	CoverMarks bool
	// NoViews is ergo build -views=off: no Go call takes a view of a
	// string's bytes, in the run build or the test build (view.go).
	NoViews bool
	// NoFallback is ergo build -fallback=off (ERGO_FALLBACK=off): an IR
	// lowering that keeps any body on the text path is refused with a
	// FallbackRefused naming each (ir_fallback.go; F8's precondition).
	NoFallback bool
	// lib is the Go package name of a library lowering (LowerIRLibrary),
	// "" for a program: the build lowerer writes that package, with each
	// exported function's Go entry, in place of main (lowerer.lib).
	lib string
}

// coverMarksAll lowers every unit with CoverMarks. Only this package's
// TestMain sets it, in the mutant runner's coverage pass (the build
// package's CoverVariable set), whose programs the test harness links
// with -cover (support_test.go).
var coverMarksAll bool

// CoverMarkFlag is the emission flag (build.Input.Flags) of a unit lowered
// with CoverMarks.
const CoverMarkFlag = "cover-marks"

// coverMarkPrefix starts a cover mark.
const coverMarkPrefix = "//ergo:cover "

// CoverMark is the cover mark of path, line and column: a comment on a
// line of its own; the Go lines after it come from that .ergo line on,
// as after a //line directive, until the next mark or directive.
func CoverMark(path string, line, column int) string {
	return fmt.Sprintf("%s%s:%d:%d\n", coverMarkPrefix, path, line, column)
}

// Validate reports a unit whose files and sources are not aligned, whose
// source paths cannot be anchored or are admitted twice, or whose imports
// are not distinct user packages with flat units.
func (u Unit) Validate() error {
	if _, _, err := ParseRewrites(u.Rewrites); err != nil {
		return fmt.Errorf("lower: %w", err)
	}
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
// or unclean path: a source is admitted by its absolute, clean path, and
// emitName derives the name the emitted Go holds from it.
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

// emitName is the name of the admitted source path that the emitted Go
// holds, in its //line directives and its declaration and call tables:
// path relative to root with forward slashes (binsize BS6). Then the same
// program in two directories lowers to the same bytes and builds the same
// executable, and a defect report names main.ergo:4. The Go compiler keeps
// a relative //line name as written (-trimpath rewrites only absolute
// ones), so the report matches the table. A path outside root, or an
// empty root, keeps the admitted path.
func emitName(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.ToSlash(rel)
}

// Directive is the //line directive that maps the next Go line to line of
// path, and to column when column is positive. It starts in column 1, as the
// Go compiler requires, and ends with a line feed. The path is the emitName
// of a path that satisfies ValidatePath.
func Directive(path string, line, column int) string {
	if column > 0 {
		return fmt.Sprintf("//line %s:%d:%d\n", path, line, column)
	}
	return fmt.Sprintf("//line %s:%d\n", path, line)
}

// InlineDirective is the inline /*line*/ directive that maps the character
// after it to line and column of path, wherever it stands in a Go line
// (cmd/compile, Compiler directives). The path is the emitName of a path
// that satisfies ValidatePath, which refuses a path that holds */.
func InlineDirective(path string, line, column int) string {
	return fmt.Sprintf("/*line %s:%d:%d*/", path, line, column)
}
