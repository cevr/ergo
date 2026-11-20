// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoBareRange is the clause of a range with neither a key nor a value
// bound (a std body's `for _ in xs`): `for range x`, with no left-hand
// side, as hand-written Go spells it.
func ergoBareRange(x syntax.Expr) *syntax.RangeClause {
	return &syntax.RangeClause{X: x}
}

// ergoIsDefect reports a panic value of a pointer to a struct: the defect
// the prelude's Unreachable raises (runtime/v1 UnreachableAt), which a
// hand-written Go program panics with as it is.
func ergoIsDefect(t types2.Type) bool {
	if t == nil {
		return false
	}
	p, ok := t.Underlying().(*types2.Pointer)
	if !ok {
		return false
	}
	_, ok = p.Elem().Underlying().(*types2.Struct)
	return ok
}
