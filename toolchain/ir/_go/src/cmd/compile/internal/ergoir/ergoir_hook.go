// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package ergoir reads Ergo IR (-ergoir): typed function bodies that
// replace the Go source's, attached as typed syntax after types2 checked
// the package and before the unified writer encodes it (PATCH-MIN M4: our
// code in our own package; noder calls it from one line of unified.go).
package ergoir

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"cmd/internal/src"
)

// Stub is the reader's one entry (noder's ergoStub): with -ergoir it
// attaches the IR bodies. Their Go text is ergo's to print
// (lower.IRGoText).
func Stub(makeXPos func(syntax.Pos) src.XPos, pkg *types2.Package, info *types2.Info, files []*syntax.File) {
	if ergoFlag.IR == "" {
		return
	}
	noders := make([]*noder, len(files))
	for i, f := range files {
		noders[i] = &noder{file: f}
	}
	ergoAttach(posMap{makeXPos}, pkg, info, noders)
}

// noder is the part of noder's noder the reader reads: a file's syntax.
type noder struct{ file *syntax.File }

// posMap is the part of noder's posMap the reader calls.
type posMap struct{ xpos func(syntax.Pos) src.XPos }

func (m *posMap) makeXPos(p syntax.Pos) src.XPos { return m.xpos(p) }

// lastNonEmptyStmt and isPtrTo are copies of noder's (writer.go).

func lastNonEmptyStmt(stmts []syntax.Stmt) syntax.Stmt {
	for i := len(stmts) - 1; i >= 0; i-- {
		stmt := stmts[i]
		if _, ok := stmt.(*syntax.EmptyStmt); !ok {
			return stmt
		}
	}
	return nil
}

func isPtrTo(from, to types2.Type) bool {
	ptr, ok := types2.Unalias(from).(*types2.Pointer)
	return ok && types2.Identical(ptr.Elem(), to)
}
