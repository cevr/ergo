package check

import (
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Task escape (check-v1 stage D3a; #196 "Task ownership in v1"). A Task
// stays in the frame that holds it: it leaves the frame only by return, so
// the task's lifetime stays inside the scope that started it. One property
// and two rules decide it, each in one place:
//
//  1. holdsTask, the property: a type holds a Task when it is a Task or one
//     is in its structure (a type argument, a declared body, an element, a
//     field, a variant). It is coinductive over interned type identity: a
//     back edge contributes nothing, so a recursive type terminates, and
//     the walk visits each type of the declaration graph once (Tarjan's
//     strongly connected components, cached when a component completes).
//     An operation type and a function type hold none: the Task guard of
//     assignability (typeops.go, clause 3) keeps a Task out of an operation
//     value, and calling a function may make a Task.
//  2. The type-position rule: a Task-holding type is only the whole type of
//     a local, a parameter or a result (a function type's parameters and
//     results included). As a slice or Map element or key, a tuple element,
//     a borrowed type, a record or variant field, a defined type's base or
//     a package let's type it is S108 at the type expression, and the type
//     is invalid. A type argument is rule 4's.
//  3. The use rule, one pass over every body once the bodies are typed
//     (checkTaskUses): a Task-holding value may be bound by a local let,
//     returned (a return, or a block's final value that leaves through an
//     if, a match, a catch, a try or a with), passed as an argument, or be
//     the receiver of a called Wait or Stop (or of a dot call). Every other
//     use is S108 at the value, and a reference to a Task-holding local
//     from another frame (a function literal, a trailing block, a go
//     block) is S108 wherever it stands. A refused value's parts and the
//     locals its let binds add no diagnostic.
//  4. The type-argument rule (check-v1 stage E2, C13): a type argument,
//     written or inferred, holds no Task, the prelude's Task[T, E] included.
//     Every binding of a generic declaration's type parameters to arguments
//     is one instantiation, and every instantiation passes through
//     typeArgsAdmitted: a type expression G[A] (instantiateExprs), an
//     explicit function instance F[A], an inferred call, an inferred
//     composite literal and a dot call's receiver binding. An argument that
//     holds a Task is S108 at the instantiation, which is invalid. A generic
//     body is then never instantiated with a Task, so the type-position
//     rule and the use rule, checked once over the generic declaration,
//     hold for each of its instances. #197 designs aggregate ownership.
//
// Info.TaskResults records every function whose result holds a Task.
//
// Prior art: Rust's auto traits (Send) are coinductive over the type graph
// and a `dyn Trait + 'scope` value cannot outlive its scope; Rust's scoped
// threads, Kotlin's structured concurrency and Trio's nurseries bind a task
// to the scope that started it. Swift's generic parameters are implicitly
// Copyable and Escapable, so a non-copyable or non-escapable type is no
// type argument unless the parameter says `~Copyable`; rule 4 is that
// default without the opt-out. Go has no such rule: a goroutine outlives
// its caller. Gleam, Elixir and ReScript have no task ownership in types.

const taskEscapeText = "a Task leaves its function only by return; wait on it or return it"

// setEscapeText is taskEscapeText for a value that is a tasks.Set.
const setEscapeText = "a tasks.Set leaves its function only by return; stop it or return it"

// ownedEscapeText is the text of an escape of a value of type t: a Set's own,
// else a Task's.
func ownedEscapeText(t Type) string {
	if isSet(t) {
		return setEscapeText
	}
	return taskEscapeText
}

// typeArgsAdmitted applies the type-argument rule (rule 4) to one
// instantiation of the generic declaration name, tparams := targs, at the
// node at: when an argument of a type parameter holds a Task it reports
// S108 and returns false, and the caller's instantiation is invalid.
// Otherwise it records the instantiation's edges for the expansion check
// (mono.go) and returns true. It is the only caller of
// recordInstantiation (TestTypeArgSitesPassTheTaskRule).
func (c *checker) typeArgsAdmitted(name string, tparams []*TypeParam, targs []Type, at syntax.Node) bool {
	for i, tp := range tparams {
		if i >= len(targs) || tp.Kind != KindType {
			continue
		}
		a := targs[i]
		if a == nil || a == NoValue || isInvalid(a) || !c.holdsTask(a) {
			continue
		}
		c.errorf(at, CodeType, "the type argument %s of %s holds a Task, and a type argument never does: %s", a, name, taskEscapeText)
		return false
	}
	c.recordInstantiation(name, tparams, targs, at)
	return true
}

// isTask reports whether t is a type whose value a frame owns: the
// prelude's Task[T, E], std tasks.Set[E] (runtime.md, G4), or an instance
// of one. A Set is an owner of tasks, so it stays in its frame by the same
// rules as a Task (owner q39: one rule): it leaves only by return, which
// moves it to the caller, and its owner's end stops and drains it.
func isTask(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	if o == nil || o.Pkg == nil {
		return false
	}
	switch {
	case o.Pkg.Path == std.PreludePath:
		return o.Name == "Task"
	case o.Pkg.std && o.Pkg.Path == "tasks":
		return o.Name == "Set"
	}
	return false
}

// isSet reports whether t is std tasks.Set[E] or an instance of it.
func isSet(t Type) bool {
	n, ok := t.(*Named)
	return ok && isTask(t) && n.origin().Obj.Name == "Set"
}

// ResultIsTask reports whether a function of signature s returns a Task
// itself: its one result is the prelude's Task[T, E] or an instance of it
// (isTask, the base case of holdsTask). The lowering reads it for function
// types, literals, function values and method values: such a function
// takes the caller's frame scope as its destination (stdlib.md, Hidden
// arguments). A result that holds a Task inside another type is
// TaskResults' and Call.TaskResult's (holdsTask). Added in check-v1 stage
// D3b round 2 (additive).
func (s *Signature) ResultIsTask() bool {
	return len(s.Results) == 1 && isTask(s.Results[0])
}

// taskNode is one type on the Task walk's stack (Tarjan).
type taskNode struct {
	index, low int
	onStack    bool
	holds      bool
	// final is false for a type whose id depends on an unbound inference
	// variable or a declaration still being resolved: its component is
	// not cached.
	final bool
}

// taskWalk is one holdsTask walk: the nodes it has visited and the stack
// of the component it is building.
type taskWalk struct {
	c     *checker
	nodes map[int32]*taskNode
	stack []int32
	next  int
}

// noLow is the low link of a finished node: it lowers no other node's.
const noLow = int(^uint(0) >> 1)

// holdsTask reports whether a value of type t holds a Task (rule 1).
func (c *checker) holdsTask(t Type) bool {
	w := taskWalk{c: c}
	holds, _ := w.visit(t)
	return holds
}

// visit returns whether t holds a Task as far as the walk knows, and t's
// low link: noLow once t's component is finished, else the index of the
// earliest node on the stack t reaches.
func (w *taskWalk) visit(t Type) (bool, int) {
	c := w.c
	c.taskSteps++
	switch t.(type) {
	case *Named, *Slice, *Map, *Tuple, *Pointer, *Struct, *Union:
	default:
		// A basic type, a type parameter, a row, an operation type
		// (Interface) and a function type (Signature) hold no Task.
		return false, noLow
	}
	id, infer := c.types.intern(t)
	if holds, ok := c.taskMemo[id]; ok {
		return holds, noLow
	}
	if n := w.nodes[id]; n != nil {
		if n.onStack {
			// A back edge: the cycle adds no Task of its own.
			return n.holds, n.index
		}
		return n.holds, noLow
	}
	if w.nodes == nil {
		w.nodes = map[int32]*taskNode{}
	}
	n := &taskNode{index: w.next, low: w.next, onStack: true, final: !infer}
	w.next++
	w.nodes[id] = n
	w.stack = append(w.stack, id)
	edge := func(u Type) {
		if n.holds {
			// A Task is already found: the component holds one, whatever
			// the remaining edges reach.
			return
		}
		h, low := w.visit(u)
		n.holds = n.holds || h
		n.low = min(n.low, low)
	}
	switch t := t.(type) {
	case *Named:
		n.holds = isTask(t)
		for _, a := range t.TArgs {
			edge(a)
		}
		// The declared body, not the instance's substitution: a Task
		// that substitution brings in is already in TArgs, and a growing
		// generic (type E[T] []E[[]T]) stays a finite graph.
		o := t.origin()
		if o.state != 2 {
			n.final = false
		} else {
			edge(o.underlying())
		}
	case *Slice:
		edge(t.Elem)
	case *Map:
		edge(t.Key)
		edge(t.Elem)
	case *Tuple:
		for _, e := range t.Types {
			edge(e)
		}
	case *Pointer:
		edge(t.Elem)
	case *Struct:
		for _, f := range t.Fields {
			edge(f.Type)
		}
	case *Union:
		for _, v := range t.Variants {
			edge(v)
		}
	}
	if n.low < n.index {
		return n.holds, n.low
	}
	// n roots a component: every member reaches every other, so each
	// holds a Task when any of them does.
	i := len(w.stack) - 1
	for w.stack[i] != id {
		i--
	}
	members := w.stack[i:]
	holds, final := false, true
	for _, m := range members {
		holds = holds || w.nodes[m].holds
		final = final && w.nodes[m].final
	}
	for _, m := range members {
		mn := w.nodes[m]
		mn.holds, mn.onStack = holds, false
		if final {
			if c.taskMemo == nil {
				c.taskMemo = map[int32]bool{}
			}
			c.taskMemo[m] = holds
		}
	}
	w.stack = w.stack[:i]
	return holds, noLow
}

// resultsHoldTask reports whether a function's result holds a Task.
func (c *checker) resultsHoldTask(sig *Signature) bool {
	for _, r := range sig.Results {
		if c.holdsTask(r) {
			return true
		}
	}
	return false
}

// taskRefused applies the type-position rule (rule 2) to the type
// expression x of type t: when t holds a Task it reports S108 and returns
// true, and the caller's type is invalid.
func (c *checker) taskRefused(x syntax.Expr, t Type) bool {
	if isInvalid(t) || !c.holdsTask(t) {
		return false
	}
	c.errorf(x, CodeType, "%s", ownedEscapeText(t))
	return true
}

// checkTaskUses runs the use rule (rule 3) over every file.
func (c *checker) checkTaskUses() {
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, f := range c.files {
		c.cur = f
		w := &taskUses{c: c, info: c.pkg.Info, varFrame: map[*Var]syntax.Node{}, quietVars: map[*Var]bool{}}
		for _, d := range f.syntax.Decls {
			w.visit(d, false)
		}
	}
}

// taskUses is the use rule's walk over one file.
type taskUses struct {
	c    *checker
	info *Info
	// stack is the path from the declaration to the node being visited.
	stack []syntax.Node
	// frames are the function bodies around the node: a declaration's,
	// a function literal's, a trailing block's and a go block's.
	frames []syntax.Node
	// varFrame is the frame that declares each local.
	varFrame map[*Var]syntax.Node
	// quietVars are the locals a refused let binds: their uses add nothing.
	quietVars map[*Var]bool
	refused   int
}

func (w *taskUses) visit(n syntax.Node, quiet bool) {
	if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
		return
	}
	if x, ok := n.(syntax.Expr); ok && w.c.intoInvalid[x] {
		// Its target's refusal covers it.
		quiet = true
	}
	if id, ok := n.(*syntax.Ident); ok {
		if obj, ok := w.info.Defs[id]; ok {
			if v, ok := obj.(*Var); ok {
				w.varFrame[v] = w.frame()
			}
			return
		}
	}
	if x, ok := n.(syntax.Expr); ok && !quiet && w.escapes(x) {
		if !w.reported(x) {
			w.c.errorf(x, CodeType, "%s", ownedEscapeText(w.info.Types[x]))
		}
		w.refused++
		quiet = true
	}
	quiet = quiet || w.quietBelow(n)
	frame := w.isFrame(n)
	if frame {
		w.frames = append(w.frames, n)
	}
	before := w.refused
	w.stack = append(w.stack, n)
	syntax.Inspect(n, func(m syntax.Node) bool {
		if m == n {
			return true
		}
		w.visit(m, quiet || w.childQuiet(n, m))
		return false
	})
	w.stack = w.stack[:len(w.stack)-1]
	if frame {
		w.frames = w.frames[:len(w.frames)-1]
	}
	if spec, ok := n.(*syntax.VarSpec); ok && w.refused > before {
		syntax.Inspect(spec, func(m syntax.Node) bool {
			if id, ok := m.(*syntax.Ident); ok {
				if v, ok := w.info.Defs[id].(*Var); ok {
					w.quietVars[v] = true
				}
			}
			return true
		})
	}
}

// frame is the innermost frame, nil at package level.
func (w *taskUses) frame() syntax.Node {
	if len(w.frames) == 0 {
		return nil
	}
	return w.frames[len(w.frames)-1]
}

func (w *taskUses) parent() syntax.Node {
	if len(w.stack) == 0 {
		return nil
	}
	return w.stack[len(w.stack)-1]
}

// isFrame reports whether n opens a frame: a function, test or law body, a
// function literal, or a trailing block of a call other than with.
func (w *taskUses) isFrame(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl, *syntax.FuncLit:
		return true
	case *syntax.BlockStmt:
		switch p := w.parent().(type) {
		case *syntax.CallExpr:
			if p.Trailing != n {
				return false
			}
			call := w.info.Calls[p]
			return call == nil || call.Kind != CallWith
		}
	}
	return false
}

// quietBelow reports whether n's parts add no diagnostic: a pattern, or a
// value whose own check failed (it has no type), so its parts are already
// covered by that diagnostic.
func (w *taskUses) quietBelow(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.CompositeLit, *syntax.TupleExpr, *syntax.UnaryExpr, *syntax.BinaryExpr, *syntax.IndexExpr, *syntax.SliceExpr:
		if _, ok := w.info.Types[n.(syntax.Expr)]; !ok {
			return true
		}
	case *syntax.AssignStmt:
		// An assignment to a target of no type is reported there.
		for _, l := range n.Lhs {
			if id, ok := unparen(l).(*syntax.Ident); ok && id.Name == "_" {
				continue
			}
			if _, ok := w.info.Types[l]; !ok {
				return true
			}
		}
	}
	if x, ok := n.(syntax.Expr); ok {
		if _, ok := w.info.Patterns[x]; ok {
			return true
		}
	}
	return false
}

// childQuiet reports whether the part m of n adds no diagnostic of its own.
func (w *taskUses) childQuiet(n, m syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.AssignStmt:
		// A target is not a use of the value it held; the assigned value
		// is the use the rule decides.
		for _, l := range n.Lhs {
			if l == m {
				return true
			}
		}
	case *syntax.CallExpr:
		// A call that failed to check, or a conversion to a refused type,
		// is reported; its arguments add nothing.
		call := w.info.Calls[n]
		_, typed := w.info.Types[n]
		if call == nil || call.Kind == CallConversion && !typed {
			for _, a := range n.Args {
				if a == m {
					return true
				}
			}
		}
	}
	return false
}

// reported reports whether a diagnostic already lies on x: the Task guard
// of assignability reports an escape into an operation value at the value.
func (w *taskUses) reported(x syntax.Node) bool {
	pos, end := int(x.Pos()), int(x.End())
	for _, d := range w.c.diags {
		if d.File == w.c.cur.index && d.Offset >= pos && d.Offset < end {
			return true
		}
	}
	return false
}

// escapes reports whether the value x holds a Task and stands where the
// use rule refuses it.
func (w *taskUses) escapes(x syntax.Expr) bool {
	if _, ok := x.(*syntax.ParenExpr); ok {
		return false
	}
	t, ok := w.info.Types[x]
	if !ok || !w.c.holdsTask(t) {
		return false
	}
	if id, ok := x.(*syntax.Ident); ok {
		if v, ok := w.info.Uses[id].(*Var); ok {
			if v.pkg || w.quietVars[v] {
				// A package let that holds a Task is refused at its
				// declaration; so is a let whose value was refused.
				return false
			}
			if f, ok := w.varFrame[v]; ok && f != w.frame() {
				return true
			}
		}
	}
	return !w.allowed(x)
}

// allowed reports whether x stands where a Task-holding value may.
func (w *taskUses) allowed(x syntax.Expr) bool {
	child, i := syntax.Node(x), len(w.stack)-1
	for i >= 0 {
		if _, ok := w.stack[i].(*syntax.ParenExpr); !ok {
			break
		}
		child, i = w.stack[i], i-1
	}
	if i < 0 {
		return false
	}
	switch p := w.stack[i].(type) {
	case *syntax.VarSpec:
		// A local let binds it in its own frame; a package let is no
		// frame, and `let _ = t` binds nothing: it discards the Task.
		if w.frame() == nil {
			return false
		}
		if p.Pattern != nil {
			return true
		}
		for _, n := range p.Names {
			if n.Name != "_" {
				return true
			}
		}
		return false
	case *syntax.ReturnStmt:
		return true
	case *syntax.ExprStmt:
		_, tail := w.info.Tails[p]
		return tail
	case *syntax.CallExpr:
		if p.Fun == child {
			return true
		}
		call := w.info.Calls[p]
		if call == nil {
			return true
		}
		switch call.Kind {
		case CallFunc, CallOp, CallProviderOp, CallLawOp, CallFuncValue:
			return true
		}
		return false
	case *syntax.SelectorExpr:
		return w.receiver(x, p, i)
	case *syntax.TryExpr, *syntax.CatchExpr:
		// The value leaves through the try or the catch, which the rule
		// decides in turn.
		return true
	}
	// A block-final if, match or catch is the block's value.
	if s, ok := child.(syntax.Stmt); ok {
		_, tail := w.info.Tails[s]
		return tail
	}
	return false
}

// receiver reports whether x, the base of the selector sel at stack index
// i, is the receiver of the call of sel: a Task's Wait or Stop, or a dot
// call.
func (w *taskUses) receiver(x syntax.Expr, sel *syntax.SelectorExpr, i int) bool {
	var fun syntax.Node = sel
	j := i - 1
	for j >= 0 {
		if _, ok := w.stack[j].(*syntax.ParenExpr); !ok {
			break
		}
		fun, j = w.stack[j], j-1
	}
	if j < 0 {
		return false
	}
	ce, ok := w.stack[j].(*syntax.CallExpr)
	if !ok || ce.Fun != fun {
		return false
	}
	call := w.info.Calls[ce]
	if call == nil {
		return true
	}
	if call.Recv == nil || unparen(call.Recv) != unparen(x) {
		return false
	}
	switch call.Kind {
	case CallOp:
		// Every operation of a Set is its owner's (Wait, Stop, Len).
		return isTask(w.info.Types[x]) && (sel.Sel.Name == "Wait" || sel.Sel.Name == "Stop" || isSet(w.info.Types[x]))
	case CallFunc:
		return true
	}
	return false
}
