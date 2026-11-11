package check

// The predeclared set (D-D3): the names a declaration in a program package
// may not take. One implementation answers both the checker's binder rule
// (isPreludeName) and tools outside the checker (IsPredeclared, read by
// bindc), so the two cannot drift.

// predeclaredNames are the names the decided language predeclares that the
// checker spells as syntax (`Map[K, V]`). Every other predeclared name is
// found in the scopes above the package: the universe and the prelude's
// scope.
var predeclaredNames = map[string]bool{"Map": true}

// notPreludeNames are universe names outside D-D3's decided list: `error`
// is only the head of an error row (D-E2), `uintptr`, `complex64` and
// `complex128` are not v1 types, `with` is a keyword and `go` names the
// extern go package. Each keeps the treatment it had before D-D3.
var notPreludeNames = map[string]bool{"error": true, "uintptr": true, "complex64": true, "complex128": true, "with": true, "go": true}

// IsPredeclared reports whether D-D3 refuses name as a declaration in a
// program package: a universe name of D-D3's list, a name of the prelude
// scope or one of predeclaredNames. A prelude operation (a function on a
// slice, a Map or a prelude type, reached by the dot form only) is not in
// the prelude scope, so it is not predeclared.
func IsPredeclared(name string) bool {
	return predeclaredAbove(name, prelude().scope)
}

// predeclaredAbove reports whether name is predeclared for a package whose
// scope's parent is above: the universe for the prelude itself, the
// prelude scope for every other package.
func predeclaredAbove(name string, above *Scope) bool {
	// The constraint wire is not predeclared: a declaration named wire
	// shadows it, so wire stays an ordinary identifier (owner q83; 196w),
	// for the checker and for bindc alike.
	if name == "_" || notPreludeNames[name] || name == SetWire.String() {
		return false
	}
	if predeclaredNames[name] {
		return true
	}
	for sc := above; sc != nil; sc = sc.parent {
		if _, ok := sc.names[name]; ok {
			return true
		}
	}
	return false
}
