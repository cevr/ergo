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
// Ergo code reads. A function or method with results of its own returns
// the written slice first (HOST-ROW-3 slice 2):
//
//	// ergo:writes b
//	extern go os.File.Read func (f *File) Read(b []byte) ([]byte, int) fails go.Error needs Ambient
//
// so a read loop is `let (buf, n) = try f.Read(buf)`, and a dead buffer
// is Go's with no copy.

// writesMarker starts a binding's `writes` line.
const writesMarker = "// ergo:writes "

// writesLine records the operand a `// ergo:writes` line above fn names
// (a method's receiver is operand 0). A line that names no parameter of
// fn (a method's receiver is none) is S108: the binding is not what ergo
// bind go writes.
func (h *hostPkg) writesLine(fn *Func, text string) {
	name := strings.TrimSpace(text)
	i := operandIndex(fn, name)
	if i < 0 || fn.Decl.Recv != nil && i == 0 || strings.Contains(name, " ") {
		h.c.errorf(fn.Decl.Name, CodeType, "the `// ergo:writes` line above %s names %s, which is not one of its parameters", fn.Name, name)
		return
	}
	if h.writes == nil {
		h.writes = map[*Func]int{}
	}
	h.writes[fn] = i
}

// misfitWrites reports, on the declaration, a writes line whose parameter
// is not a slice, or is not the function's result or its result tuple's
// first element (S108).
func (h *hostPkg) misfitWrites(f *Func) {
	i, ok := h.writes[f]
	if !ok || f.Sig == nil {
		return
	}
	p := i
	if f.Decl.Recv != nil {
		p--
	}
	if p < 0 || p >= len(f.Sig.Params) {
		return // writesLine reported it
	}
	var first Type
	if len(f.Sig.Results) == 1 {
		first = f.Sig.Results[0]
		if t, ok := first.(*Tuple); ok && len(t.Types) > 1 {
			first = t.Types[0]
		}
	}
	if !h.sliceOperand(f, i) || first == nil || !h.c.identical(first, f.Sig.Params[p].Type) {
		h.c.errorf(f.Decl.Name, CodeType, "the `// ergo:writes` line above %s names %s, which is not a slice the function returns as its result or its first result", f.Name, f.Sig.Params[p].Name)
	}
}

// GoWrites is the index of the operand a Go function or method of import
// go writes in place (its binding's `// ergo:writes` line; a method's
// receiver is operand 0): the call passes Go that operand and its value
// is the operand Go wrote, first in a tuple with Go's own results. ok is
// false for any other function.
func GoWrites(f *Func) (int, bool) {
	h := hostOf(f.Pkg)
	if h == nil {
		return 0, false
	}
	i, ok := h.writes[f]
	return i, ok
}
