package lower

// IR builder: copies for an instance (CONSTRAINTS C4; decision 5 of the
// CONSTRAINTS spec). See ir_call.go and mono.go.
//
// A call of a generic function whose type arguments are all known Go
// types goes to the callee's copy for them (mono.go monoAt, the same copy
// and name the emitter's call takes), and the copy's body is built by the
// IR. In the copy each type parameter is its Go type argument (typeCtx
// mono), so an operation call on a value of a type parameter whose type
// argument is a named type with a known implementing function is a direct
// call of that function: `by.Compare(a, b)` with by a ByPriority is
// `by.Compare(a, b)` on ByPriority's Go method, which gc inlines as it
// inlines a hand-written compare. Any other operation call in the copy
// calls the witness's field, as the generic body does: a witness of
// another copy's type parameter, a generic implementing function that
// takes witnesses of its own (cmp.Reverse's Compare), a function that can
// fail or whose receiver is a pointer.
//
// The copy keeps the emitter's head: the witness parameter stays and its
// callers pass the witness (MINIMAL FORK PATCH, IR-ONLY: the IR file
// carries bodies, the head is Go text). The copy's direct calls never
// read it, and gc removes the unread parameter's loads.
//
// Copies are keyed by the callee and the Go spelling of the type
// arguments (monoKey), so two providers of one element type are two
// copies, and the count is bounded by the distinct instances, never by
// the call sites. A test build takes no copy (shortcuts.go: monoTaken asks
// takes("mono-instance")), so its calls keep the generic function and its
// witness, as genericLowering "stencil" does in a run build: the
// reference of the copies. instanceOps "witness" keeps the copies and
// calls every operation through the witness: the reference of the direct
// call alone. TestInstanceCopiesAgree (internal/experimental/irspike) runs
// generated programs by the IR and by the source path, whose copies call
// through the witness, and under both references.
//
// Prior art: Rust and C++ monomorphize a generic per instance, so a trait
// or concept call is static and inlined; MLton specializes every
// polymorphic function the same way; GHC's specialization removes a
// dictionary argument at a known instance; Swift specializes within a
// module. Go's GC-shape stenciling keeps a dictionary call for a type
// parameter's method. Gleam, ReScript and Elixir pass a compare function
// by hand and call it through the host's closure.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// instanceOps, "witness", calls every operation in a copy for an instance
// through its witness: -ldflags=-X=ergo.local/prototype/internal/sketch/lower.instanceOps=witness.
var instanceOps string

// SetInstanceLowering selects, for the lowerings that follow, the copies
// for an instance with direct operation calls (direct), or the reference:
// copies whose operations go through the witness (instanceOps "witness")
// and, with stencil, no copies at all (genericLowering "stencil"). It
// returns what restores the settings before it: the references of
// TestInstanceCopiesAgree (internal/experimental/irspike).
func SetInstanceLowering(direct, stencil bool) (restore func()) {
	savedOps, savedGeneric := instanceOps, genericLowering
	instanceOps, genericLowering = "witness", ""
	if direct {
		instanceOps = ""
	}
	if stencil {
		genericLowering = "stencil"
	}
	return func() { instanceOps, genericLowering = savedOps, savedGeneric }
}

// instanceCopy is the Go name of the copy for an instance that the call x
// of c goes to (mono.go monoAt, which queues the copy's body), or "".
func (b *irBuilder) instanceCopy(x *syntax.CallExpr, c *check.Call) string {
	if c.Kind != check.CallFunc || c.Func == nil || len(c.TypeArgs) == 0 {
		return ""
	}
	if sp := b.fc.monoAt(x, c, c.Func, c.TypeArgs); sp != nil {
		return sp.name
	}
	return ""
}

// copyTakesNoWitness reports a call x of c that goes to a copy for its
// instance (instanceCopy, after the known variant and the copy for a known
// record, as call orders them) that takes no witness (specCopy.wits).
func (b *irBuilder) copyTakesNoWitness(x *syntax.CallExpr, c *check.Call, fd *funcDecl) bool {
	if fd.method != nil || len(witnessParams(c.Func.Sig)) == 0 || b.copyCall(x, c) != "" {
		return false
	}
	sp := b.fc.monoAt(x, c, c.Func, c.TypeArgs)
	return sp != nil && sp.wits != nil
}

// directOp is the operation call x of c on a value of the type parameter
// tp as a direct call of the function that implements the operation for
// tp's type argument in the copy being built, when the copy knows it
// (staticOpFunc); ok is false otherwise, and the call goes through the
// witness.
func (b *irBuilder) directOp(x *syntax.CallExpr, c *check.Call, tp *check.TypeParam, ptr bool, p irPos) (pre []*irStmt, e *irExpr, effect, ok bool) {
	if ptr || !b.fc.mono || c.Op == nil || len(rowElems(c.Op.Sig.Errors)) > 0 {
		return nil, nil, false, false
	}
	arg := b.fc.bound[tp]
	if arg == nil || mentionsTypeParam(arg) {
		return nil, nil, false, false
	}
	op := b.staticOpFunc(arg, c.Op.Name)
	if op == nil || instanceOps == "witness" {
		return nil, nil, false, false
	}
	f := op.Func
	fd := b.l.funcDecl(f)
	if fd.extern != "" || fd.ptr || fd.fallible || len(witnessParams(f.Sig)) > 0 || len(b.l.ownedParams(f)) > 0 || b.l.hasGoRow(f.Sig) {
		return nil, nil, false, false
	}
	name := fd.name
	if len(c.Op.Sig.TParams) > 0 {
		// A generic operation (GENERIC-OP): the provider's copy at the
		// receiver's type arguments and the call's own, closed here.
		own := make([]check.Type, len(c.TypeArgs))
		for i, t := range c.TypeArgs {
			own[i] = b.fc.boundType(t)
		}
		targs := check.ProviderTypeArgs(arg, f, own)
		if targs == nil {
			return nil, nil, false, false
		}
		sp := b.fc.monoAt(nil, nil, f, targs)
		if sp == nil {
			return nil, nil, false, false
		}
		pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
		if !fd.pure {
			vals = append([]*irExpr{{K: "local", Pos: p, I: b.env}}, vals...)
		}
		return pre, &irExpr{K: "call", Pos: p, Name: sp.name, Args: vals}, !fd.pure, true
	}
	if len(f.Sig.TParams) > 0 {
		// A generic implementing function (cmp.Natural's Compare): its
		// own copy for the instance, a plain function that takes the
		// receiver first (mono.go). Only a pure one: its head has no
		// environment to place.
		if !fd.pure {
			return nil, nil, false, false
		}
		sp := b.fc.monoAt(nil, nil, f, op.Inst.TypeArgs)
		if sp == nil {
			return nil, nil, false, false
		}
		name = sp.name
		fd = nil
	} else {
		b.l.useFunc(f, x)
	}
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	pure := fd == nil || fd.pure
	if !pure {
		env := &irExpr{K: "local", Pos: p, I: b.env}
		at := 0
		if fd.method != nil {
			at = 1
		}
		vals = append(vals[:at:at], append([]*irExpr{env}, vals[at:]...)...)
	}
	if fd != nil && fd.method != nil {
		return pre, &irExpr{K: "mcall", Pos: p, Name: name, X: vals[0], Args: vals[1:]}, !pure, true
	}
	return pre, &irExpr{K: "call", Pos: p, Name: name, Args: vals}, !pure, true
}

// staticOpFunc is the operation name of the closed type t as the checker
// bound it in some witness of the program (each instance's witnesses, and
// the witnesses of their implementing instances): the function that
// implements it for t, with its instance when it is generic. Nil when no
// witness of the program names t's operation by a function, or when the
// function is generic and takes witnesses of its own. A type has one
// implementing function per operation name (check methodOf), so any
// witness of t gives the same one.
func (b *irBuilder) staticOpFunc(t check.Type, name string) *check.WitnessOp {
	if _, ok := t.(*check.Named); !ok {
		return nil
	}
	l, st := b.l, b.st
	if st.opFuncs == nil {
		st.opFuncs = map[opFuncKey]*check.WitnessOp{}
		for _, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
			if p.Info == nil {
				continue
			}
			for _, inst := range p.Info.Instances {
				if inst == nil {
					continue
				}
				for i := range inst.Witnesses {
					w := &inst.Witnesses[i]
					if mentionsTypeParam(w.Arg) {
						continue
					}
					key := b.fc.goType(w.Arg)
					for j := range w.Ops {
						op := &w.Ops[j]
						if op.Func == nil || op.Inst != nil && (len(op.Inst.Witnesses) > 0 || mentionsTypeParamList(op.Inst.TypeArgs)) {
							continue
						}
						st.opFuncs[opFuncKey{key, op.Name}] = op
					}
				}
			}
		}
	}
	return st.opFuncs[opFuncKey{b.fc.goType(t), name}]
}

// opFuncKey is a closed type, by its Go spelling, and an operation name.
type opFuncKey struct {
	t, op string
}
