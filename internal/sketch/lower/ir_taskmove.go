package lower

// IR builder: a task's literal that moves what it captures (TASK-CAPTURES
// tip 2; R12, R27, R30; the emitter's taskcapture.go). A literal a task
// runs may capture an owned local of its sender whole (check.TaskCapture's
// Vars). The sender's frame lists that value; the call that starts the
// task takes it over, in the call's own step:
//
//   - a literal run once (task.Go's block) is a frame of its own that owns
//     its captures, registered at its open as its taken parameters are
//     (closureLit; the emitter's funcLitClosure, ownsCaptures);
//   - a literal a Set may run again (tasks.Go's block, Repeated) only
//     lends them: a root callback, which the runtime runs once before any
//     run and whose scope it ends after the last, owns them
//     (taskCaptureRoot), passed as TasksGoOwned's prepare;
//   - at the call, a successful admission hands each capture's entry out of
//     the sender's list (v1.Hand), and a failed one releases each now, last
//     taken first (captureCall, settleTaken).
//
// A captured let mut, whose sender may write it after the call, and a
// sender whose capture is not in its frame's list keep the source body.
//
// Prior art: Rust's `move` closure passed to thread::spawn transfers its
// captures' ownership at the call, and the spawned thread drops them;
// Erlang copies a spawned fun's free variables into the new process, so
// the sender keeps nothing to release. Go's `go` statement captures by
// reference and leaves releasing to the programmer, which this lowering
// writes for it.

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// taskAdmits reports a task's literal the IR builds: its captures, lent
// or moved, are lets (R12, R27). The emitter's taskSnapshot rebinds each
// to a sender local of the same value; a let is never written, so the
// literal reading it is that snapshot. A let mut, which the snapshot
// fixes before later writes, keeps the emitter's.
func taskAdmits(c *check.TaskCapture) bool {
	for _, v := range c.Vars {
		if v.Mut() {
			return false
		}
	}
	for _, v := range c.Borrowed {
		if v.Mut() {
			return false
		}
	}
	return true
}

// movedCaptures are the owned locals the literals passed to the call x
// move (captureNodes), and whether one of them is a Set's repeated body.
func (b *irBuilder) movedCaptures(x *syntax.CallExpr) (vars []*check.Var, repeated bool) {
	for _, n := range b.fc.captureNodes(x) {
		c := b.l.info.TaskCaptures[n]
		for _, v := range c.Vars {
			if b.l.info.OwnedVars[v] != nil {
				vars = append(vars, v)
			}
		}
		if len(c.Vars) > 0 && c.Repeated {
			repeated = true
		}
	}
	return vars, repeated
}

// captureHandles are the sender's list handles of vars: each must be an
// entry of the frame's list form (listLet).
func (b *irBuilder) captureHandles(vars []*check.Var) []int {
	if len(vars) == 0 {
		return nil
	}
	o := b.own
	if o == nil || !o.list {
		irNo("function literal that a task captures") // the static form keeps the source body
	}
	hs := make([]int, len(vars))
	for i, v := range vars {
		h, ok := o.handle[v]
		if !ok {
			irNo("function literal that a task captures") // not registered on this path
		}
		hs[i] = h
	}
	return hs
}

// captureRoot is the prepare callback of TasksGoOwned for the Set member
// at n, whose repeated body lends vars: on the member's task, a plain
// owner scope that owns each of vars, its releases on the member's
// environment (the emitter's taskCaptureRoot, an envClosure of childEnv):
//
//	func(t *rt.Task) *rt.Scope {
//		e := *env; e.task = t
//		root := rt.NewOwnerScope(e.task)
//		own := root.Owner(); v1.Owning(own, v, release)  // each of vars
//		return &root
//	}
func (b *irBuilder) captureRoot(n syntax.Node, vars []*check.Var, p irPos) *irExpr {
	if b.env < 0 {
		irNo("function literal that a task captures")
	}
	tt := b.spelling("*" + b.l.taskPkg() + ".Task")
	st := b.spelling(b.l.rt() + ".Scope")
	t := b.temp("t", tt, p)
	cp := b.temp("e", b.envValue(), p)
	ep := b.temp("rootEnv", b.envPointer(), p)
	root := b.temp("root", st, p)
	end := p
	lit := &irExpr{K: "func", Pos: p, T: "func(" + tt + ") *" + st, Params: []int{t}, End: &end}
	lit.Body = append(lit.Body,
		&irStmt{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}},
		&irStmt{K: "store", Pos: p, X: &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: cp}}, Y: &irExpr{K: "local", Pos: p, I: t}},
		&irStmt{K: "def", Pos: p, Local: ep, X: b.addrOf(cp, p)},
	)
	b.keepImport("var _ = " + b.l.rt() + ".NewOwnerScope")
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: ep}}
	lit.Body = append(lit.Body,
		&irStmt{K: "var", Pos: p, Local: root},
		&irStmt{K: "set", Pos: p, Local: root, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "NewOwnerScope", Args: []*irExpr{task}}},
	)
	env, frame, with, own := b.env, b.frame, b.with, b.own
	b.env, b.frame, b.with = ep, root+1, 0
	b.own = &irOwn{list: true, kept: -1, live: -1, handle: map[*check.Var]int{}, bit: map[*check.Var]int{}, site: n, p: p, keys: map[syntax.Node]bool{}, pending: map[*check.Var]bool{}}
	for _, v := range vars {
		i, ok := b.locals[v]
		if !ok {
			irNo("function literal that a task captures")
		}
		lit.Body = append(lit.Body, b.ownLet(v, i, n, nil)...)
	}
	b.env, b.frame, b.with, b.own = env, frame, with, own
	lit.Body = append(lit.Body, &irStmt{K: "ret", Pos: p, X: b.addrOf(root, p)})
	return lit
}

// settleCaptures is the admission step of a call whose literals move the
// captures whose sender handles are hs: call bound, then each handed out
// of the sender's list on success, or on a failure (err, for a fallible
// call) each released now, last taken first. The result is the call's
// value (or its error, for a fallible call with no value).
func (b *irBuilder) settleCaptures(call *irExpr, hs []int, fallible, value bool, t string, p irPos) ([]*irStmt, *irExpr) {
	hand := func() []*irStmt {
		var out []*irStmt
		for _, h := range hs {
			out = append(out, &irStmt{K: "expr", Pos: p, X: b.ownCall("Hand", "struct{}", p, &irExpr{K: "local", Pos: p, I: h}, &irExpr{K: "zero", Pos: p, T: "struct{}"})})
		}
		return out
	}
	if fallible {
		if value {
			irNo("function literal that a task captures") // the emitter's "moving callback with a fallible result"
		}
		e := b.temp("err", "error", p)
		var release []*irStmt
		for _, h := range slices.Backward(hs) {
			release = append(release, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Release", X: &irExpr{K: "local", Pos: p, I: h}}})
		}
		return []*irStmt{
			{K: "def", Pos: p, Local: e, X: call},
			{K: "if", Pos: p, End: p, X: &irExpr{K: "bin", Pos: p, Op: "==", X: &irExpr{K: "local", Pos: p, I: e}, Y: &irExpr{K: "nil", Pos: p, T: "error"}}, Then: hand(), Else: release},
		}, &irExpr{K: "local", Pos: p, I: e}
	}
	if !value {
		irNo("function literal that a task captures")
	}
	v := b.temp("v", t, p)
	return append([]*irStmt{{K: "def", Pos: p, Local: v, X: call}}, hand()...), &irExpr{K: "local", Pos: p, I: v}
}
