package check

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Fingerprints and the reuse decision (compile-speed plan §5.3, CS5a;
// docs/spec/build.md, Interface fingerprint). A package has three:
//
//   - Source: its import path, the path it is checked as, the program's
//     environment (module, platform, Go module, bindings, assets) and each
//     file's name and token text (syntax.TokenText: no comment, no space,
//     no position). A comment or layout edit changes none of it.
//   - Interface: what an importer can see. The token text of every file
//     with the bodies an importer cannot see cut out (interfaceCuts), the
//     rows the checker inferred from those bodies, the cell conversions
//     the package makes, and the interface of each import, so a type an
//     import declares and this package passes on is in it too.
//   - Implementation: the source and the implementation of each import.
//     Only evaluation (tests, laws) reads it.
//
// fingerprint.Reusable decides, from the fingerprints of the last check and this one,
// the packages whose check this one could take as it was: a package whose
// source is the same and whose imports' interfaces are the same. Nothing
// skips work on it yet; the snapshot of a package that names its imports'
// objects by global id cannot outlive a new check of an import (CS5b).
//
// Prior art: OCaml and ReScript stop at an unchanged .cmi digest; Go stops
// at unchanged export data, which, like this interface, holds the types an
// import passes on; Gleam keeps a fingerprint and a dependency list per
// module; Salsa's red-green marking makes the same early cutoff.

// fingerprintSchema versions every fingerprint.
const fingerprintSchema = "ergo-fingerprint-1"

// FingerprintOf is the fingerprints of the package at path, checked as
// checkPath from files (a nil file did not parse) to pkg, with the
// fingerprints of its local imports. clean is a check with no diagnostic;
// env is the program's environment, which the caller encodes.
func FingerprintOf(path, checkPath string, files []*syntax.File, pkg *Package, clean bool, env string, imports []fingerprint.Package) fingerprint.Package {
	fp := fingerprint.Package{Path: path}
	imports = slices.Clone(imports)
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
	for _, q := range imports {
		fp.Imports = append(fp.Imports, q.Path)
	}
	fp.Source = SourceFingerprint(checkPath, files, env)
	if fp.Source != "" {
		parts := []string{fingerprintSchema, "implementation", fp.Source}
		for _, q := range imports {
			parts = append(parts, q.Path, q.Implementation)
		}
		fp.Implementation = fingerprint.Of(parts...)
	}
	if pkg == nil || !clean || fp.Source == "" {
		return fp
	}
	parts := []string{fingerprintSchema, "interface", checkPath, env, InterfaceText(pkg)}
	for _, q := range imports {
		if q.Interface == "" {
			return fp
		}
		parts = append(parts, q.Path, q.Interface)
	}
	fp.Interface = fingerprint.Of(parts...)
	return fp
}

// SourceFingerprint is the source fingerprint of files checked as path in
// env: each file's name and token text, in name order. It is "" when a
// file did not parse.
func SourceFingerprint(path string, files []*syntax.File, env string) string {
	parts := []string{fingerprintSchema, "source", path, env}
	for _, k := range canonicalFiles(files) {
		f := files[k]
		if f == nil {
			return ""
		}
		parts = append(parts, f.Name, syntax.TokenText(f.Src))
	}
	return fingerprint.Of(parts...)
}

// InterfaceText is the text the interface fingerprint hashes: each file's
// token text without the bodies interfaceCuts names, then the rows
// inferred from those bodies, the package-level variables that a body
// assigns, and the cell conversions the package makes. It is not a
// printing of the package: only its equality means anything.
func InterfaceText(pkg *Package) string {
	var b strings.Builder
	for _, fi := range pkg.files {
		f := fi.syntax
		b.WriteString("file\n")
		at := 0
		for _, c := range interfaceCuts(f) {
			b.WriteString(syntax.TokenText(f.Src[at:c[0]]))
			b.WriteString("cut\n")
			at = c[1]
		}
		b.WriteString(syntax.TokenText(f.Src[at:]))
	}
	// A body this text cuts decides an inferred row, which a caller in an
	// importer reads through a value (`let Step = helper`).
	for i, fn := range pkg.Funcs {
		if fn.inferErrors && fn.errVar != nil {
			fmt.Fprintf(&b, "errors %d %s {%s}\n", i, fn.Name, fn.errVar.Solution().list("|"))
		}
		if fn.inferUses && fn.useVar != nil {
			fmt.Fprintf(&b, "uses %d %s {%s}\n", i, fn.Name, fn.useVar.Solution().list("|"))
		}
	}
	names := make([]string, 0, len(pkg.Scope.names))
	for name := range pkg.Scope.names {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if v, ok := pkg.Scope.names[name].(*Var); ok && v.reassigned {
			fmt.Fprintf(&b, "assigned %s\n", name)
		}
	}
	var cells []string
	for t, conv := range pkg.cellConvs {
		cells = append(cells, typeString(t)+" from "+typeString(conv.From))
	}
	slices.Sort(cells)
	for _, c := range cells {
		b.WriteString("cell " + c + "\n")
	}
	return b.String()
}

// interfaceCuts are the byte spans of f an importer cannot see, in order
// and disjoint: the body of each written function that is not generic (an
// importer reads its signature, whose rows it spells or the checker
// infers), and the body of each test. A generic function's body stays: an
// importer's instance may read it. Everything else stays, laws included,
// since the test build of an importer runs a law of an operation type it
// imports against its own providers.
func interfaceCuts(f *syntax.File) [][2]int {
	var cuts [][2]int
	cut := func(n syntax.Node) {
		if n != nil && n.Pos().IsValid() && int(n.End()) <= len(f.Src) {
			cuts = append(cuts, [2]int{int(n.Pos()), int(n.End())})
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Derived != nil || d.Body == nil {
				continue
			}
			if d.TypeParams == nil {
				cut(d.Body)
			}
		case *syntax.TestDecl:
			cut(d.Body)
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i][0] < cuts[j][0] })
	out := cuts[:0]
	for _, c := range cuts {
		if n := len(out); n > 0 && c[0] < out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], c[1])
			continue
		}
		out = append(out, c)
	}
	return out
}
