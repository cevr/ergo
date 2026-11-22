package eval

import (
	"path"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// shownAssert is a failed assert's report, the text the native runner
// prints after "assert failed at SITE: " (TESTSPEED FT1): the assert's
// source text and, for a comparison whose operands the report shows
// (assertShown), its two operand values, rendered when the case is
// reported.
type shownAssert struct {
	text string
	// unshown says the native runner shows this assert's operands and
	// the evaluator does not (tryShown): its report is not the native one.
	unshown    bool
	lt, rt     check.Type
	left, rite Value
	shown      bool
}

// assertOperands is an assert whose report shows its operands
// (assertShown): its comparison and the operand types, found once per
// assert (facts.assertShown).
type assertOperands struct {
	x      *syntax.BinaryExpr
	lt, rt check.Type
}

// assertStmt runs `assert x`. A comparison whose operands the report
// shows is evaluated as the walk evaluates it (each parenthesis, then the
// comparison, a step each before its work; its operands left to right),
// with the operands kept for the report; any other x is evaluated whole.
func (in *interp) assertStmt(fr *frame, s *syntax.AssertStmt) *abrupt {
	var v Value
	var left, right Value
	sf := in.sx(fr, s)
	if !sf.assertSeen {
		sf.assertSeen = true
		if b, lt, rt, ok := in.assertShown(fr, s.X); ok {
			sf.assertShown = &assertOperands{b, lt, rt}
		}
	}
	shown := sf.assertShown
	if shown != nil {
		b := shown.x
		x := s.X
		for {
			p, isParen := x.(*syntax.ParenExpr)
			if !isParen {
				break
			}
			if ab := in.charge(fr, p, 1); ab != nil {
				return ab
			}
			x = p.X
		}
		f := in.fx(fr, b)
		if ab := in.charge(fr, b, 1); ab != nil {
			return ab
		}
		a, ab := in.exprK(fr, b.X, in.kid(fr, f, 0, b.X))
		if ab != nil {
			return ab
		}
		c, ab := in.exprK(fr, b.Y, in.kid(fr, f, 1, b.Y))
		if ab != nil {
			return ab
		}
		if v, ab = in.binop(fr, b, b.Op, a, c); ab != nil {
			return ab
		}
		left, right = a, c
	} else {
		var ab *abrupt
		if v, ab = in.expr(fr, s.X); ab != nil {
			return ab
		}
	}
	if !truth(v) {
		// The report is made only when the assert fails.
		report := &shownAssert{text: in.sourceText(fr, s.X), unshown: in.tryShown(fr, s.X)}
		if shown != nil {
			report.lt, report.rt, report.left, report.rite, report.shown = shown.lt, shown.rt, left, right, true
		}
		return fr.abrupt(ctlAssert, s, report, "")
	}
	return nil
}

// assertShown reports whether a failed assert of x shows its operands, by
// the native runner's rule (lower's assertShown): x, through parentheses,
// is a comparison (==, !=, <, <=, >, >=), not of two constants, whose
// operand types are both printable. A try that starts x is not shown here
// (the native runner looks through it): such an assert's report is not the
// native one, and its result says so (Result.Message is empty).
func (in *interp) assertShown(fr *frame, x syntax.Expr) (*syntax.BinaryExpr, check.Type, check.Type, bool) {
	b, ok := unparen(x).(*syntax.BinaryExpr)
	if !ok {
		return nil, nil, nil, false
	}
	switch b.Op.String() {
	case "==", "!=", "<", "<=", ">", ">=":
	default:
		return nil, nil, nil, false
	}
	_, lc := fr.info.Values[b.X]
	_, rc := fr.info.Values[b.Y]
	lt, lok := typedExpr(fr.info, b.X)
	rt, rok := typedExpr(fr.info, b.Y)
	if (lc && rc) || !lok || !rok || !printable(fr.pkg, lt, map[*check.Named]bool{}) || !printable(fr.pkg, rt, map[*check.Named]bool{}) {
		return nil, nil, nil, false
	}
	return b, lt, rt, true
}

// tryShown reports whether x is an assert the native runner shows through
// a try that starts it, which the evaluator does not show.
func (in *interp) tryShown(fr *frame, x syntax.Expr) bool {
	t, ok := unparen(x).(*syntax.TryExpr)
	if !ok {
		return false
	}
	_, _, _, shown := in.assertShown(fr, t.X)
	return shown
}

// typedExpr is x's checked type, through parentheses.
func typedExpr(info *check.Info, x syntax.Expr) (check.Type, bool) {
	for {
		if t, ok := info.Types[x]; ok {
			return t, true
		}
		p, isParen := x.(*syntax.ParenExpr)
		if !isParen {
			return nil, false
		}
		x = p.X
	}
}

// printable is the native runner's rule for a type a report shows (lower's
// printable): a basic type; a slice, Map or tuple of printable types; a
// union of printable members; or a record, union or defined type of pkg,
// the package under test, over printable types.
func printable(pkg *check.Package, t check.Type, seen map[*check.Named]bool) bool {
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Slice:
		return printable(pkg, u.Elem, seen)
	case *check.Map:
		return printable(pkg, u.Key, seen) && printable(pkg, u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if !printable(pkg, e, seen) {
				return false
			}
		}
		return true
	case *check.Union:
		for _, v := range u.Variants {
			if !printable(pkg, v, seen) {
				return false
			}
		}
		return len(u.Variants) > 0
	case *check.Named:
		if seen[u] {
			return true
		}
		seen[u] = true
		if u.Obj.Pkg != pkg {
			return false
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if !printable(pkg, f.Type, seen) {
					return false
				}
			}
			return true
		case *check.Union:
			for _, v := range under.Variants {
				if !printable(pkg, v, seen) {
					return false
				}
			}
			return true
		case *check.Basic, *check.Slice, *check.Map, *check.Tuple:
			return printable(pkg, under, seen)
		}
	}
	return false
}

// sourceText is x's text in the source of the frame's declaration.
func (in *interp) sourceText(fr *frame, x syntax.Node) string {
	f := in.fileOf(fr.pkg, fr.decl)
	if f == nil || int(x.End()) > len(f.Src) {
		return ""
	}
	return f.Src[x.Pos():x.End()]
}

// nativeSite is a site as the native runner names it: the file's base name,
// its line and its column.
func nativeSite(s Site) string {
	return path.Base(strings.ReplaceAll(s.File, "\\", "/")) + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Column)
}

// assertMessage is the report of the failed assert a, at site: the native
// runner's text after "): " on its FAIL line.
func (in *interp) assertMessage(fr *frame, site Site, a *shownAssert) string {
	text := a.text
	if a.shown {
		l, r := in.argument(fr, a.lt, a.left), in.argument(fr, a.rt, a.rite)
		text += ": left " + l + ", right " + r
		if isStringLike(a.lt) && isStringLike(a.rt) {
			ls, lok := a.left.(string)
			rs, rok := a.rite.(string)
			if lok && rok {
				text += assertDiff(ls, rs)
			}
		}
	}
	return "assert failed at " + nativeSite(site) + ": " + text
}

// defectMessage is a defect's report on the native runner's FAIL line; the
// runtime's report of its reasons follows on standard error.
const defectMessage = "a defect, which stops the run"

// lawFalse is res refuted by its law's value at pos, with the native
// runner's report: the failing expression's site, then the law's
// declaration on a line of its own.
func (in *interp) lawFalse(res Result, pkg *check.Package, decl syntax.Decl, pos syntax.Pos) Result {
	res.Status, res.Reason = Refuted, "the law is false"
	res.Site = in.site(pkg, decl, pos)
	res.Message = "law refuted at " + nativeSite(res.Site) + ": the law is false\n\tlaw declared at " + nativeSite(in.site(pkg, decl, decl.Pos()))
	return res
}

// isStringLike reports string or a defined type over it.
func isStringLike(t check.Type) bool {
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	b, ok := t.(*check.Basic)
	return ok && b.Kind == check.String
}

// assertDiff is the native runner's line diff of a failed comparison of
// two strings ($AssertDiff): "" unless they differ and either holds more
// than one line; else a line feed, then each line of the longest common
// subsequence of their lines as "  " + line, a line only the left holds as
// "- " + line, and one only the right holds as "+ " + line.
func assertDiff(a, b string) string {
	if a == b || (!strings.Contains(a, "\n") && !strings.Contains(b, "\n")) {
		return ""
	}
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	out.WriteString("\n--- left\n+++ right")
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out.WriteString("\n  " + x[i])
			i, j = i+1, j+1
		case j == len(y) || (i < len(x) && lcs[i+1][j] >= lcs[i][j+1]):
			out.WriteString("\n- " + x[i])
			i++
		default:
			out.WriteString("\n+ " + y[j])
			j++
		}
	}
	return out.String()
}

// shownText is ergo·shown(left, right), which the rewrite of a harnessed
// test's shown assert calls: the text assertMessage adds after a shown
// assert's source text, ": left L, right R" and the line diff of two
// strings, each operand at the type the checker gave it.
func (in *interp) shownText(fr *frame, x *syntax.CallExpr) (Value, *abrupt) {
	if len(x.Args) != 2 {
		ice("ergo·shown of %d values", len(x.Args))
	}
	var vals [2]Value
	var types [2]check.Type
	for i, a := range x.Args {
		v, ab := in.expr(fr, a)
		if ab != nil {
			return nil, ab
		}
		t, ok := typedExpr(fr.info, a)
		if !ok {
			ice("ergo·shown of an untyped operand")
		}
		vals[i], types[i] = v, t
	}
	text := ": left " + in.argument(fr, types[0], vals[0]) + ", right " + in.argument(fr, types[1], vals[1])
	if isStringLike(types[0]) && isStringLike(types[1]) {
		ls, lok := vals[0].(string)
		rs, rok := vals[1].(string)
		if lok && rok {
			text += assertDiff(ls, rs)
		}
	}
	mustCharge(in, fr, int64(len(text)))
	return text, nil
}
