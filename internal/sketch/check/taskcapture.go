package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TaskCapture is the transport at a callback's creation. Vars are owning
// moves in first-use order: the child's root registers them before its
// first checkpoint, and the sender drops its entries at admission.
// Borrowed captures retain identity but never gain an owning entry.
type TaskCapture struct {
	Vars     []*Var
	Borrowed []*Var
	Repeated bool
}

func movingBody(call *Call) (int, bool, bool) {
	if call == nil || call.Kind != CallFunc || call.Func == nil || call.Func.Decl == nil || call.Func.Decl.Extern == nil || call.Func.Pkg == nil || !call.Func.Pkg.Std() {
		return 0, false, false
	}
	path, name := call.Func.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return 0, false, false
	}
	return v1.MovingBody(name)
}

// taskFrame checks a literal executed by a moving body. A resource lends
// under R27, otherwise it moves whole, even when only a field is used.
// A nested literal cannot hide a capture. Plain values still copy.
func (w *ownWalk) taskFrame(key syntax.Node, body *syntax.BlockStmt, params []*Var, takes []int, repeated bool) {
	capture := &TaskCapture{Repeated: repeated}
	w.literalCaptures(key, body, capture, true)
	w.keepCallbackLoans(key)
	w.frameCaptured(body, takes, params, w.name, "this task block", false, capture)
}
