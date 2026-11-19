package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/reflectread"
)

// An object path names an object of a checked package by the steps that
// reach it from the package's declarations, as Go's objectpath does
// (compile-speed/cs5b.md §3): a reference from one user package's export
// segment to an object of another is written as a path and resolved in
// whichever check of that package is current, so a cached importer
// survives a fresh check of its import.
//
// A path starts at a name of the package scope or of its context
// namespace, then steps through fields (by index in the Go struct),
// slice elements (by index) and map entries with a string or integer key
// (by key). Pointers and interfaces are followed without a step. The empty
// path is the package itself. Paths are valid only for the compiler that
// wrote them (the export is keyed by the compiler's identity).
//
// Decided: the walk reads the model by reflection, not by an exportgen
// walker, by simplicity (one walker and one resolver read the same field
// list, so they cannot disagree); reverse by generating both from
// exportgen's type graph when CS5b-3 measures the walk's cost. The walker
// and the resolver live in reflectread, the checker's one reviewed reader
// by reflection (the provenance analyzer bans reflect in the checker).
type objPath string

// A walk takes each struct's fields in declaration order, so an object is
// named through the field that holds it before any cache of it (a
// Struct's Fields before its index, a Named's methods before mlist), and
// it skips a map keyed by an object, whose keys have no name a path may
// hold.

// stopType reports the pointer types a walk does not enter: syntax nodes,
// checkers and their tables, importers, and other packages.
func stopType(pkgPath, name string) bool {
	if pkgPath == "ergo.local/prototype/internal/sketch/syntax" || pkgPath == "sync" {
		return true
	}
	if pkgPath != "ergo.local/prototype/internal/sketch/check" {
		return true
	}
	switch name {
	case "checker", "typeTable", "stdImporter", "goImp", "Package", "Info":
		return true
	}
	return false
}

// ObjectPaths are the paths of every object reached from pkg's
// declarations, by object, each object's first path in a fixed walk order
// (scope names, then contexts, sorted; fields in declaration order; map
// entries by key). Compute it right after pkg's check, before an importer
// fills lazy state in it (census.md §1). ResolvePath reads a path.
func ObjectPaths(pkg *Package) map[any]string {
	out := map[any]string{pkg: ""}
	first := func(x any, path []byte) bool {
		if _, seen := out[x]; seen {
			return true
		}
		out[x] = string(path)
		return false
	}
	if pkg.Scope != nil {
		for _, name := range sortedKeys(pkg.Scope.names) {
			reflectread.Walk(pkg.Scope.names[name], reflectread.AppendStep(nil, reflectread.StepScope, name), stopType, first)
		}
	}
	for _, name := range sortedKeys(pkg.Contexts) {
		reflectread.Walk(pkg.Contexts[name], reflectread.AppendStep(nil, reflectread.StepContext, name), stopType, first)
	}
	return out
}

// ResolvePath returns the object at path (ObjectPaths) in pkg, or false
// when the path names nothing there.
func ResolvePath(pkg *Package, path string) (any, bool) { return resolvePath(pkg, objPath(path)) }

// PathString renders a path for messages.
func PathString(path string) string { return objPath(path).String() }

func resolvePath(pkg *Package, path objPath) (any, bool) {
	if path == "" {
		return pkg, true
	}
	kind, s, rest, ok := reflectread.NextStep([]byte(path))
	if !ok {
		return nil, false
	}
	var root any
	switch kind {
	case reflectread.StepScope:
		if pkg.Scope == nil {
			return nil, false
		}
		o, found := pkg.Scope.names[s]
		if !found {
			return nil, false
		}
		root = o
	case reflectread.StepContext:
		c, found := pkg.Contexts[s]
		if !found {
			return nil, false
		}
		root = c
	default:
		return nil, false
	}
	return reflectread.Resolve(root, rest)
}

// String renders a path for messages: s:Name.f3.i0.k:key.
func (p objPath) String() string {
	b := []byte(p)
	var parts []string
	for len(b) > 0 {
		kind, s, rest, ok := reflectread.NextStep(b)
		if !ok {
			parts = append(parts, "?")
			break
		}
		parts = append(parts, string(kind)+s)
		b = rest
	}
	return strings.Join(parts, ".")
}
