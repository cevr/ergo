package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// externForm is one reference form of TestExternCallOnlyDomain: an extern
// reference with the function type it has, the arguments of a direct call,
// and a control: an ordinary function of the same signature.
type externForm struct {
	name    string
	imports string // the imports both the extern and the control use
	decl    string // the extern's declaration ("" for std and prelude)
	ref     string // the extern reference
	typ     string // its function type
	args    string // the arguments of a direct call
	ctlDecl string // the control's declaration
	ctlRef  string // the control reference
}

// externForms is every reference form of the call-only rule's domain. Std
// exports no non-generic extern (task.Go, queue.New, slice.WithCapacity,
// timeout.After and scope.OnExit are generic; log's and trace's externs are
// unexported), so the qualified non-generic form has no row.
var externForms = []externForm{
	{
		name: "user", decl: "extern go strings.ToUpper func Upper(s string) string\n",
		ref: "Upper", typ: "func(string) string", args: `("x")`,
		ctlDecl: "func upper(s string) string { s }\n", ctlRef: "upper",
	},
	{
		name: "prelude", ref: "Sleep", typ: "func(LiveScheduler, duration)", args: "(LiveScheduler, 1s)",
		ctlDecl: "func nap(s LiveScheduler, d duration) {}\n", ctlRef: "nap",
	},
	{
		name: "queue-explicit", imports: "import queue\n",
		ref: "queue.New[int]", typ: "func(int) queue.Queue[int]", args: "(1)",
		ctlDecl: "func mk[T](n int) queue.Queue[T] { queue.New[T](n) }\n", ctlRef: "mk[int]",
	},
	{
		name: "slice-explicit", imports: "import slice\n",
		ref: "slice.WithCapacity[int]", typ: "func(int) []int", args: "(1)",
		ctlDecl: "func wc[T](n int) []T { slice.WithCapacity[T](n) }\n", ctlRef: "wc[int]",
	},
	{
		name: "queue-inferred", imports: "import queue\n",
		ref: "queue.New", typ: "func(int) queue.Queue[int]", args: "(1)",
		ctlDecl: "func mk[T](n int) queue.Queue[T] { queue.New[T](n) }\n", ctlRef: "mk",
	},
}

// externSlot is one position of TestExternCallOnlyDomain. body is main's
// body, top a package-level declaration; R stands for the reference, T for
// its type and A for a direct call's arguments. A callee slot is a direct
// call.
type externSlot struct {
	name   string
	callee bool
	top    string
	body   string
}

var externSlots = []externSlot{
	{name: "callee", callee: true, body: "R A"},
	{name: "trailing-owner", callee: true, body: "R() { }"},
	{name: "let", body: "let v = R\n\t_ = v"},
	{name: "argument", top: "func take(f T) {}\n", body: "take(R)"},
	{name: "field", top: "type Holder { F T }\n", body: "_ = Holder{F: R}"},
	{name: "element", body: "_ = []T{R}"},
	{name: "map-key", body: `_ = Map[string, int]{R: 1}`},
	{name: "map-value", body: `_ = Map[string, T]{"a": R}`},
	{name: "tail", top: "func get() T { R }\n"},
	{name: "return", top: "func get() T { return R }\n"},
	{name: "equal", body: "let k = 1\n\t_ = R == k"},
	{name: "if", body: "if R { }"},
	{name: "match", body: "let m = match R { case _: 1 }\n\t_ = m"},
	{name: "for-in", body: "for x in R { _ = x }"},
	{name: "index-base", body: "_ = (R)[0]"},
	{name: "selector-base", body: "_ = R.Len"},
	{name: "interpolation", body: `_ = "\(R)"`},
	{name: "try", body: "let t = try R\n\t_ = t"},
}

// externProgram is the program of one row, with the byte range of the
// reference's text in it.
func externProgram(f externForm, s externSlot, ref, decl string) (string, int, int) {
	fill := func(text string) string {
		text = strings.ReplaceAll(text, "T", f.typ)
		return strings.ReplaceAll(text, "A", f.args)
	}
	// R is replaced last, after T and A, so the reference's own text is not
	// rewritten; the marker keeps the reference's offset findable.
	const mark = "\x00"
	top, body := fill(strings.ReplaceAll(s.top, "R", mark)), fill(strings.ReplaceAll(s.body, "R", mark))
	src := f.imports + "\n" + decl + top + "\nfunc main() {\n\t" + body + "\n}\n"
	start := strings.Index(src, mark)
	src = strings.Replace(src, mark, ref, 1)
	end := start + len(ref)
	// A slot's own parentheses around the reference are the reference's
	// too (index-base writes (R)[0]).
	for start > 0 && src[start-1] == '(' {
		start--
	}
	return src, start, end
}

// TestExternCallOnlyDomain is A3 of check-v1 stage E4: every reference form
// of an extern, in zero, one and two pairs of parentheses, in every slot,
// against a control of the same signature. The oracle is independent of the
// checker: outside a callee slot an extern reference is exactly one
// diagnostic, S108 with the P3 message, at the reference; in a callee slot
// the rule says nothing; a control never gets the P3 message.
func TestExternCallOnlyDomain(t *testing.T) {
	for _, f := range externForms {
		for parens := 0; parens <= 2; parens++ {
			ref, ctl := f.ref, f.ctlRef
			for i := 0; i < parens; i++ {
				ref, ctl = "("+ref+")", "("+ctl+")"
			}
			for _, s := range externSlots {
				t.Run(fmt.Sprintf("%s/parens=%d/%s", f.name, parens, s.name), func(t *testing.T) {
					src, start, end := externProgram(f, s, ref, f.decl)
					_, diags := check.CheckSource(src)
					p3 := 0
					for _, d := range diags {
						if d.Message == p3Text {
							p3++
						}
					}
					switch {
					case s.callee && p3 != 0:
						t.Errorf("a direct call got the P3 message\n%s\n\t%s", src, describe(diags))
					case !s.callee:
						if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Message != p3Text ||
							diags[0].Offset < start || diags[0].Offset >= end {
							t.Errorf("want exactly one P3 S108 at bytes %d-%d\n%s\n\t%s", start, end, src, describe(diags))
						}
					}
					ctlSrc, _, _ := externProgram(f, s, ctl, f.ctlDecl)
					_, ctlDiags := check.CheckSource(ctlSrc)
					for _, d := range ctlDiags {
						if d.Message == p3Text {
							t.Errorf("the control got the P3 message\n%s\n\t%s", ctlSrc, describe(ctlDiags))
						}
					}
				})
			}
		}
	}
}
