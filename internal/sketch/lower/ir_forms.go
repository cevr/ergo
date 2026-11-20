package lower

// IR forms (IR-FORMS IRF-0): node kinds shaped like Ergo's constructs,
// which live only in the builder's memory. The builder writes a form where
// it wrote the gc-shaped kind before; passes (IRF-1 on) rewrite forms;
// eraseForms (ir_forms_erase.go) turns every form back into the kinds the
// reader takes before anything else reads the body. So the IR file, its
// version and the patched compile's reader do not change: a form never
// reaches the file (irNoForms), and the fork patch has 0 lines for them.
//
// The forms:
//
//   - "match" (an irStmt): a match on a union's value. It holds the switch
//     it erases to (X, Cases, Else and their positions, as the builder
//     placed them) and, in F, what a pass needs and the switch has lost:
//     the union's layout (a sealed interface, a flat union, a scalar-form
//     local), the local the subject is read into, and per case the
//     variants it names (by tag) and the statement that binds `T as x`.
//   - "variant" (an irExpr): a variant put into its union. X is the
//     variant's value; F holds the union's Go type, the variant's tag and
//     the layout. It erases to Go's conversion to the sealed interface
//     ("toiface") or to the flat union's of method on its zero value.
//
// The reference is irFormLowering "off" (selectable by -X, or per run by
// LowerIRReference): the builder erases each form as it makes it, so no
// form ever exists, and the body is the one the builder wrote before
// IR-FORMS. TestFormsEraseToTheReference holds the two equal, byte for
// byte, on the examples and on irspike's generated programs.
//
// Prior art: MLIR's dialects (each form has a verifier and one lowering
// to the next level, and a pass never sees a half-lowered form) and GHC's
// Core (case and constructor application kept whole until code
// generation, so case-of-known-constructor is a local rewrite).

import (
	"fmt"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
)

// irFormLowering selects the forms: "" builds them and erases them after the
// body is built; "off" is the reference, which never builds one.
var irFormLowering string

// Layouts of a union, as the lowering decided it (flatOf).
const (
	layoutIface  = "iface"  // a sealed Go interface
	layoutFlat   = "flat"   // a flat union: a tag and slots (U1)
	layoutScalar = "scalar" // a scalar-form local: a tag local and slot locals
)

// irMatch is a "match" form's Ergo-level facts.
type irMatch struct {
	// Kind is the switch the form erases to: "tswitch" or "switch".
	Kind   string
	Layout string
	// Subj is the local the subject is read into (-1 for a scalar-form
	// local, whose tag and slots are locals of their own).
	Subj int
	// Arms are the facts of each case, in the order of the statement's
	// Cases.
	Arms []irArm
}

// irArm is one case of a "match" form.
type irArm struct {
	// Tags are the variants the case names, by the tag a variant form of
	// the same layout carries (irVariant.Tag): a flat union's or a
	// scalar-form local's tag, an interface's variant index (unionTag);
	// none for a default.
	Tags []int
	// Bind is the case's `T as x`, the first statement of its body, or nil.
	Bind *irStmt
}

// irVariant is a "variant" form's facts: the union's Go type is the
// expression's T.
type irVariant struct {
	Tag    int
	Layout string
	// Of is the flat union's of method that puts the variant in (ofName).
	Of string
}

// formsOff reports the reference: no form is built.
func (b *irBuilder) formsOff() bool {
	return irFormLowering == "off" || b.st != nil && b.st.formsOff
}

// sawForm counts a form the eraser met in the report (IRReport.Forms).
func (st *irState) sawForm(kind, layout string) {
	if st.report.Forms == nil {
		st.report.Forms = map[string]int{}
	}
	st.report.Forms[kind+"/"+layout]++
}

// matchForm makes the switch sw of a match on a union a "match" form, with
// its layout, the local its subject is in and its arms' facts (one per
// case of sw); the reference keeps sw.
func (b *irBuilder) matchForm(sw *irStmt, layout string, subj int, arms []irArm) *irStmt {
	if len(arms) != len(sw.Cases) {
		panic(fmt.Sprintf("ergo: match form: %d arms for %d cases", len(arms), len(sw.Cases)))
	}
	if b.formsOff() {
		return sw
	}
	sw.F = &irMatch{Kind: sw.K, Layout: layout, Subj: subj, Arms: arms}
	sw.K = "match"
	return sw
}

// variantForm is e, a value of variant tag of the union of Go type gt, put
// into its union: a "variant" form, or in the reference its erasure.
func (b *irBuilder) variantForm(e *irExpr, gt string, tag int, layout, of string) *irExpr {
	if b.formsOff() {
		// The builder's own kinds, as before IR-FORMS (not eraseVariant:
		// the reference shares no code with the erasure it checks).
		if layout == layoutFlat {
			return &irExpr{K: "mcall", Pos: e.Pos, Name: of, X: &irExpr{K: "zero", Pos: e.Pos, T: gt}, Args: []*irExpr{e}}
		}
		return &irExpr{K: "toiface", Pos: e.Pos, T: gt, X: e}
	}
	return &irExpr{K: "variant", Pos: e.Pos, T: gt, X: e, F: &irVariant{Tag: tag, Layout: layout, Of: of}}
}

// unionTag is the index of the variant v in the union u's variant order,
// or -1 (a variant is a named type: its declaration names it, as tagOf).
func unionTag(u check.Type, v check.Type) int {
	un, ok := underlying(u).(*check.Union)
	n, named := v.(*check.Named)
	if !ok || !named {
		return -1
	}
	for i, w := range un.Variants {
		if wn, ok := w.(*check.Named); ok && wn.Obj == n.Obj {
			return i
		}
	}
	return -1
}

// copyFor is the match facts m of a statement whose cases are from, for
// a copy of the statement whose cases are to, its locals renamed by local
// (-1: the copy no longer holds the subject in a local). The one IR tree
// copy (ir_treecopy.go: the inliner's clone, the spawn's capture copy)
// copies a form's facts with it by this rule: an arm's bind, the first statement
// of its case's body, is the copy's first statement there, so the facts
// point into the copy, not the original (MATCH-BIND-ICE: a copy that kept
// the original's facts failed verifyMatch). A bind out of its place is
// kept, so verifyMatch still reports it.
func (m *irMatch) copyFor(from, to []*irCase, local func(int) int) *irMatch {
	if m == nil {
		return nil
	}
	n := *m
	n.Arms = make([]irArm, len(m.Arms))
	for i, a := range m.Arms {
		n.Arms[i] = irArm{Tags: append([]int(nil), a.Tags...), Bind: a.Bind}
		if a.Bind != nil && i < len(from) && i < len(to) && len(from[i].Body) > 0 && from[i].Body[0] == a.Bind && len(to[i].Body) > 0 {
			n.Arms[i].Bind = to[i].Body[0]
		}
	}
	if n.Subj >= 0 {
		n.Subj = local(n.Subj)
	}
	return &n
}

// verifyMatch reports what is wrong with a "match" form, "" when nothing:
// its erased kind fits its layout and each case names as many variants as
// it has types (interface) or values (flat, scalar).
func verifyMatch(s *irStmt) string {
	f := s.F
	if f == nil {
		return "a match form without its facts"
	}
	switch {
	case f.Layout == layoutIface && f.Kind != "tswitch",
		f.Layout != layoutIface && f.Kind != "switch",
		f.Layout != layoutIface && f.Layout != layoutFlat && f.Layout != layoutScalar:
		return "a match form of layout " + f.Layout + " that erases to " + f.Kind
	case len(f.Arms) != len(s.Cases):
		return "a match form with " + strconv.Itoa(len(f.Arms)) + " arms for " + strconv.Itoa(len(s.Cases)) + " cases"
	}
	for i, c := range s.Cases {
		n := len(c.Vals)
		if f.Layout == layoutIface {
			n = len(c.Types)
		}
		if !c.Def && len(f.Arms[i].Tags) != n {
			return "a match form's case " + strconv.Itoa(i) + " names " + strconv.Itoa(len(f.Arms[i].Tags)) + " variants for " + strconv.Itoa(n) + " tests"
		}
		if b := f.Arms[i].Bind; b != nil && (len(c.Body) == 0 || c.Body[0] != b) {
			return "a match form's case " + strconv.Itoa(i) + " binds outside its body's start"
		}
	}
	return ""
}

// verifyVariant reports what is wrong with a "variant" form, "" when
// nothing.
func verifyVariant(x *irExpr) string {
	f := x.F
	switch {
	case f == nil:
		return "a variant form without its facts"
	case x.X == nil:
		return "a variant form without its value"
	case f.Layout == layoutFlat && f.Of == "":
		return "a flat variant form without its of method"
	case f.Layout != layoutFlat && f.Layout != layoutIface:
		return "a variant form of layout " + f.Layout
	}
	return ""
}
