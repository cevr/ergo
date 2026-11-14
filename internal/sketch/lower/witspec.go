package lower

import (
	"cmp"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Known witnesses (VI1). A type parameter that an operation type
// constrains, [S Screen], takes its operations as a witness (witness.go):
// a struct of function values the caller builds, so each operation call
// in the generic body is an indirect call Go cannot inline, and each call
// that passes the witness on builds it again, field by field. When a call
// binds such a parameter to a named type whose operations are package
// functions (the witness is known at the call: `PlayOn(&s, input)` with s
// a *screen.Screen), the call goes to a copy of the callee for that type,
// one instance with no witness parameter. In the copy
//   - each operation call on a value of the parameter calls the
//     operation's function directly, through the closure the witness
//     would hold, called where it is written, which Go inlines;
//   - a call that binds a witness parameter of its callee to the copy's
//     own parameter goes to the callee's copy for the same type, so the
//     known witness reaches every function the generic code calls;
//   - any other use of the witness (a call with no copy, a conversion to
//     the operation type) takes the struct the call would have passed,
//     built in the copy.
//
// The copy differs from its function only in how it reaches the
// operations: its body is the function's, its polls, failures and
// defects its own. A function has copies when it has an Ergo body, is no
// method, no extern, no Close and not main, and its one type parameter is
// the one an operation type constrains. The copies' Go names are reserved
// in the naming pass, for every such function and every named type that
// some call of the program binds to a parameter of the same constraint.
//
// The reference is witnessLowering "dynamic", set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.witnessLowering=dynamic:
// every call passes the witness. TestKnownWitnessesAgree runs generated
// programs under both.
//
// Prior art: Rust and C++ monomorphize a generic for each type argument,
// so a trait or concept call is static; MLton specializes every
// polymorphic function the same way; GHC's SPECIALISE pragma and its
// automatic specialization of overloaded functions remove the dictionary
// argument at a known instance; Swift specializes a generic within its
// module and passes a witness table only across it. Go's own GC-shape
// stenciling keeps a dictionary for a type parameter's methods. Gleam,
// Elixir and ReScript have no constrained generics: a record of functions
// passed by hand stays a call through the host's closure.
var witnessLowering string

// witSpecParam is the type parameter f's copies are made for, or nil: f's
// only type-kinded type parameter, one an operation type constrains.
func (l *lowerer) witSpecParam(f *check.Func) *check.TypeParam {
	if witnessLowering == "dynamic" {
		return nil
	}
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" || isClose(f) {
		return nil
	}
	var own []*check.TypeParam
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			own = append(own, tp)
		}
	}
	ws := witnessParams(f.Sig)
	if len(own) != 1 || len(ws) != 1 || ws[0] != own[0] || isWireParam(ws[0]) {
		return nil
	}
	return ws[0]
}

// witArg is the named type a witness for arg is keyed by, and whether arg
// is a pointer to it: a named type of the program with no type arguments
// and no operation type (whose value carries its operations).
func witArg(arg check.Type) (*check.TypeName, bool) {
	ptr := false
	if p, ok := arg.(*check.Pointer); ok {
		arg, ptr = p.Elem, true
	}
	n, ok := arg.(*check.Named)
	if !ok || len(n.TArgs) > 0 || n.Obj.Pkg == nil || n.Obj.Pkg.Host() || isOps(n) {
		return nil, false
	}
	return n.Obj, ptr
}

// witKey is the key of f's copy for the witness arg.
func witKey(f *check.Func, tn *check.TypeName, ptr bool) specKey {
	if ptr {
		return specKey{f: f, tn: tn, prov: "wit*"}
	}
	return specKey{f: f, tn: tn, prov: "wit"}
}

// staticWitness reports a witness whose every operation is a package
// function: one the lowering can call directly.
func staticWitness(w *check.Witness) bool {
	if w == nil || len(w.Ops) == 0 {
		return false
	}
	if tn, _ := witArg(w.Arg); tn == nil {
		return false
	}
	for _, op := range w.Ops {
		if op.Func == nil || op.Sig == nil || op.Inst != nil && mentionsTypeParamList(op.Inst.TypeArgs) {
			return false
		}
	}
	return true
}

func mentionsTypeParamList(ts []check.Type) bool {
	for _, t := range ts {
		if mentionsTypeParam(t) {
			return true
		}
	}
	return false
}

// reserveWitSpecs gives, in the naming pass, the Go name of the copy of
// each function of pkgs that has one (witSpecParam) for each named type a
// call of pkgs binds, with a known witness, to a parameter of the same
// constraint.
func (l *lowerer) reserveWitSpecs(pkgs []*check.Package) {
	if witnessLowering == "dynamic" {
		return
	}
	type arg struct {
		tn  *check.TypeName
		ptr bool
	}
	args := map[*check.Interface]map[arg]bool{}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for _, inst := range p.Info.Instances {
			for i := range inst.Witnesses {
				w := &inst.Witnesses[i]
				if !staticWitness(w) {
					continue
				}
				it := constraintOf(w.Param)
				if it == nil {
					continue
				}
				tn, ptr := witArg(w.Arg)
				if args[it] == nil {
					args[it] = map[arg]bool{}
				}
				args[it][arg{tn, ptr}] = true
			}
		}
	}
	if len(args) == 0 {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			tp := l.witSpecParam(f)
			if tp == nil {
				continue
			}
			set := args[constraintOf(tp)]
			list := make([]arg, 0, len(set))
			for a := range set {
				list = append(list, a)
			}
			slices.SortFunc(list, func(a, b arg) int {
				if c := cmp.Compare(a.tn.Pkg.Path, b.tn.Pkg.Path); c != 0 {
					return c
				}
				if c := cmp.Compare(a.tn.Name, b.tn.Name); c != 0 {
					return c
				}
				return cmp.Compare(btoi(a.ptr), btoi(b.ptr))
			})
			for _, a := range list {
				l.pkgName(pkgKey{"spec", witKey(f, a.tn, a.ptr)}, f.Name+a.tn.Name)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// knownWitness is the witness the call x of f passes for f's copy
// parameter, when the lowering knows its operations: built at the call
// from a named type's functions, or the known witness of the copy being
// written for its own parameter. Else nil.
func (fc *funcCtx) knownWitness(x *syntax.CallExpr, f *check.Func) *check.Witness {
	tp := fc.l.witSpecParam(f)
	if tp == nil {
		return nil
	}
	inst := fc.l.info.Instances[x]
	if inst == nil || inst.Func != f {
		return nil
	}
	var w *check.Witness
	for i := range inst.Witnesses {
		if inst.Witnesses[i].Param == tp {
			w = &inst.Witnesses[i]
		}
	}
	if w == nil {
		return nil
	}
	if src, ptr := paramSource(w.Arg); src != nil {
		st := fc.static[src]
		if st == nil || ptr {
			return nil
		}
		ops := make([]check.WitnessOp, len(w.Ops))
		for i, op := range w.Ops {
			own := staticOp(st, op.Name)
			if own == nil {
				return nil
			}
			ops[i] = *own
		}
		return &check.Witness{Param: tp, Arg: st.Arg, Ops: ops}
	}
	if !staticWitness(w) {
		return nil
	}
	return w
}

// staticOp is the operation name of the known witness w, or nil.
func staticOp(w *check.Witness, name string) *check.WitnessOp {
	for i := range w.Ops {
		if w.Ops[i].Name == name {
			return &w.Ops[i]
		}
	}
	return nil
}
