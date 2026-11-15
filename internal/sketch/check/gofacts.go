package check

import (
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
// them back through GoFacts and GoReads, so they read one decision.

// readsMarker starts a binding's `reads` line.
const readsMarker = "// ergo:reads "

// Fact levels (interop.md, Behavior contracts): a derived fact is checked
// (the pinned toolchain's analysis says it); a vouched fact is a reviewed
// row, validated when its guard law passes (LAWFFI r2).
const (
	FactChecked = "checked"
	FactVouched = "vouched"
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
	if h == nil || len(h.reads[f]) == 0 || f.Sig == nil {
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
	return out
}

// misfitReads reports, on the declaration, each binder of f's reads line
// whose operand is not a slice (S108): the binding is not what ergo bind
// go writes.
func (h *hostPkg) misfitReads(f *Func) {
	for _, name := range h.reads[f] {
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

// GoReads reports a Go call's operand i (a method's receiver first) as one
// the function only reads (a `reads` fact): the lowering may pass it with
// no copy (LAWFFI LF1).
func GoReads(f *Func, operand int) bool {
	for _, fact := range GoFacts(f) {
		if fact.Kind == "reads" && fact.Operand == operand {
			return true
		}
	}
	return false
}
