package lower

// IR builder: the variant with no result (IR-DROP; the text path's OPT5,
// drop.go). A statement that discards a call's value (`let _ = c.Put(k,
// v)`, or a call as a statement) calls the callee's variant with no
// result, as the emitter's callText does: the same body lowered once more
// where the frame has no result, so each return evaluates its value for
// its effects alone and Go drops the stores no one reads. lru's Put builds
// an Option of the evicted entry on every eviction, which its bench loop
// drops.
//
// The variant is the emitter's: droppedName queues its body once, and
// emitBody builds that body's IR with drop set (decls.go), as for every
// function. The call qualifies by the emitter's own rule (droppedSite: a
// statement's call of an Ergo body with one result), and here also:
//   - the IR inliner does not copy the call (ir_forms_inline.go): a copy
//     discards its value in place, as the text path's inlined statement
//     does;
//   - the callee has no failure row: the IR binds a fallible call's
//     value and error (ir_fail.go), where the variant returns the error
//     alone.
//
// The reference is irDropLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irDropLowering=off):
// the call names the function itself.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irDropLowering is "off" for the reference: a discarded call names the
// function itself.
var irDropLowering string

// irDropBodyLowering is "off" for the reference: the variant's body is the
// emitter's Go text. Otherwise the IR builds it (IR-DROPBODY), as any body
// with no result: each return evaluates its value for its effects alone
// (retOf, discard), and the inliner and its passes reach the hot loop's
// discarded calls (lru's PutDropped).
var irDropBodyLowering string

// IRDropBodyOn reports that the IR builds the variants with no result
// (the property's reach check).
func IRDropBodyOn() bool { return irDropBodyLowering != "off" }

// IRDropOn reports that discarded calls name the variant with no result
// (the property's reach check).
func IRDropOn() bool { return irDropLowering != "off" }

// droppedCall is e, the call x of fd, naming fd's variant with no result
// when a statement discards x's value and the rule above admits it.
func (b *irBuilder) droppedCall(x *syntax.CallExpr, c *check.Call, fd *funcDecl, e *irExpr) *irExpr {
	if irDropLowering == "off" || b.dropping != x || fd.fallible || len(rowElems(c.Func.Sig.Errors)) > 0 {
		return e
	}
	if b.st.inl != nil && b.st.inl.sites[e] != nil {
		return e // the inliner copies it (ir_forms_inline.go)
	}
	// droppedSite's rule, without its count of the emitter's sites.
	f := c.Func
	if !formsOn() || len(f.Sig.Results) != 1 || fd.extern != "" || f.Decl == nil || f.Decl.Body == nil {
		return e
	}
	if len(b.l.ownedParams(f)) > 0 && (b.l.vals == nil || !b.l.vals.ownCall[x]) {
		return e // droppedSite's defect: every such call is owned
	}
	e.Name = b.l.droppedName(c.Func, fd)
	b.st.report.Dropped++
	return e
}
