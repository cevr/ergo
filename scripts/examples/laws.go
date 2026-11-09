package main

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// A domain package states each contract it needs as an operation type with
// laws (AGENTS.md). The runner enforces it for a program of packages: an
// operation type declared outside cmd/ with no member law is a layout
// error. The -lawless flag reports the same check over every example and
// fails nothing.
//
// The check reads the checker's model, not the text (fu-lawcheck-compiler):
// each package folder is read and checked as `ergo check DIR` reads it
// (sketchfront.CheckDir), and an operation type is a declared type whose
// underlying type is a *check.Interface with at least one operation; its
// member laws are the checker's TypeName.Laws. So a declaration the
// formatter would move, a name outside ASCII or a member split over lines
// is read as the checker reads it. A package the checker refuses cannot be
// read: each of its diagnostics is a problem, so a type error never hides
// a lawless type.

// lawlessType is an operation type with no member law.
type lawlessType struct {
	file string
	line int
	name string
}

func (l lawlessType) String() string {
	return fmt.Sprintf("%s:%d: operation type %s has no law; a domain package states its contract as an operation type with laws (AGENTS.md)", l.file, l.line, l.name)
}

// lawlessPackage checks the package in the folder dir and the packages of
// its module it imports, and returns the operation types of that package
// with no member law, in source order, each file named relative to base;
// and one problem per diagnostic or read error, which means its types
// could not be read.
func lawlessPackage(dir, base string) ([]lawlessType, []string) {
	label := relTo(base, dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: the law check cannot read the package: %v", label, err)}
	}
	a, err := sketchfront.CheckDir(abs)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: the law check cannot read the package: %v", label, err)}
	}
	var problems []string
	for _, d := range a.Diagnostics {
		problems = append(problems, fmt.Sprintf("%s: the law check reads the checked package, and the checker reports %s at %s:%d:%d: %s", label, d.Code, d.File, d.Span.Line, d.Span.Column, d.Message))
	}
	packages := a.Packages()
	if len(packages) == 0 {
		return nil, problems
	}
	root := packages[len(packages)-1]
	if root.Package == nil {
		if len(problems) == 0 {
			problems = append(problems, fmt.Sprintf("%s: the law check found no checked package", label))
		}
		return nil, problems
	}
	var found []lawlessType
	for _, tn := range root.Package.Types {
		if !isOperationType(tn) || len(tn.Laws) > 0 {
			continue
		}
		file, line := label, 0
		for _, f := range root.Files {
			if f.Syntax != nil && slices.Contains(f.Syntax.Decls, syntax.Decl(tn.Decl)) {
				file, line = relTo(base, filepath.Join(root.Dir, filepath.Base(f.Rel))), f.Syntax.Line(tn.Decl.Type)
			}
		}
		found = append(found, lawlessType{file: file, line: line, name: tn.Name})
	}
	return found, problems
}

// isOperationType reports whether tn declares an operation type: its
// underlying type is a method set with at least one operation. A record
// with a function field is a *check.Struct, not one.
func isOperationType(tn *check.TypeName) bool {
	n, ok := tn.Type.(*check.Named)
	if !ok || tn.Decl == nil {
		return false
	}
	it, ok := n.Underlying().(*check.Interface)
	return ok && len(it.Methods) > 0
}

// relTo is name relative to base, slash-separated; name when it is not
// under base.
func relTo(base, name string) string {
	rel, err := filepath.Rel(base, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(rel)
}

// packageFolders are the folders of sources, each once, sorted.
func packageFolders(sources []string) []string {
	var dirs []string
	for _, source := range sources {
		if dir := filepath.Dir(source); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// moduleLawProblems returns the lawless operation types of a program of
// packages: those declared in its package folders outside cmd/, which hold
// the binary and choose providers.
func moduleLawProblems(p *program) []string {
	var problems []string
	commands := filepath.Join(p.dir, "cmd") + string(filepath.Separator)
	var sources []string
	for _, source := range p.sources {
		if !strings.HasPrefix(source, commands) {
			sources = append(sources, source)
		}
	}
	for _, dir := range packageFolders(sources) {
		found, checkProblems := lawlessPackage(dir, p.dir)
		problems = append(problems, checkProblems...)
		for _, l := range found {
			problems = append(problems, l.String())
		}
	}
	return problems
}

// reportLawless prints every lawless operation type of every package
// folder under examples/, programs or not, and the problems of the
// folders the checker refuses, and returns 0: a report, not a check.
func reportLawless(root string, stdout io.Writer) int {
	examples := filepath.Join(root, "examples")
	var sources []string
	err := filepath.WalkDir(examples, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "ergo.bind" {
			return filepath.SkipDir // generated Go bindings (IG7), no package
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".ergo") && entry.Type().IsRegular() {
			sources = append(sources, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(stdout, "examples:", err)
		return 2
	}
	count := 0
	for _, dir := range packageFolders(sources) {
		found, problems := lawlessPackage(dir, root)
		for _, problem := range problems {
			fmt.Fprintln(stdout, problem)
		}
		for _, l := range found {
			fmt.Fprintln(stdout, l)
		}
		count += len(found)
	}
	fmt.Fprintf(stdout, "examples: %d operation types with no law (report only)\n", count)
	return 0
}
