// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Ergo IR decoder: the placeholder body of a generic declaration. See
// ergoir.go.
//
// types2 refuses a generic function without a body, so the lowering gives
// a generic declaration whose body the IR holds the body
//
//	{ panic("ergoir: no IR body") }
//
// on its signature's line, and ergoAttach replaces exactly that body. A
// declaration that keeps it after the IR is attached is an error, never a
// panic at run time. No hook in Go's own files is needed (PATCH-MIN M3).

package ergoir

import (
	"strconv"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoPlaceholder reports a body that is exactly the placeholder: one call
// of the universe's panic with the placeholder's string.
func ergoPlaceholder(info *types2.Info, body *syntax.BlockStmt) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}
	st, ok := body.List[0].(*syntax.ExprStmt)
	if !ok {
		return false
	}
	call, ok := st.X.(*syntax.CallExpr)
	if !ok || len(call.ArgList) != 1 || call.HasDots {
		return false
	}
	fun, ok := call.Fun.(*syntax.Name)
	if !ok || fun.Value != "panic" {
		return false
	}
	if info != nil {
		if _, builtin := info.Uses[fun].(*types2.Builtin); !builtin {
			return false
		}
	}
	lit, ok := call.ArgList[0].(*syntax.BasicLit)
	if !ok || lit.Kind != syntax.StringLit {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == "ergoir: no IR body"
}
