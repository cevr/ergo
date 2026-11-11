package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The durable rule (MU9, survey MQ5; runtime.md, Machines and actors 17):
// a machine given to a durable runner, std actor.StartDurable,
// actor.SuperviseDurable (MU7) or
// journal.Start, has a pure step. A recovery steps the saved signals
// again, so a step with an effect would act twice; a pure step replays to
// the same state by construction. The checker must see which function is
// the step, so the machine argument is a declaration's value `X{}`,
// machines.Start(X{}, input), or a prelude Machine literal whose Step
// names a package function (a generic one instantiated, step[S, E], too);
// anything else is S108 too. The step
// is pure for a replay when its footprint is replayable (rows.md,
// Footprints 2 and 3): no capability, no task start and no opaque call;
// it may make hosts with actor.NewHost (MakesHost, wf10 DH1). The diagnostic names the
// first call in the step's body that makes it impure. Work and timers stay
// allowed: their results reach the step as signals, which the save keeps.
// A call in std is not checked: std forwards a machine its caller gave,
// and the caller's call is checked.

// TakesDurableMachine reports a std durable runner: actor.StartDurable,
// actor.SuperviseDurable or
// journal.Start.
func TakesDurableMachine(f *Func) bool {
	if f == nil || f.Pkg == nil || !f.Pkg.std {
		return false
	}
	return (f.Pkg.Path == childPath && (f.Name == "StartDurable" || f.Name == "SuperviseDurable")) || (f.Pkg.Path == "journal" && f.Name == "Start")
}

// MakesHost reports std actor.NewHost, the one host function a durable
// step may call (wf10 DH1; runtime.md, Machines and actors 17): it makes an
// empty cell and acts on nothing outside. A host is entry data, not durable
// data: one made in a replay is unbound until the recovered actor's first
// entry binds it, so a replay that makes it again acts twice on nothing.
// Every other host function stays S108.
func MakesHost(f *Func) bool {
	return f != nil && f.Pkg != nil && f.Pkg.std && f.Pkg.Path == childPath && f.Name == "NewHost" &&
		f.Decl != nil && f.Decl.Extern != nil
}

// durableCall is a call of a durable runner, with the runner's name.
type durableCall struct {
	x    *syntax.CallExpr
	name string
	file *fileInfo
}

// recordDurable keeps a call of a durable runner for checkDurableSteps,
// which runs once the footprints are known.
func (c *checker) recordDurable(x *syntax.CallExpr, f *Func) {
	if c.pkg.std || len(x.Args) == 0 {
		return
	}
	c.durableCalls = append(c.durableCalls, durableCall{x: x, name: f.Pkg.Path + "." + f.Name, file: c.cur})
}

// checkDurableSteps reports each durable runner's machine whose step is
// not pure, or that is no machine declaration's value.
func (c *checker) checkDurableSteps() {
	for _, d := range c.durableCalls {
		c.cur = d.file
		arg := d.x.Args[0]
		step := c.durableStep(arg)
		if step == nil {
			c.errorf(arg, CodeType, "%s replays the saved signals with the machine's step, so the checker must see that the step is pure: pass a machine declaration's value X{}, machines.Start(X{}, input), or a Machine literal whose Step names a function (runtime.md, Machines and actors 17)", d.name)
			continue
		}
		info := step.Pkg.Info
		if info == nil {
			continue
		}
		fp, ok := info.Footprints[step.Decl]
		if !ok || fp.Replayable() {
			continue
		}
		c.errorf(arg, CodeType, "%s replays the saved signals with the machine's step, so the step must be pure: %s (runtime.md, Machines and actors 17)", d.name, impureCall(step, fp))
	}
}

// durableStep is the step function of the machine declaration whose value
// arg is (X{} or machines.Start(X{}, input)), or nil.
func (c *checker) durableStep(arg syntax.Expr) *Func {
	arg = unparen(arg)
	if call, ok := arg.(*syntax.CallExpr); ok {
		k := c.pkg.Info.Calls[call]
		if k == nil || k.Kind != CallFunc || k.Func == nil || k.Func.Pkg == nil || !k.Func.Pkg.std ||
			k.Func.Pkg.Path != "machines" || k.Func.Name != "Start" || len(call.Args) == 0 {
			return nil
		}
		arg = unparen(call.Args[0])
	}
	lit, ok := arg.(*syntax.CompositeLit)
	if !ok {
		return nil
	}
	n, ok := c.pkg.Info.Types[arg].(*Named)
	if !ok {
		return nil
	}
	if m := preludeType("Machine"); m != nil && n.origin() == m {
		// A prelude Machine literal: its Step names a package function.
		for _, e := range lit.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*syntax.Ident); !ok || k.Name != "Step" {
				continue
			}
			// The step is a function's name, or a generic one's name
			// instantiated (step[S, E, O, F, R]): either way the
			// checker sees its body.
			v := unparen(kv.Value)
			if ix, ok := v.(*syntax.IndexExpr); ok {
				v = unparen(ix.X)
			}
			id, ok := v.(*syntax.Ident)
			if !ok {
				return nil
			}
			f, _ := c.pkg.Info.Uses[id].(*Func)
			if f == nil || f.Decl == nil || f.Decl.Body == nil {
				return nil
			}
			return f
		}
		return nil
	}
	if n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil
	}
	if _, ok := n.under.(*MachineType); !ok {
		return nil
	}
	spec, ok := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok {
		return nil
	}
	id, ok := spec.Step.(*syntax.Ident)
	if !ok || n.Obj.Pkg.Scope == nil {
		return nil
	}
	f, _ := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	if f == nil || f.Decl == nil || f.Decl.Body == nil {
		return nil
	}
	return f
}

// impureCall names the first call in step's own frame that makes its
// footprint fp not empty, with its position; when none of its own calls
// does (a literal it calls in place does), it names what fp holds.
func impureCall(step *Func, fp Footprint) string {
	info := step.Pkg.Info
	var found syntax.Node
	var why string
	inspectFrame(info, step.Decl.Body, func(n syntax.Node, _ []*syntax.CallExpr) bool {
		if found != nil {
			return false
		}
		switch n := n.(type) {
		case *syntax.RangeStmt:
			if l := info.Loops[n]; l == nil || l.Domain == LoopSeq {
				found, why = n, "a for over a Seq, which calls unknown code"
			}
		case *syntax.CallExpr:
			if w := impureWhy(info, n); w != "" {
				found, why = n, w
			}
		}
		return found == nil
	})
	if found != nil && step.file != nil {
		line, col := step.file.syntax.Position(found.Pos())
		return fmt.Sprintf("step %s calls %s at %d:%d", step.Name, why, line, col)
	}
	switch {
	case len(fp.Caps) > 0:
		return fmt.Sprintf("step %s uses the capability %s", step.Name, fp.Caps[0])
	case fp.Tasks:
		return fmt.Sprintf("step %s starts a task", step.Name)
	default:
		return fmt.Sprintf("step %s calls code whose footprint is unknown", step.Name)
	}
}

// impureWhy is the reason the call x adds to its frame's footprint, or ""
// when it adds nothing (footprinter.call is the rule it follows).
func impureWhy(info *Info, x *syntax.CallExpr) string {
	k := info.Calls[x]
	if k == nil {
		return "a call whose footprint is unknown"
	}
	switch k.Kind {
	case CallProviderOp:
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			switch r := sel.X.(type) {
			case *syntax.Ident:
				return "the capability operation " + r.Name + "." + sel.Sel.Name
			case *syntax.SelectorExpr:
				if id, ok := r.X.(*syntax.Ident); ok {
					return "the capability operation " + id.Name + "." + r.Sel.Name + "." + sel.Sel.Name
				}
			}
			return "the capability operation " + sel.Sel.Name
		}
		return "a capability operation"
	case CallFunc:
		f := k.Func
		switch {
		case f == nil || f.Decl == nil:
			return "a function whose footprint is unknown"
		case pureStd(f):
			if k.StartsTask {
				return f.Name + ", which starts a task"
			}
			return ""
		case MakesHost(f):
			return ""
		case f.Decl.Extern != nil || f.Decl.Body == nil:
			return "the host function " + f.Name
		}
		if f.Pkg != nil && f.Pkg.Info != nil {
			if g, ok := f.Pkg.Info.Footprints[f.Decl]; ok && g.Replayable() {
				return ""
			}
		}
		return f.Name + ", which is not pure"
	case CallFuncValue:
		if _, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
			return ""
		}
		return "a function value"
	case CallOp, CallLawOp:
		return "an operation of a value"
	case CallWith:
		return "a with, which runs its providers"
	}
	return ""
}
