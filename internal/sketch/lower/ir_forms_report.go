package lower

// The forms report (IR-FORMS IRF-6, `ergo inspect forms`): each site where
// a pass over the IR decided, with the form it looked at, the pass, and
// whether it fired or the reason it did not. A pass names its decisions
// from its own table of reasons (formReasons), so the command, the docs
// and the tests read one list; a pass that records a reason outside it is
// an internal error. See ir.go.

import (
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// FormSite is one decision of a pass over the IR at a source site.
type FormSite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	// Func is the Go name of the body that holds the site.
	Func string `json:"func"`
	// Form is the construct the pass looked at ("call": a call and its
	// facts), Pass the pass.
	Form string `json:"form"`
	Pass string `json:"pass"`
	// Fired reports the pass changed the site; Reason says what it did, or
	// why it did not.
	Fired  bool   `json:"fired"`
	Reason string `json:"reason"`
}

// FormReason is one entry of a pass's table: what it says at a site.
type FormReason struct {
	Form   string `json:"form"`
	Pass   string `json:"pass"`
	Fired  bool   `json:"fired"`
	Reason string `json:"reason"`
}

// The memo pass (ir_memo.go, LAWFFI LF3a): a loop-invariant pure call
// computed once per entry of its loop.
var (
	memoFired      = &FormReason{"call", "memo", true, "computed once per entry of its loop"}
	memoImpure     = &FormReason{"call", "memo", false, "the callee is not pure"}
	memoKind       = &FormReason{"call", "memo", false, "a method, a task, a call with a block or a call site"}
	memoNotBasic   = &FormReason{"call", "memo", false, "an operand or the result is not a number, a bool or a string"}
	memoVariant    = &FormReason{"call", "memo", false, "an operand may change in the loop"}
	memoStatements = &FormReason{"call", "memo", false, "statements run before the call"}
)

// formReasons is every reason a pass may give, by pass, in the order the
// docs list them (docs/spec/inspect.md, Forms).
var formReasons = []*FormReason{memoFired, memoImpure, memoKind, memoNotBasic, memoVariant, memoStatements,
	mergeFired, mergeWritten, mergeNotValue, mergeNotFirst, // ir_merge.go
	foldFired, foldOtherGo, foldNotConst} // ir_fold.go

// FormReasons is every reason a pass may give: the vocabulary of
// `ergo inspect forms`.
func FormReasons() []FormReason {
	out := make([]FormReason, len(formReasons))
	for i, r := range formReasons {
		out[i] = *r
	}
	return out
}

// formSite records r at n in the body being built. The body's sites reach
// the report only when its IR body is kept (lowerIRBody): a body that
// falls back to the emitter has no pass.
func (b *irBuilder) formSite(n syntax.Node, r *FormReason) {
	known := false
	for _, k := range formReasons {
		known = known || k == r
	}
	if !known {
		ice(n, "a form reason outside formReasons: %q", r.Reason)
	}
	if _, ok := b.l.sourceOf(n); !ok {
		// A std body's sites are std's own: the report is the program's.
		return
	}
	file, line, col := b.l.locate(n, n.Pos())
	b.forms = append(b.forms, FormSite{File: file, Line: line, Column: col, Func: b.fn.Name, Form: r.Form, Pass: r.Pass, Fired: r.Fired, Reason: r.Reason})
}

// sortFormSites orders the report by position, then pass.
func sortFormSites(s []FormSite) {
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Pass < b.Pass
	})
}

// FormsText is sites as ergo inspect forms prints them, one line each:
// position, form, pass, fired or not, and the reason.
func FormsText(sites []FormSite) string {
	if len(sites) == 0 {
		return "no form site\n"
	}
	var b strings.Builder
	for _, s := range sites {
		verdict := "not fired"
		if s.Fired {
			verdict = "fired"
		}
		b.WriteString(s.File + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Column) + "  " + s.Form + "  " + s.Pass + "  " + verdict + "  " + s.Func + "  (" + s.Reason + ")\n")
	}
	return b.String()
}
