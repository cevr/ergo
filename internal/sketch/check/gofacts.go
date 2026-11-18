package check

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Foreign facts of a Go binding (docs/spec/interop.md, Foreign facts;
// LAWFFI LF0). `ergo bind go` writes a derived fact as a fixed-form comment
// above the declaration, as it writes `// ergo:context`:
//
//	// ergo:reads a
//	extern go sort.SearchInts func SearchInts(a []int, x int) int
//
// names the binders the function only reads: Go's escape analysis at the
// toolchain the `// ergo:bind` line names says it neither writes through,
// keeps, returns a part of nor calls each of them. The checker reads the
// line into the function's facts; the lowering and `ergo inspect` read
// them back through GoFacts, so they read one decision.

// readsMarker starts a binding's `reads` line.
const readsMarker = "// ergo:reads "

// Fact levels (interop.md, Behavior contracts; LAWFFI r2 §1): a derived
// fact is checked (the pinned toolchain's analysis says it); a vouched fact
// is a reviewed row (internal/gofacts), which the checker reads as
// vouched and which acts on nothing; the lowering raises it to validated
// when the ledger holds a current line whose guard run passed
// (lower.factLevel), the only level of a vouched fact that changes code.
const (
	FactChecked   = "checked"
	FactVouched   = "vouched"
	FactValidated = "validated"
)

// ForeignFact is one fact on an operand of a Go function or method of
// import go.
type ForeignFact struct {
	Kind string // "reads"
	// Param is the operand's binder in the binding; Operand its index
	// among the call's operands, a method's receiver first.
	Param   string
	Operand int
	// Level is FactChecked or FactVouched; Source says who established
	// it ("go escape analysis"); Law is the id of the law that guards a
	// vouched fact, "" for a derived one.
	Level, Source, Law string
}

// readsLine records the binders a `// ergo:reads` line above fn names.
// A line that names no binder of fn is S108: the generated binding is
// wrong, and a fact on the wrong operand would skip the wrong copy.
func (h *hostPkg) readsLine(fn *Func, text string) {
	if head, reason, ok := strings.Cut(text, " (vouched: "); ok {
		// `// ergo:reads p (vouched: reason)`: one binder, vouched by the
		// reviewed table (LAWFFI LF2).
		reason, closed := strings.CutSuffix(reason, ")")
		name := strings.TrimSpace(head)
		switch {
		case !closed || reason == "" || strings.ContainsAny(name, " \t"):
			h.c.errorf(fn.Decl.Name, CodeType, "the vouched `// ergo:reads` line above %s is not `// ergo:reads <parameter> (vouched: <reason>)`", fn.Name)
		case operandIndex(fn, name) < 0:
			h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:reads` line above %s names %s, which is not one of its parameters", fn.Name, name)
		default:
			if h.vouched == nil {
				h.vouched = map[*Func][]vouch{}
			}
			h.vouched[fn] = append(h.vouched[fn], vouch{name, reason})
		}
		return
	}
	names := strings.Fields(text)
	if len(names) == 0 {
		h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:reads` line above %s names no parameter", fn.Name)
		return
	}
	for _, name := range names {
		if operandIndex(fn, name) < 0 {
			h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:reads` line above %s names %s, which is not one of its parameters", fn.Name, name)
			continue
		}
		if h.reads == nil {
			h.reads = map[*Func][]string{}
		}
		h.reads[fn] = append(h.reads[fn], name)
	}
}

// effectMarker starts a binding's memory-only line (np4f-plan GBX6):
// `// ergo:effect none (vouched: <reason>)` above a Go function or method
// that `ergo bind go` binds with no `needs go.Foreign`.
const effectMarker = "// ergo:effect "

// effectLine records the memory-only line above fn, or its host line
// (`// ergo:effect ambient (vouched: <reason>)`, HOST-ROW). The row acts
// only on its guard line current and validated (EffectActs, HostActs); any
// other line, or a row that does not act, is S108, since the binding then
// charges less than go.Foreign for Go that may read host state.
func (h *hostPkg) effectLine(fn *Func, text string) {
	if fn.Decl == nil || fn.Decl.Extern == nil {
		return
	}
	target := fn.Decl.Extern.Target.Value
	if strings.HasPrefix(text, "ambient (vouched: ") && strings.HasSuffix(text, ")") {
		h.hostLine(fn, target)
		return
	}
	// `none from <target> (vouched: …)` is a method Go promotes from an
	// embedded field (GB): the row is its declaring type's method of the
	// same name.
	if rest, ok := strings.CutPrefix(text, "none from "); ok {
		from, tail, _ := strings.Cut(rest, " ")
		dot := strings.LastIndex(target, ".")
		if dot < 0 || from == target || !strings.HasSuffix(from, target[dot:]) {
			h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:effect` line above %s names %s, which is not a method of the same name", fn.Name, from)
			return
		}
		target, text = from, "none "+tail
	}
	if !strings.HasPrefix(text, "none (vouched: ") || !strings.HasSuffix(text, ")") {
		h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:effect` line above %s is not `// ergo:effect none (vouched: <reason>)` or `// ergo:effect ambient (vouched: <reason>)`", fn.Name)
		return
	}
	switch ok, refuted, why := EffectActs(target); {
	case refuted:
		h.c.errorf(fn.Decl.Name, CodeType, "%s is bound as memory-only, and its reviewed row does not hold: %s", target, why)
		return
	case !ok:
		h.c.errorf(fn.Decl.Name, CodeType, "%s is bound as memory-only without a reviewed row that acts (%s); regenerate its binding", target, why)
		return
	}
	if h.effect == nil {
		h.effect = map[*Func]bool{}
	}
	h.effect[fn] = true
}

// hostLine records the host line above fn (HOST-ROW): its row in
// gofacts.Hosts acts, and its needs row is Ambient alone, the authority
// the row charges in place of go.Foreign.
func (h *hostPkg) hostLine(fn *Func, target string) {
	if h.host == nil {
		h.host = map[*Func]bool{}
	}
	// The line is read: a row that does not act is its one diagnostic,
	// not also ambientUnvouched's.
	h.host[fn] = false
	switch ok, refuted, why := HostActs(target); {
	case refuted:
		h.c.errorf(fn.Decl.Name, CodeType, "%s is bound as a host call, and its reviewed row does not hold: %s", target, why)
		return
	case !ok:
		h.c.errorf(fn.Decl.Name, CodeType, "%s is bound as a host call without a reviewed row that acts (%s); regenerate its binding", target, why)
		return
	}
	if names := needsNames(fn); len(names) != 1 || names[0] != "Ambient" {
		h.c.errorf(fn.Decl.Name, CodeType, "%s is bound as a host call, so its row is needs Ambient alone", target)
		return
	}
	h.host[fn] = true
}

// ambientUnvouched reports a Go function or method of a binding that needs
// Ambient with no host line that acts (S108): only a reviewed host row
// charges Ambient in place of go.Foreign (HOST-ROW).
func (h *hostPkg) ambientUnvouched(fn *Func) {
	if _, lined := h.host[fn]; lined || fn.Decl == nil || fn.Decl.Extern == nil || !slices.Contains(needsNames(fn), "Ambient") {
		return
	}
	h.c.errorf(fn.Decl.Name, CodeType, "%s is bound with needs Ambient and no host line: only a host row of internal/gofacts charges Ambient for a Go call (`// ergo:effect ambient (vouched: <reason>)`); bind it with needs go.Foreign", fn.Decl.Extern.Target.Value)
}

// needsNames is the names of fn's written needs row.
func needsNames(fn *Func) []string {
	if fn.Decl == nil || fn.Decl.Type == nil || fn.Decl.Type.Needs == nil {
		return nil
	}
	var out []string
	for _, x := range fn.Decl.Type.Needs.Names {
		if id, ok := x.(*syntax.Ident); ok {
			out = append(out, id.Name)
		} else {
			out = append(out, "?")
		}
	}
	return out
}

// GoHost reports a Go function or method of import go bound as a host
// call (`// ergo:effect ambient`; HOST-ROW): it charges Ambient, not
// go.Foreign.
func GoHost(f *Func) bool {
	h := hostOf(f.Pkg)
	return h != nil && h.host[f]
}

// GoEffect reports a Go function or method of import go bound as
// memory-only (`// ergo:effect none`; GBX6): it charges no go.Foreign, and
// it is not pure, so its call is opaque in a footprint (pureGo).
func GoEffect(f *Func) bool {
	h := hostOf(f.Pkg)
	return h != nil && h.effect[f]
}

// operandIndex is the index of the binder name among fn's operands (a
// method's receiver first), or -1.
func operandIndex(fn *Func, name string) int {
	i := 0
	var lists []*syntax.FieldList
	if fn.Decl.Recv != nil {
		lists = append(lists, fn.Decl.Recv)
	}
	if fn.Decl.Type != nil && fn.Decl.Type.Params != nil {
		lists = append(lists, fn.Decl.Type.Params)
	}
	for _, l := range lists {
		for _, field := range l.List {
			if len(field.Names) == 0 {
				i++
				continue
			}
			for _, id := range field.Names {
				if id.Name == name {
					return i
				}
				i++
			}
		}
	}
	return -1
}

// GoFacts is the foreign facts of a Go function or method of import go, in
// the order its binding writes them; none for any other function. A fact
// on an operand whose type is not a slice is no fact: only a slice operand
// has a copy a `reads` fact can skip (ResolveAll reports it, S108). It reads
// what the binding's load recorded and writes nothing, so the lowering's
// workers may call it at once.
func GoFacts(f *Func) []ForeignFact {
	h := hostOf(f.Pkg)
	if h == nil || len(h.reads[f])+len(h.vouched[f]) == 0 || f.Sig == nil {
		return nil
	}
	var out []ForeignFact
	for _, name := range h.reads[f] {
		i := operandIndex(f, name)
		if !h.sliceOperand(f, i) {
			continue
		}
		out = append(out, ForeignFact{Kind: "reads", Param: name, Operand: i, Level: FactChecked, Source: "go escape analysis"})
	}
	for _, v := range h.vouched[f] {
		i := operandIndex(f, v.param)
		if !h.sliceOperand(f, i) {
			continue
		}
		out = append(out, ForeignFact{Kind: "reads", Param: v.param, Operand: i, Level: FactVouched, Source: "vouched: " + v.reason, Law: "go " + strings.Trim(f.Decl.Extern.Target.Value, "\"") + " reads " + v.param})
	}
	return out
}

// vouch is a vouched `reads` line: the binder and the reviewer's reason.
type vouch struct{ param, reason string }

// misfitReads reports, on the declaration, each binder of f's reads line
// whose operand is not a slice (S108): the binding is not what ergo bind
// go writes.
func (h *hostPkg) misfitReads(f *Func) {
	names := slices.Clone(h.reads[f])
	for _, v := range h.vouched[f] {
		names = append(names, v.param)
	}
	for _, name := range names {
		if !h.sliceOperand(f, operandIndex(f, name)) {
			h.c.errorf(f.Decl.Name, CodeType, "the `// ergo:reads` line above %s names %s, which is not a slice", f.Name, name)
		}
	}
}

// sliceOperand reports f's operand i as a slice, or a type over one.
func (h *hostPkg) sliceOperand(f *Func, i int) bool {
	if f.Sig == nil || i < 0 {
		return false
	}
	var t Type
	switch {
	case f.Decl.Recv != nil && i == 0:
		// A value receiver of a binding type (`(x IntSlice)`); a pointer
		// receiver is no slice.
		id, ok := f.Decl.Recv.List[0].Type.(*syntax.Ident)
		if !ok {
			return false
		}
		tn, ok := h.c.pkg.Scope.names[id.Name].(*TypeName)
		if !ok {
			return false
		}
		t = tn.Type
	case f.Decl.Recv != nil:
		i--
		fallthrough
	default:
		if i >= len(f.Sig.Params) {
			return false
		}
		t = f.Sig.Params[i].Type
	}
	_, ok := under(t).(*Slice)
	return ok
}
