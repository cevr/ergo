package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Foreign facts at Go calls (LAWFFI LF0; docs/spec/interop.md, Foreign
// facts). The binding of a Go function carries the facts `ergo bind go`
// derived from the pinned toolchain's escape analysis (check.GoFacts). The
// value walk records, for each Map or slice operand of a Go call, the
// call, the callee and the operand's index; after it decides the operand's
// copy it reports the decision and its reason in the copy report (ergo
// build -report copies), and each fact on the operand as a row of ergo
// inspect facts, with what the lowering did with it.
//
// A `reads` fact (LF1) skips the copy: Go neither writes through the
// operand, keeps it, calls it nor returns a part of it, so the run build
// passes a live value as it is (readsOK, the shortcut host-reads). The
// test build takes no shortcut: it passes a copy and, after the call,
// compares the copy with the value Ergo holds (v1.GoReadsCheck), so a
// write the fact missed is a defect naming the function, the parameter
// and the call. Go's analysis does not report a write to a Map (LF0), so
// the compare stays for derived facts too. The skip needs the whole call
// to be harmless: no callback, which may reach the same place; every
// other Map or slice operand read only, so no operand Go writes aliases
// it; the operand a binding or a field path in the frame that declares
// it, so no task holds it while Go reads; and elements the compare reads
// bit for bit (scalars and strings).

// goOperand is a Map or slice operand of a Go call: the call, its callee
// (nil for a Go interface operation) and the operand's index among the
// call's operands, a method's receiver first (goReads).
type goOperand struct {
	call  *syntax.CallExpr
	fn    *check.Func
	index int
}

// goOperand records a, operand i of the Go call n.
func (w *valueWalk) goOperand(a syntax.Expr, n *syntax.CallExpr, c *check.Call, i int) {
	if w.goOps == nil {
		w.goOps = map[syntax.Expr]goOperand{}
	}
	op := goOperand{call: n, index: i}
	if c.Kind == check.CallFunc {
		op.fn = c.Func
	}
	w.goOps[a] = op
}

// readsGuard is a test build's compare of a reads operand after the call:
// the function, the parameter and the call site its defect names.
type readsGuard struct {
	fn, param, at string
}

// readsOK is the admission of host-reads: take reports a site where the
// run build passes the Go operand a with no copy, guard one where the test
// build copies it and compares after the call. It records what the
// lowering did with the fact for the reports.
func (w *valueWalk) readsOK(a syntax.Expr) (take, guard bool) {
	why := w.readsWhy(a)
	switch {
	case why != "":
	case w.fresh(a):
		why = "no copy to skip: the operand is fresh, a local dead after the call, or copied by another rule"
	case !w.l.takes("host-reads"):
		if w.l.tests == nil {
			why = "the reference lowering copies every Go operand"
			break
		}
		w.useFact(a, "checked: the test build copies the operand and compares it after the call")
		return false, true
	default:
		w.l.took("host-reads")
		w.useFact(a, "used: passed with no copy")
		return true, false
	}
	if w.goOps[a].fn != nil && check.GoFacts(w.goOps[a].fn) != nil {
		w.useFact(a, "not used: "+why)
	}
	return false, false
}

// useFact records what the lowering did with the operand a's fact.
func (w *valueWalk) useFact(a syntax.Expr, use string) {
	if w.factUse == nil {
		w.factUse = map[syntax.Expr]string{}
	}
	w.factUse[a] = use
}

// readsWhy is "" when the Go operand a may skip its copy on a reads fact,
// else why not.
func (w *valueWalk) readsWhy(a syntax.Expr) string {
	op, ok := w.goOps[a]
	if !ok || op.fn == nil {
		return "no reads fact"
	}
	if ok, why := goReads(op.call, op.fn, op.index); !ok {
		if why == "" {
			why = "no reads fact"
		}
		return why
	}
	s, ok := underEqual(w.typeOf(a)).(*check.Slice)
	if !ok {
		return "not a slice"
	}
	if _, ok := underEqual(s.Elem).(*check.Basic); !ok {
		return "its elements are not scalars or strings, which the test build's compare reads bit for bit"
	}
	id := pathRoot(a)
	if id == nil {
		return "not a binding or a field of one"
	}
	for x := unparen(a); x != syntax.Expr(id); {
		switch s := x.(type) {
		case *syntax.SelectorExpr:
			x = unparen(s.X)
		case *syntax.SliceExpr:
			// A slice of one (`b[:n]`, S2-COPY): Go reads part of the
			// same array, and the test build compares that part.
			x = unparen(s.X)
		default:
			return "not a binding or a field of one"
		}
	}
	v, ok := w.info.Uses[id].(*check.Var)
	if !ok {
		return "not a binding or a field of one"
	}
	if why := w.callReadsWhy(op); why != "" {
		return why
	}
	vv := w.vars[v]
	for _, o := range vv.occs {
		if o.id != id {
			continue
		}
		for f := o.frame; f != nil && f != vv.frame; f = f.parent {
			if f.escaping {
				return "a task or a kept function captures it"
			}
		}
	}
	return ""
}

// callReadsWhy is "" when the Go call of op leaves its operand op alone
// but for what op's fact says: the call has no callback, which may reach
// the same place, and Go may write no other Map or slice operand, which
// may share its memory; else why not.
func (w *valueWalk) callReadsWhy(op goOperand) string {
	c := w.info.Calls[op.call]
	list := op.call.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if op.call.Trailing != nil {
		return "a callback of the call may reach it"
	}
	for i, b := range list {
		t := w.typeOf(b)
		if _, ok := underEqual(t).(*check.Signature); ok {
			return "a callback of the call may reach it"
		}
		if ok, _ := goReads(op.call, op.fn, i); i != op.index && goShared(t) && !ok {
			return "Go may write another operand of the call, which may share its memory"
		}
	}
	return ""
}

// readsGuardAt marks the copy of the Go operand a for the test build's
// compare after the call.
func (w *valueWalk) readsGuardAt(a syntax.Expr) {
	op := w.goOps[a]
	param := ""
	for _, f := range check.GoFacts(op.fn) {
		if f.Kind == "reads" && f.Operand == op.index {
			param = f.Param
		}
	}
	w.vf.reads[a] = readsGuard{fn: goTarget(op.fn), param: param, at: w.l.siteText(op.call)}
}

// readsCompare is one compare the test build writes after a Go call: the
// value Ergo holds, the copy Go was given, and what a defect names.
type readsCompare struct {
	held, given string
	readsGuard
	// done is a guarded copy of a view site (view.go), done after the
	// call in place of a compare.
	done string
}

// readsCopy is the test build's operand of a reads site: a copy of val,
// the operand x's value, whose compare with val goCall writes after the
// call (compareReads).
func (fc *funcCtx) readsCopy(x syntax.Expr, val string, g readsGuard) string {
	held := fc.spill(val)
	given := fc.spill(fc.cloneValue(fc.typeOf(x), held, x))
	fc.goCompare = append(fc.goCompare, readsCompare{held: held, given: given, readsGuard: g})
	return given
}

// compareReads writes, after a Go call, the compares of the reads operands
// lowered since mark: v1.GoReadsCheck raises a defect when Go wrote the
// copy it was given.
func (fc *funcCtx) compareReads(mark int) {
	for _, c := range fc.goCompare[mark:] {
		if c.done != "" {
			fc.line(c.done + ".Done()")
			continue
		}
		fc.line(fc.l.rt() + ".GoReadsCheck(" + c.held + ", " + c.given + ", " + strconv.Quote(c.fn) + ", " + strconv.Quote(c.param) + ", " + strconv.Quote(c.at) + ")")
	}
	fc.goCompare = fc.goCompare[:mark]
}

// goTarget is the Go function a binding's extern names, as ergo inspect
// prints it: `sort.SearchInts`.
func goTarget(f *check.Func) string {
	if f == nil || f.Decl == nil || f.Decl.Extern == nil {
		return ""
	}
	t := f.Decl.Extern.Target
	if t == nil {
		return ""
	}
	return strings.Trim(t.Value, "\"")
}

// goOnlyReport reports the Go operand a, the identifier id, when it is a
// let of Go's own Map or slice that only Go calls use (H5), which goes to
// Go uncopied.
func (w *valueWalk) goOnlyReport(a syntax.Expr, id *syntax.Ident, goOnly map[*vvar]bool) {
	if v, ok := w.info.Uses[id].(*check.Var); ok && goOnly[w.vars[v]] {
		w.goOperandReport(a, "shared", "a let of Go's own Map or slice that only Go calls use")
	}
}

// goOperandReport writes the copy report's row for the Go operand a, whose
// copy the walk decided: its form (copy, shared, no copy), why, and each
// foreign fact on it, and adds those facts to the inspect rows.
func (w *valueWalk) goOperandReport(a syntax.Expr, form, why string) {
	op, ok := w.goOps[a]
	if !ok {
		return
	}
	l := w.l
	var facts []check.ForeignFact
	if op.fn != nil {
		for _, f := range check.GoFacts(op.fn) {
			if f.Operand == op.index {
				facts = append(facts, f)
			}
		}
	}
	file, line, col := l.locate(a, a.Pos())
	use := w.factUse[a]
	if use == "" {
		use = "not used: " + form + ", " + why
	}
	for _, f := range facts {
		f.Level, _ = factLevel(op.call, op.fn, f, valueClass)
		why += "; " + goTarget(op.fn) + " " + f.Kind + " " + f.Param + " (" + f.Level + ": " + f.Source + "): " + use
		l.foreign = append(l.foreign, placedFact{Fact: Fact{
			Kind:     "fact-go-" + f.Kind,
			Func:     goTarget(op.fn),
			At:       file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col),
			Subjects: []string{f.Param},
			Holds:    f.Kind + " " + f.Param,
			Evidence: f.Level,
			Reason:   f.Source,
			Law:      f.Law,
			Use:      use,
		}, file: file, line: line, col: col})
	}
	l.stats.Copies = append(l.stats.Copies, CopySite{File: file, Line: line, Column: col, Form: form, Why: why})
}
