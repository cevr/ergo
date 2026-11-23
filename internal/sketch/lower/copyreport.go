package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The whole copy report (REUSE RU0; docs/tooling.md, ergo build -report
// copies). Every copy the lowering inserts is a CopySite: the hand-over's
// Append, Concat and Ref sites (handover.go, copySite) and each copy the
// value rule decides (values.go), at a Go boundary (gocall.go,
// gocallback.go), at a matched Get's entry (getmatch.go), in an owned
// parameter's wrapper and at Put or Delete's source. The value walk records
// why it decided each copy (copyAt); the lowering records the site when it
// writes the copy (reportCopy), with the copy's kind. No decision reads the
// report: it is the decision's record, so the report and the lowered Go
// cannot disagree (TestCopyReportIsWhole holds the report's sites equal to
// the copy calls of the Go).
//
// Prior art: Swift's -Xllvm -sil-print of copy_value and its
// "-Rpass=…" optimization remarks; Rust's clippy::redundant_clone names a
// clone and why it is needed; Go's -gcflags=-m prints each escape and its
// reason. Each reports the compiler's own decision, not a second analysis.

// The reasons of the value rule's copies.
const (
	whyInFlight = "a later operand of the call writes the root before the callee reads the value (in flight)"
	whyEntry    = "the matched Get's entry is kept past a write of its Map (V9)"
	whyResult   = "a pure call's result shares its argument, which a later write of the root reaches (result alias)"
	whyEscape   = "the value escapes its root while a later write of the root may reach its storage (V2)"
	whyBorrow   = "a generic call's result through a borrow may share the borrowed value"
	whyChain    = "an Append chain's source enters a root while another binding holds it (V1)"
	whyElement  = "an Append's element enters a root while another binding holds it (V1)"
	whyEnters   = "the value enters a root (a let mut or a borrow) while another binding holds it (V1)"
	whyPut      = "Put stores the value in a Map whose entries are written in place (V9)"
	whyOwnedArg = "the argument of an owned parameter is not handed over: another binding still holds it (V5)"
	whyGoDeep   = "Go may write an entry of a Map that shares entries with another (V8, deep)"
	whyGoArg    = "an operand of a Go call, which Go may write or keep"
	whyCell     = "the value moves into a provider cell while another binding holds it"
	whyGoResult = "a Go call's result, which Go may still write (V8, deep)"
	whyGoIn     = "a Go callback's argument, which Go may still write (V8, deep)"
	whyGoOut    = "a Go callback's result, which Go may write (V8, deep)"
	whyGetEntry = "the matched Get's entry is kept past a write that may reach it (V9)"
	whyRefCell  = "an owned Ref site's copy of the held value, taken when the cell is not fresh (refown.go)"
	whyWrapper  = "the wrapper of an owned parameter: a caller that cannot hand its argument over gets a copy (V5)"
	whyMapWrite = "Put or Delete's Map may be read again: the write goes to a copy (V3)"
	whyCopyElem = "Go's copy stores the source's elements, each holding a slice or Map, in another array: they are copied (V3)"
)

// copyAt marks the value x copied where it is lowered, for why; the first
// reason stays.
func (w *valueWalk) copyAt(x syntax.Expr, why string) {
	w.vf.clone[x] = true
	if _, ok := w.vf.why[x]; !ok {
		w.vf.why[x] = why
	}
}

// reportCopy records the copy text the lowering wrote at site for why, when
// text is a copy: Go's slices.Clone, slices.Clip or maps.Clone, or a
// generated copy function (cloneOf). It returns text.
func (fc *funcCtx) reportCopy(site syntax.Node, text, why string) string {
	if kind := copyKind(text); kind != "" && site != nil {
		file, line, col := fc.l.locate(site, site.Pos())
		fc.l.stats.Copies = append(fc.l.stats.Copies, CopySite{File: file, Line: line, Column: col, Form: "copy", Kind: kind, Why: why, Func: fc.owner})
	}
	return text
}

// copyKind is the kind of the copy text: "slice" and "Map" for Go's own
// shallow copies, "deep" for a generated copy function, which copies each
// element that holds a Map or slice; "" when text is no copy.
func copyKind(text string) string {
	head, _, ok := strings.Cut(text, "(")
	if !ok {
		return ""
	}
	_, name, _ := strings.Cut(head, ".")
	if name == "" {
		name = head
	}
	switch {
	case name == "Clone" && strings.Contains(head, "maps"):
		return "Map"
	case name == "Clone" || name == "Clip":
		return "slice"
	case strings.HasPrefix(head, HelperSpelling("", "ergoClone")): // a clone function (values.go cloneBase; pkgnames.go)
		return "deep"
	}
	return ""
}

// OwnedFunc is a function with owned parameters (V5; ergo inspect
// copies): its callers hand those arguments over when they are unshared.
type OwnedFunc struct {
	Func   string   `json:"func"`
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Column int      `json:"column"`
	Params []string `json:"params"`
}

// ownedFuncs is each function of the root package with an owned
// parameter, in source order.
func (l *lowerer) ownedFuncs() []OwnedFunc {
	if l.pkg == nil {
		return nil
	}
	var out []OwnedFunc
	for _, f := range l.pkg.Funcs {
		owned := l.ownedParams(f)
		if len(owned) == 0 || f.Decl == nil {
			continue
		}
		o := OwnedFunc{Func: f.Name}
		if fd := l.funcDecl(f); fd != nil && fd.method != nil {
			o.Func = fd.method.tn.Name + "." + f.Name // as fc.owner names it
		}
		o.File, o.Line, o.Column = l.locate(f.Decl, f.Decl.Name.Pos())
		for _, p := range owned {
			o.Params = append(o.Params, p.v.Name)
		}
		out = append(out, o)
	}
	return out
}

// ownedCallSite records the call x of an owned body (V5): which owned
// arguments it hands over, and which it copies first and why.
func (fc *funcCtx) ownedCallSite(x *syntax.CallExpr, c *check.Call) {
	var parts []string
	for _, p := range fc.l.ownedParams(c.Func) {
		if arg := argOf(x, c, p.index); arg != nil && fc.l.vals.clone[arg] {
			parts = append(parts, p.v.Name+" copied: "+fc.valueWhy(arg))
		} else {
			parts = append(parts, p.v.Name+" handed over")
		}
	}
	file, line, col := fc.l.locate(x, x.Pos())
	fc.l.stats.Copies = append(fc.l.stats.Copies, CopySite{File: file, Line: line, Column: col, Form: "owned call", Why: "calls " + c.Func.Name + "'s owned body: " + strings.Join(parts, "; "), Func: fc.owner})
}

// valueWhy is the reason the value walk recorded for the copy of x.
func (fc *funcCtx) valueWhy(x syntax.Expr) string {
	if why, ok := fc.l.vals.why[x]; ok {
		return why
	}
	return "the value rule copies it"
}
