package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The view (LAWFFI LF2b; owner q193; docs/spec/interop.md, Foreign facts
// 8; the shortcut host-view). `crc32.ChecksumIEEE(strings.Bytes(s))`
// copies s's bytes for a Go function that only reads them. Where the copy
// is the whole operand of a Go call, made by a body whose contract names a
// view form (v1.Body.View: StringsBytes names BytesView), at a parameter
// whose reads fact is vouched by a row that allows a view and validated
// (goView), and the call has no callback and writes no other operand
// (callReadsWhy), the run build passes v1.BytesView(s): the string's own
// bytes. The decision is the value walk's (viewOK), so both paths read it.
// A test build passes a guarded copy instead (v1.GuardBytes): its pages
// fault at a write, a read past the end and a read after the call, so each
// test that reaches the call checks the fact. ergo build -views=off
// (Unit.NoViews) and the reference hostViewLowering "copy" take neither: the
// call copies as before. The selection reads facts, never a name.

// hostViewLowering selects the reference lowering of host-view: "copy" makes
// every view site copy, as before LF2b.
var hostViewLowering string

// hostViewSite is a view site: the string operand of the copying call, and
// the Go function, parameter and call a guarded copy's defect names.
type hostViewSite struct {
	str           syntax.Expr
	fn, param, at string
	guard         bool // the test build's guarded copy
}

// viewForm is the view form of the body f's call reaches: an extern, of
// any package, of a body whose contract names one (bodyView; the
// language's operation s.Bytes() is an extern of StringsBytes),
// or a function whose body is one call of such an extern on its one
// parameter, plain or as a dot call (`func Bytes(s string) []byte {
// s.Bytes() }`, PRIM-4); else "". It reads declarations, never std
// membership.
func (l *lowerer) viewForm(f *check.Func) string {
	if f == nil || f.Decl == nil {
		return ""
	}
	if f.Decl.Extern != nil {
		return bodyView(f)
	}
	d := f.Decl
	if d.Body == nil || len(d.Body.List) != 1 || f.Pkg == nil || f.Pkg.Info == nil || d.Type == nil || d.Type.Params == nil || len(d.Type.Params.List) != 1 || len(d.Type.Params.List[0].Names) != 1 {
		return ""
	}
	s, ok := d.Body.List[0].(*syntax.ExprStmt)
	if !ok {
		return ""
	}
	call, ok := unparen(s.X).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return ""
	}
	c := f.Pkg.Info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == f {
		return ""
	}
	ops := callOperands(call, c)
	if len(ops) != 1 {
		return ""
	}
	arg, ok := unparen(ops[0]).(*syntax.Ident)
	if !ok || arg.Name != d.Type.Params.List[0].Names[0].Name {
		return ""
	}
	return l.viewForm(c.Func)
}

// viewWhy is the view site of the Go operand a, or why it is none.
func (w *valueWalk) viewWhy(a syntax.Expr) (hostViewSite, string) {
	call, ok := unparen(a).(*syntax.CallExpr)
	if !ok {
		return hostViewSite{}, "not a call"
	}
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || w.l.viewForm(c.Func) == "" {
		return hostViewSite{}, "its body has no view form"
	}
	ops := callOperands(call, c)
	if len(ops) != 1 {
		return hostViewSite{}, "its body has no view form"
	}
	op := w.goOps[a]
	if op.fn == nil {
		return hostViewSite{}, "a Go interface operation"
	}
	param, why := goView(op.call, op.fn, op.index)
	if why != "" {
		return hostViewSite{}, why
	}
	if why := w.callReadsWhy(op); why != "" {
		return hostViewSite{}, why
	}
	return hostViewSite{str: ops[0], fn: goTarget(op.fn), param: param, at: w.l.siteText(op.call)}, ""
}

// viewOK is the admission of host-view: whether the Go operand a is a view
// site, recorded for its lowering (vf.view), with its form: the view in a
// run build, a guarded copy in a test build.
func (w *valueWalk) viewOK(a syntax.Expr) bool {
	if w.l.canon.noViews || hostViewLowering == "copy" {
		return false
	}
	site, why := w.viewWhy(a)
	if why != "" {
		return false
	}
	if w.l.takes("host-view") {
		w.l.took("host-view")
		w.useFact(a, "used: a view of the string's bytes, with no copy")
	} else {
		site.guard = true
		w.useFact(a, "checked: the test build passes a guarded copy")
	}
	if w.vf.view == nil {
		w.vf.view = map[syntax.Expr]hostViewSite{}
	}
	w.vf.view[a] = site
	return true
}

// viewOperand lowers the view site x: v1.BytesView of the string in a run
// build; in a test build a guarded copy, done after the Go call
// (compareReads).
func (fc *funcCtx) viewOperand(x syntax.Expr, site hostViewSite) string {
	s := fc.callArgs([]syntax.Expr{site.str}, unparen(x).(*syntax.CallExpr))[0]
	if !site.guard {
		return fc.l.rt() + ".BytesView(" + s + ")"
	}
	g := fc.spill(fc.l.rt() + ".GuardBytes(" + s + ", " + strconv.Quote(site.fn) + ", " + strconv.Quote(site.param) + ", " + strconv.Quote(site.at) + ")")
	fc.goCompare = append(fc.goCompare, readsCompare{done: g})
	return g + ".Bytes()"
}
