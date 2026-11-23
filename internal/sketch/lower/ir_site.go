package lower

// IR builder: the call site a call passes (FLIP F7 GP). See ir.go.
//
// A function whose last parameter is the prelude's CallSite, which the
// call leaves out (types.md, Call sites), receives where the call
// starts: a CallSite literal after the written operands (calls.go
// callArgs). It reads the package ordinal and has no effect.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// siteValue is the CallSite literal of the call x, whose type is site.
func (b *irBuilder) siteValue(x *syntax.CallExpr, site *check.Named, p irPos) *irExpr {
	file, line, column, pkg, id := b.l.callSite(x)
	td := b.l.typeDecl(site.Obj, x)
	return &irExpr{K: "lit", Pos: p, T: b.goType(site, x),
		Keys: []string{td.fieldName["File"], td.fieldName["Line"], td.fieldName["Column"], td.fieldName["Pkg"], td.fieldName["Id"]},
		Elems: []*irExpr{
			{K: "const", Pos: p, T: "string", V: file},
			{K: "const", Pos: p, T: "int", V: strconv.Itoa(line)},
			{K: "const", Pos: p, T: "int", V: strconv.Itoa(column)},
			{K: "global", Pos: p, Name: pkg},
			{K: "const", Pos: p, T: "int", V: strconv.Itoa(id)},
		}}
}
