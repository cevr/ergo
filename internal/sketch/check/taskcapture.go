package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TaskCapture is the hand-off at a moving callback's creation. Vars are
// in first-use order. The child's root registers them before its first
// checkpoint; the sender drops its entries only after admission succeeds.
type TaskCapture struct {
	Vars     []*Var
	Repeated bool
}

func movingBody(call *Call) (int, bool, bool) {
	if call.Kind != CallFunc || call.Func == nil || call.Func.Decl == nil || call.Func.Decl.Extern == nil || call.Func.Pkg == nil || !call.Func.Pkg.Std() {
		return 0, false, false
	}
	path, name := call.Func.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return 0, false, false
	}
	return v1.MovingBody(name)
}

// taskFrame checks a literal executed by a moving body. A resource moves
// whole, even if the block uses only a field; a nested literal cannot
// hide its capture. Plain values are not tracked and still copy.
func (w *ownWalk) taskFrame(key syntax.Node, body *syntax.BlockStmt, params []*Var, takes []int, repeated bool) {
	capture := &TaskCapture{Repeated: repeated}
	seen := map[*Var]bool{}
	syntax.Inspect(body, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		v, _ := w.info.Uses[id].(*Var)
		i, ok := w.slot[v]
		if !ok || seen[v] {
			return true
		}
		seen[v] = true
		w.move(i, id, hand{at: key, how: "it is captured by a moving task (resources R12)"})
		// The hand-off occurs at task admission, not at this use in the
		// child. The lowering reads TaskCaptures at the call instead.
		delete(w.info.Handoffs, id)
		capture.Vars = append(capture.Vars, v)
		return true
	})
	if len(capture.Vars) > 0 {
		w.info.TaskCaptures[key] = capture
	}
	w.frameCaptured(body, takes, params, w.name, "this task block", false, capture)
}
