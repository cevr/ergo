package lower

import "ergo.local/prototype/internal/sketch/check"

// Named failure rows (NAMED-ROWS NR1; types.md, Error types 4). A union
// whose every variant is an error type names a row, and it is that row:
// one type with the row value of its variants (check identical), so its Go
// form is the row value's, Go's error. `type LookFail = NotFound | Busy`
// lowers as `type LookFail = error`, and func(e LookFail) and
// func(e NotFound | Busy) are one Go type as they are one Ergo type. A
// value of it is a Go error that holds one of its variants, so a type test
// of it is a test of each variant (caseGoTypes; typeTest by subUnionTests): Go's error
// alone would match every error. Its variants carry no marker method.
//
// The reference lowering is the sealed interface such a union was before
// (flatShape's step 2 kept it one; the owner's rule of 2026-11-08: the old
// lowering stays selectable):
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.namedRowLowering=sealed.
// Under it a function value typed by the name and one typed by the
// spelled row are two Go types, so a program that mixes them does not
// build; every other program prints the same under both.
//
// Prior art: Effect's `type E = A | B` is a TypeScript alias, its own
// members at run time; OCaml's abbreviation of a polymorphic variant row
// is the row; Gleam and ReScript give a sum type its own representation,
// which is the sealed form this replaces for rows.
var namedRowLowering = ""

// namedRow reports whether t is a union that names a row and lowers as Go's
// error: a named, non-generic union declared in a package whose unions the
// lowering emits (unionSourced), each of whose variants is an error type.
func (l *lowerer) namedRow(t check.Type) bool {
	if namedRowLowering == "sealed" {
		return false
	}
	n, ok := t.(*check.Named)
	if !ok || n.Orig != nil || len(n.TParams) > 0 || n.Obj.Decl == nil || !l.unionSourced(n.Obj.Pkg) {
		return false
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || u.Row != nil {
		return false
	}
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok || vn.Orig != nil || len(vn.TParams) > 0 || !l.errorType(vn) {
			return false
		}
	}
	return true
}

// caseGoTypes is what a Go type switch's case lists for a type test of t:
// t's Go type, or, for a named row, each of its variants' (namedRow).
func (l *lowerer) caseGoTypes(t check.Type, goType func(check.Type) string) []string {
	if !l.namedRow(t) {
		return []string{goType(t)}
	}
	vs := unionVariants(t)
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = goType(v)
	}
	return out
}
