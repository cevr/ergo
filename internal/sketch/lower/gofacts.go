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
// inspect facts. LF0 reports the facts only: no decision reads them yet.

// goOperand is a Map or slice operand of a Go call: the call, its callee
// (nil for a Go interface operation) and the operand's index among the
// call's operands, a method's receiver first (check.GoReads).
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
	for _, f := range facts {
		why += "; " + goTarget(op.fn) + " " + f.Kind + " " + f.Param + " (" + f.Level + ": " + f.Source + "), which no lowering reads yet"
		l.foreign = append(l.foreign, placedFact{Fact: Fact{
			Kind:     "fact-go-" + f.Kind,
			Func:     goTarget(op.fn),
			At:       file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col),
			Subjects: []string{f.Param},
			Holds:    f.Kind + " " + f.Param,
			Evidence: f.Level,
			Reason:   f.Source,
			Law:      f.Law,
			Use:      "not used: no lowering reads the fact yet (" + form + ")",
		}, file: file, line: line, col: col})
	}
	l.stats.Copies = append(l.stats.Copies, CopySite{File: file, Line: line, Column: col, Form: form, Why: why})
}
