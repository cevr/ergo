package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A function that keeps its argument's call tree as written (types.md,
// Laws 14.2; AW-ERGO, owner q230): the line `// ergo:aswritten` in the doc
// comment of a function, as the prelude's AsWritten writes it,
//
//	// ergo:aswritten
//	func AsWritten[T](v T) T { v }
//
// marks it. A rewrite law over functions never takes a call tree inside a
// call of it as its root or a part of its match, and the lowering keeps
// its call as a call until the rewrites are done. The line is the user's
// as much as std's: the checker reads it in every package.

// asWrittenMarker is the line that marks a function.
const asWrittenMarker = "// ergo:aswritten"

// checkAsWritten records each marked function after its body and rows are
// final, and refuses (S108) a line on anything but a pure identity of one
// parameter: no receiver and no extern, one parameter, one result of the
// parameter's type, empty rows, and a body that is the parameter.
func (c *checker) checkAsWritten() {
	for _, f := range c.funcs {
		if f.Decl == nil || !asWrittenLine(f.Decl.Doc) {
			continue
		}
		c.cur = f.file
		if !c.asWrittenShape(f) {
			c.errorf(f.Decl.Name, CodeType, "the `%s` line above %s marks no pure identity of one parameter: write `func %s[T](v T) T { v }`", asWrittenMarker, f.Name, f.Name)
			continue
		}
		f.asWritten = true
	}
}

// asWrittenLine reports a doc comment that holds the marker line.
func asWrittenLine(doc *syntax.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, cm := range doc.List {
		if strings.TrimSpace(cm.Text) == asWrittenMarker {
			return true
		}
	}
	return false
}

func (c *checker) asWrittenShape(f *Func) bool {
	d, s := f.Decl, f.Sig
	if d.Extern != nil || d.Recv != nil || d.Body == nil || s == nil {
		return false
	}
	if len(s.Params) != 1 || len(s.Results) != 1 || s.Fallible || s.Defects || !s.Errors.Empty() || !s.Uses.Empty() || !c.identical(s.Results[0], s.Params[0].Type) {
		return false
	}
	if len(d.Body.List) != 1 {
		return false
	}
	var x syntax.Expr
	switch st := d.Body.List[0].(type) {
	case *syntax.ExprStmt:
		x = st.X
	case *syntax.ReturnStmt:
		if len(st.Results) == 1 {
			x = st.Results[0]
		}
	}
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			break
		}
		x = p.X
	}
	id, ok := x.(*syntax.Ident)
	if !ok {
		return false
	}
	// The body reads its own copy of the parameter: the same binder.
	v, ok := c.pkg.Info.Uses[id].(*Var)
	return ok && v.param && v.id != nil && v.id == s.Params[0].id
}

// AsWritten reports a function marked `// ergo:aswritten` whose shape the
// checker accepted: the rewrite pass keeps the call tree of its argument
// as written, and the inliner keeps its call a call.
func AsWritten(f *Func) bool { return f != nil && f.asWritten }
