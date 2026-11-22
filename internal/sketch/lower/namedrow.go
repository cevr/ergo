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
// Before NR1 such a union was a sealed interface (flatShape's step 2). That
// form was kept as a measuring aid for the receipt only and is deleted: it
// could not build a program that mixes the name and its spelled row. The
// receipt (benchmarks/namedrow, instructions a call; hand Go's type switch
// 46.0): named 23.2 and spelled 23.2 under both; a catch of the row whole
// (`case Lost as l`) 63.3 sealed, 58.8 with the row as error, 38.7 once
// the arm binds the error itself and takes no type switch (ir_fail.go).
//
// Prior art: Effect's `type E = A | B` is a TypeScript alias, its own
// members at run time; OCaml's abbreviation of a polymorphic variant row
// is the row; Gleam and ReScript give a sum type its own representation,
// which is the sealed form this replaces for rows.
// namedRow reports whether t is a union that names a row and lowers as Go's
// error: a named, non-generic union declared in a package whose unions the
// lowering emits (unionSourced), each of whose variants is an error type.
func (l *lowerer) namedRow(t check.Type) bool {
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
