package check

// Named row identity (NAMED-ROWS NR1; types.md, Error types 4, Generics 8).
// A union whose every variant is an error type names a row, and it is that
// row: `type LookFail = NotFound | Busy` and the row value NotFound | Busy
// are one type, so func(e LookFail) fits where func(e NotFound | Busy) is
// wanted and a row parameter that meets LookFail binds to its variants
// (unify.go rowOf). The type table gives the named type the row value's id
// (intern.go) once the package's error types are known: before
// collectBodyErrorRows no variant is known to be an error type, so the ids
// built before it are dropped (knowRows). Two named rows of one set are
// then one type too, as two spellings of one set are.

// knowRows turns on the identity of a named row and its row, after the
// error types are collected: every id built before is rebuilt.
func (c *checker) knowRows() {
	c.types.rowOf = c.namedRowOf
	clear(c.types.of)
	c.types.newSolutions()
}

// namedRowOf is the row the named type n names, or nil: n is a union that
// is no instance and holds no row of its own, and each of its members (a
// listed union's members included, rowMembers) is an error type of the
// program's bodies (isProductionError).
func (c *checker) namedRowOf(n *Named) *Row {
	if n.Orig != nil || len(n.TArgs) > 0 || len(n.TParams) > 0 {
		return nil
	}
	u, ok := n.underlying().(*Union)
	if !ok || u.Row != nil || len(u.Variants) == 0 {
		return nil
	}
	r := newRow()
	for _, v := range u.Variants {
		for _, m := range rowMembers(v) {
			mn, ok := m.(*Named)
			if !ok || !c.isProductionError(mn) {
				return nil
			}
			r = r.with(c.types, mn)
		}
	}
	return r
}

// isProductionError reports whether n's origin is an error type in the
// program's bodies (Error types 1), of this package or of the one that
// declares it. A type that only a test's or a law's row names is none:
// one type has one identity in a check.
func (c *checker) isProductionError(n *Named) bool {
	o := n.origin()
	if c.errorTypeIn(o, OwnerProduction) {
		return true
	}
	if p := o.Obj.Pkg; p != nil && p != c.pkg && p.ErrorTypes != nil {
		return p.IsErrorType(o, OwnerProduction)
	}
	return false
}
