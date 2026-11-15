package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
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

// movingBody is the moving callback of the call's bound body (Body.Moves),
// for an extern of any package.
func movingBody(call *Call) (int, bool, bool) {
	if call == nil || call.Kind != CallFunc {
		return 0, false, false
	}
	b := BodyOf(call.Func)
	return b.MovingParam, b.Repeated, b.Moves
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
