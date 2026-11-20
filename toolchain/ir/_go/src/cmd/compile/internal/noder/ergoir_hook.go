// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"cmd/compile/internal/ergoir"
	"cmd/compile/internal/rangefunc"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// The -ergoir reader lives in package ergoir (PATCH-MIN M4). This adapter
// is noder's side: ergoFlag for irgen.go's hooks, and ergoStub, which
// unified.go's writePkgStub calls once (M2), and which returns the
// writer's range-over-func literals with the IR bodies' added.

var ergoFlag = ergoir.Flag

func ergoStub(m posMap, pkg *types2.Package, info *types2.Info, noders []*noder, other map[*syntax.FuncLit]bool) map[*syntax.FuncLit]bool {
	files := make([]*syntax.File, len(noders))
	for i, p := range noders {
		files[i] = p.file
	}
	ergoir.Stub(m.makeXPos, pkg, info, files)
	if ergoFlag.IR == "" {
		return other
	}
	// checkFiles rewrote the source's range-over-func loops before the
	// IR bodies were attached: Go's rewrite runs once more for theirs.
	for lit, v := range rangefunc.Rewrite(pkg, info, files) {
		if other == nil {
			other = map[*syntax.FuncLit]bool{}
		}
		other[lit] = v
	}
	return other
}
