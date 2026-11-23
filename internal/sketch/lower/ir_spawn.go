package lower

import (
	"strconv"
	"strings"
)

// GP-SPAWN2: task.Go's block with its captures in the Task. The adapter
// literal of a task.Go block (adaptOperand's childEnv form) is a Go
// closure over the frame's environment and the locals the block reads, a
// heap allocation per spawn that hand Go does not make (its one closure is
// the go statement's, which rt.Start makes too). spawnWith gathers those
// locals in an unnamed struct, passes it by value to v1.GoWith, which
// keeps it in the Task's own allocation, and turns the literal into one
// that captures nothing and reads each of them through its second
// parameter. If the block calls an indirect function value, Go must
// assume the function retains the child environment passed by address.
// Give that environment a stable, separately retained slot in the
// same allocation rather than a heap allocation of its own.

// spawnLowering selects the capture and frame layout for task.Go:
// "" uses GoWith or GoWithFrame, "local-frame" keeps the child
// environment local in GoWith, and "closure" keeps the capture closure
// (v1.Go), GP-SPAWN2's reference. Set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.spawnLowering=closure.
var spawnLowering string

// SetSpawnLoweringForTest sets spawnLowering for a test and returns the
// previous mode.
func SetSpawnLoweringForTest(mode string) string {
	old := spawnLowering
	spawnLowering = mode
	return old
}

// spawnWith is call, v1.Go of the adapter literal of a block the IR built
// from the local mark on, as v1.GoWith of the block's captures, or call
// itself when the block captures nothing or uses a captured local in any
// way but a read of its value: an assignment, its address (a frame
// argument, a pointer method), a store through it. A copy is the same
// value then, since nothing changes it while the block can run.
func (b *irBuilder) spawnWith(call *irExpr, targs string, mark int, p irPos, frame *irStmt) *irExpr {
	if spawnLowering == "closure" || len(call.Args) == 0 {
		return call
	}
	lit := call.Args[len(call.Args)-1]
	if lit.K != "func" || len(lit.Params) != 1 || lit.Params[0] < mark {
		return call
	}
	s := spawnScan{mark: mark, at: map[int]int{}, b: b}
	s.stmts(lit.Body)
	if s.refused || len(s.order) == 0 {
		return call
	}
	task, _ := b.spawnLocalType(lit.Params[0])
	head := "func(" + task + ")"
	if task == "" || !strings.HasPrefix(lit.T, head) {
		return call
	}
	fields := make([]string, len(s.order))
	caps := &irExpr{K: "lit", Pos: p}
	for k, i := range s.order {
		name := "F" + strconv.Itoa(k)
		t, ok := b.spawnLocalType(i)
		if !ok {
			return call
		}
		fields[k] = name + " " + t
		caps.Keys = append(caps.Keys, name)
		caps.Elems = append(caps.Elems, &irExpr{K: "local", Pos: p, I: i})
	}
	ct := "struct{" + strings.Join(fields, "; ") + "}"
	caps.T = ct
	c := b.temp("c", "*"+ct, p)
	// The literal is rebuilt, not edited: a node of it may be shared.
	sub := func(e *irExpr) *irExpr {
		k := s.at[e.I]
		return &irExpr{K: "field", Pos: e.Pos, Name: "F" + strconv.Itoa(k), X: &irExpr{K: "deref", Pos: e.Pos, X: &irExpr{K: "local", Pos: e.Pos, I: c}}}
	}
	nl := *lit
	nl.Params = append(append([]int{}, lit.Params...), c)
	name, params := "GoWith", task+", *"+ct
	targs += ", " + ct
	tree := s.copyTree(sub)
	if spawnLowering != "local-frame" && frame != nil && s.indirect {
		ft, _ := b.spawnLocalType(frame.Local)
		id := b.temp("childEnv", "*"+ft, p)
		tree.putFrame(frame, id)
		name = "GoWithFrame"
		params += ", *" + ft
		targs += ", " + ft
		nl.Params = append(nl.Params, id)
	}
	nl.Body = tree.stmts(lit.Body)
	nl.T = "func(" + params + ")" + strings.TrimPrefix(lit.T, head)
	args := append(append([]*irExpr{}, call.Args[:len(call.Args)-1]...), caps, &nl)
	return &irExpr{K: "pcall", Pos: call.Pos, T: call.T, Name: name, V: targs, Args: args}
}

// spawnScan finds the locals below mark that a block reads (its
// captures), in order, and refuses the block when it uses one otherwise.
type spawnScan struct {
	b        *irBuilder
	mark     int
	at       map[int]int // a captured local's field
	order    []int
	refused  bool
	indirect bool // the child environment may escape through a function value
}

// copyTree replaces each read of a captured local by sub's expression.
// It uses the one tree copy so each match form's facts are the copy's.
func (s *spawnScan) copyTree(sub func(*irExpr) *irExpr) *irTreeCopy {
	return &irTreeCopy{subj: s.subj, expr: func(e *irExpr) *irExpr {
		if e.K == "local" && s.outer(e.I) {
			return sub(e)
		}
		return nil
	}}
}

// putFrame moves the adapter's child environment local into its Task's
// frame slot. The initializer stays on the child at its original
// position; the sender still captures the same parent environment.
func (c *irTreeCopy) putFrame(frame *irStmt, slot int) {
	ptr := func(p irPos) *irExpr { return &irExpr{K: "local", Pos: p, I: slot} }
	value := func(p irPos) *irExpr { return &irExpr{K: "deref", Pos: p, X: ptr(p)} }
	isFrame := func(e *irExpr) bool { return e.K == "local" && e.I == frame.Local }
	capture := c.expr
	c.expr = func(e *irExpr) *irExpr {
		switch {
		case isFrame(e):
			return value(e.Pos)
		case e.K == "addr" && isFrame(e.X):
			return ptr(e.Pos)
		default:
			return capture(e)
		}
	}
	c.stmt = func(old, n *irStmt) {
		if old == frame {
			*n = irStmt{K: "store", Pos: n.Pos, X: value(n.Pos), Y: n.X}
		}
	}
}

func (s *spawnScan) outer(i int) bool { return i < s.mark }

// subj is a match form's subject local in the copy: -1 for a captured one.
func (s *spawnScan) subj(i int) int {
	if s.outer(i) {
		return -1
	}
	return i
}

// pointer reports a local of a pointer type.
func (s *spawnScan) pointer(i int) bool {
	t, ok := s.b.spawnLocalType(i)
	return ok && strings.HasPrefix(t, "*")
}

// spawnLocalType is the IR's Go type of the local i: a temporary's or
// let's as declared, the environment's, or a parameter's of a function
// that is not a method. ok is false for any other (a method's parameter,
// its receiver among them, keeps the closure form).
func (b *irBuilder) spawnLocalType(i int) (string, bool) {
	if t, ok := b.localTypes[i]; ok {
		return t, true
	}
	if i == b.env && b.env >= 0 {
		return "*" + b.l.env(), true
	}
	if !b.params[i] || b.ops == nil || b.ops.f == nil || b.ops.f.Decl == nil || b.ops.f.Decl.Recv != nil {
		return "", false
	}
	for v, l := range b.locals {
		if l == i {
			return b.goType(v.Type, b.ops.f.Decl), true
		}
	}
	return "", false
}

func (s *spawnScan) stmts(list []*irStmt) {
	for _, st := range list {
		s.stmt(st)
	}
}

func (s *spawnScan) stmt(st *irStmt) {
	if st == nil {
		return
	}
	switch st.K {
	case "def", "var":
		// Declares st.Local, the block's own.
	case "set", "def2", "count":
		if s.outer(st.Local) {
			s.refused = true
		}
	case "store":
		if r := root(st.X); r != nil && s.outer(r.I) && !s.pointer(r.I) {
			s.refused = true // a field of a captured struct value
		}
	}
	for _, l := range []*int{st.Key, st.Val} {
		if l != nil && s.outer(*l) {
			s.refused = true
		}
	}
	for _, l := range st.Ls {
		if l >= 0 && s.outer(l) {
			s.refused = true
		}
	}
	s.expr(st.X)
	s.expr(st.Y)
	s.stmts(st.Then)
	s.stmts(st.Else)
	s.stmts(st.Body)
	for _, c := range st.Cases {
		s.stmts(c.Body)
		for _, v := range c.Vals {
			s.expr(v)
		}
	}
}

func (s *spawnScan) expr(e *irExpr) {
	if e == nil {
		return
	}
	switch e.K {
	case "local":
		if s.outer(e.I) {
			if _, seen := s.at[e.I]; !seen {
				s.at[e.I] = len(s.order)
				s.order = append(s.order, e.I)
			}
		}
		return
	case "addr", "mcall":
		// Its address, or a method that may take it (a pointer receiver
		// of a value local).
		if r := root(e.X); r != nil && s.outer(r.I) && !s.pointer(r.I) {
			s.refused = true
		}
	case "result":
		// Result indices are local to the current literal. A move's
		// framed block takes the address of its own named error result.
		return
	case "hole":
		s.refused = true // a pattern operand, not a bound local
		return
	case "vcall":
		s.indirect = s.indirect || e.X.K != "func"
	}
	s.expr(e.X)
	s.expr(e.Y)
	for _, a := range e.Args {
		s.expr(a)
	}
	for _, a := range e.Elems {
		s.expr(a)
	}
	s.stmts(e.Body)
}

// root is the local a chain of field reads of e starts at, or nil.
func root(e *irExpr) *irExpr {
	for e != nil && e.K == "field" {
		e = e.X
	}
	if e != nil && e.K == "local" {
		return e
	}
	return nil
}
