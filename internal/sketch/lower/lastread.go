package lower

// The last read of a collect's source (FLIP F7 GP-RESLICE). A collect
// (loops.go, DT4)
//
//	for i in lo..hi { xs = xs.Append(Q[i+s]) }
//
// reads Q once per index. When that loop is Q's last use, and Q's array is
// shared with no other value, nothing reads Q's elements after the loop:
// a slice that takes the part Q[a:b] itself, with no copy, holds storage
// no other value sees, as the copy would (ir_collectfold.go writes it).
//
// lastRead holds the name Q in the Append of each such loop. It is the
// value rule's freshness (freshPath) read over the loop as one use: Q is
// a local of this frame (not a pointer, captured by no other frame), no
// use of Q is inside the loop but this one or after the loop, no earlier
// use shares Q's array (an escape that was not copied or handed over),
// the loop runs once per run of Q's binding (no loop around it that is not
// around Q's let), and Q's value is unshared (a let mut, or a let of a
// fresh value).
//
// Prior art: Koka's Perceus and Lean 4's reset/reuse reuse a cell whose
// reference count is one at its last use; Swift's copy-on-write checks
// uniqueness at run time. Here the uniqueness is the value rule's
// build-time proof, so no count and no check runs.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// lastReads notes the source of each collect loop of this body whose read
// is the source's last and unshared (vf.lastRead).
func (w *valueWalk) lastReads() {
	for _, call := range w.calls {
		c := w.info.Calls[call]
		if c == nil || !preludeAppend(c.Func) {
			continue
		}
		parent, child := w.unparenParent(call)
		as, ok := parent.(*syntax.AssignStmt)
		if !ok || len(as.Rhs) != 1 || as.Rhs[0] != child {
			continue
		}
		block, ok := w.parent[as].(*syntax.BlockStmt)
		if !ok || len(block.List) != 1 {
			continue
		}
		loop, ok := w.parent[block].(*syntax.RangeStmt)
		if !ok || loop.Body != block {
			continue
		}
		args := call.Args
		if c.Recv == nil && len(args) > 0 {
			args = args[1:]
		}
		if len(args) != 1 {
			continue
		}
		ix, ok := unparen(args[0]).(*syntax.IndexExpr)
		if !ok {
			continue
		}
		id, ok := unparen(ix.X).(*syntax.Ident)
		if !ok {
			continue
		}
		v, ok := w.info.Uses[id].(*check.Var)
		if ok && w.freshAfter(w.varOf(v), id, loop) {
			w.vf.lastRead[id] = true
		}
	}
}

// freshAfter reports that the use id of vv inside the loop is vv's last
// and its value unshared (see the file comment).
func (w *valueWalk) freshAfter(vv *vvar, id *syntax.Ident, loop syntax.Node) bool {
	if !vv.local || vv.ptr {
		return false
	}
	var use *vocc
	for _, o := range vv.occs {
		if o.id == id {
			use = o
		}
	}
	if use == nil || use.frame != vv.frame {
		return false
	}
	for _, l := range use.loops {
		if l != loop && !containsNode(vv.loops, l) {
			return false
		}
	}
	for _, o := range vv.occs {
		if o == use {
			continue
		}
		if o.frame != vv.frame || o.pre >= w.pre[loop] {
			return false // captured, or read in the loop or after it
		}
		if (o.kind == occEscape || o.kind == occReturn) && !w.vf.clone[o.path] && !w.vf.handed[o.path] {
			return false // shared before the loop
		}
	}
	return vv.mut || vv.handed || vv.init != nil && w.fresh(vv.init)
}
