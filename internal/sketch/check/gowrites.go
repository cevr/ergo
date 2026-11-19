package check

import "strings"

// A Go function that writes its slice argument in place (GBX2;
// docs/spec/interop.md, Go writes in place). `ergo bind go` writes the
// reviewed fact (runtime/v1.GoWrites) as a line above the declaration,
// and the Ergo signature returns the slice Go wrote:
//
//	// ergo:writes x
//	extern go slices.Sort func Sort[E ordered](x own []E) []E
//
// so `let s = slices.Sort(s)` is the Ergo form of Go's in-place sort: the
// value rule gives Go a copy, or the dead local itself, and the call's
// value is what Go wrote. Without the line Go would sort a copy that no
// Ergo code reads.

// writesMarker starts a binding's `writes` line.
const writesMarker = "// ergo:writes "

// writesLine records the parameter a `// ergo:writes` line above fn
// names. A line that names no parameter of a function (a method takes
// none) is S108: the binding is not what ergo bind go writes.
func (h *hostPkg) writesLine(fn *Func, text string) {
	name := strings.TrimSpace(text)
	i := operandIndex(fn, name)
	if fn.Decl.Recv != nil || i < 0 || strings.Contains(name, " ") {
		h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:writes` line above %s names %s, which is not one of its parameters", fn.Name, name)
		return
	}
	if h.writes == nil {
		h.writes = map[*Func]int{}
	}
	h.writes[fn] = i
}

// misfitWrites reports, on the declaration, a writes line whose parameter
// is not a slice or is not the function's one result (S108).
func (h *hostPkg) misfitWrites(f *Func) {
	i, ok := h.writes[f]
	if !ok || f.Sig == nil {
		return
	}
	if !h.sliceOperand(f, i) || f.Sig.Fallible || len(f.Sig.Results) != 1 || !h.c.identical(f.Sig.Results[0], f.Sig.Params[i].Type) {
		h.c.errorf(f.Decl.Name, CodeType, "the `// ergo:writes` line above %s names %s, which is not a slice the function returns as its one result", f.Name, f.Sig.Params[i].Name)
	}
}

// GoWrites is the index of the parameter a Go function of import go
// writes in place (its binding's `// ergo:writes` line): the call passes
// Go that operand and its value is the operand Go wrote. ok is false for
// any other function.
func GoWrites(f *Func) (int, bool) {
	h := hostOf(f.Pkg)
	if h == nil {
		return 0, false
	}
	i, ok := h.writes[f]
	return i, ok
}
