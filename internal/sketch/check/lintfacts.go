package check

import "ergo.local/prototype/internal/sketch/syntax"

// Facts for lint (internal/sketch/lint). Each reads what the checker has
// already decided; none decides anything new, so a lint rule can match a
// resolved object or a fact and never an identifier's spelling.

// CallAddsFootprint reports whether the call x adds to its frame's
// footprint (rows.md, Footprints): a capability operation, a callee whose
// footprint is not empty or is unknown, a task start or a with. It follows
// the footprint rule the checker's own purity diagnostics name
// (impureWhy).
func CallAddsFootprint(info *Info, x *syntax.CallExpr) bool {
	return impureWhy(info, x) != ""
}

// Exported reports whether f is visible to importers of its package
// (grammar.md, Visibility).
func (f *Func) Exported() bool { return isExported(f.Name) }

// WritesErrors reports whether f's error row is written in its
// declaration, not inferred from its body (rows.md, Error rows 6).
func (f *Func) WritesErrors() bool { return !f.inferErrors }
