package lower

// IR builder: the Go declaration of a body the IR gives (PATCH-MIN M3).
//
// A function whose body is in the IR file keeps its Go declaration without
// a body, which gc accepts for any function but a generic one: types2
// reports "generic function is missing function body" while it checks the
// package, before the reader can attach the IR body. So a generic
// declaration (one with type parameters of its own) gets the placeholder
// body irPlaceholder on its signature's line, which types2 checks as any
// body, and the reader (noder/ergoir.go, ergoPlaceholder) replaces exactly
// that body with the IR's. The compiler fork then needs no hook in Go's
// own type checker (irgen.go): the decision is the lowering's.
//
// Prior art: a declaration with a stub body its real body replaces later
// is how Go's own //go:linkname'd and assembly-backed functions stand
// (a bodiless declaration the linker fills), and how TypeScript's .d.ts
// or OCaml's .mli let a checker see a signature whose body comes from
// elsewhere; Kotlin/Native's `external` and Swift's `@_silgen_name`
// declare a body another stage supplies.

// irPlaceholder is the body of a generic declaration whose body the IR
// gives; the reader matches it exactly (ergoPlaceholder).
const irPlaceholder = ` { panic("ergoir: no IR body") }`

// irHeadEnd is what follows the signature of a declaration whose body the
// IR gives: the placeholder for a generic one, nothing for any other.
func irHeadEnd(goGeneric bool) string {
	if goGeneric {
		return irPlaceholder
	}
	return ""
}
