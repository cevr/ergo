package eval

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
)

// preflight walks reachable checked bodies, all arms, once per obligation and
// binding. Only resolved law operations may remove opacity. An unrelated
// capability, unknown dispatch or missing footprint is never hidden by a binding.
type preflight struct {
	in   *interp
	seen map[syntax.Node]bool
}

func (p *preflight) function(fr *frame, fn *check.Func) string {
	if fn == nil {
		ice("no resolved callee")
	}
	if fn.Decl == nil || fn.Decl.Body == nil || fn.Decl.Extern != nil || fn.Pkg.Host() {
		if !fn.Pkg.Host() && nativeOf(fn) != nil {
			return ""
		}
		return fmt.Sprintf("a Go function (%s), even a pure one", fn.Name)
	}
	return p.body(p.in.newFrame(fn.Pkg, fn.Decl), fn.Decl, fn.Decl.Body, nil, nil)
}

func (p *preflight) body(fr *frame, key syntax.Node, body *syntax.BlockStmt, own *check.Conversion, ops map[*check.Var]check.Conversion) string {
	if p.seen[key] {
		return ""
	}
	p.seen[key] = true
	// A capability runs through the provider a with binds (effects.go);
	// a capability no with of the run binds is skipped at its call. A
	// task runs on the case's scheduler (sched.go).
	if _, ok := fr.info.Footprints[key]; !ok {
		ice("no footprint for the frame at %d", key.Pos())
	}
	why := ""
	syntax.Inspect(body, func(n syntax.Node) bool {
		if n == nil || why != "" {
			return false
		}
		mustCharge(p.in, fr, 1)
		// A machine's conversion, alone or a tuple's element, is a value
		// the run makes (machinevalue.go).
		switch x := n.(type) {
		case *syntax.FuncLit:
			return false // only a known immediately-called literal is reached
		case *syntax.RangeStmt:
			if _, ok := fr.rtypeOf(x.X).under().t.(*check.Map); ok {
				why = "Map iteration has unspecified order"
			}
		case *syntax.CallExpr:
			c := fr.info.Calls[x]
			if c == nil {
				ice("a call with no Calls entry")
			}
			if check.IsUnreachable(c) {
				return true
			}
			switch c.Kind {
			case check.CallLen, check.CallConversion:
			case check.CallFunc:
				if inst := fr.info.Instances[x]; inst != nil && len(inst.Witnesses) > 0 {
					why = "a generic call with unresolved operation witnesses"
					break
				}
				if c.Site != nil {
					why = "a call with a CallSite or trailing block"
					break
				}
				if x.Trailing != nil {
					// The block of a native the evaluator runs (task.Go,
					// scope.OnExit, tasks.Go) is a function of its own.
					if c.Func.Pkg.Host() || nativeOf(c.Func) == nil {
						why = "a call with a CallSite or trailing block"
						break
					}
					why = p.body(fr, x.Trailing, x.Trailing, own, ops)
					break
				}
				if !preludeCollection(c.Func) {
					why = p.function(fr, c.Func)
				}
			case check.CallFuncValue:
				if lit, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
					why = p.body(fr, lit, lit.Body, own, ops)
				}
				// Any other callee is a value, known only when the run
				// reaches the call (effects.go, callValue).
			case check.CallOp, check.CallLawOp:
				conv := own
				if c.Kind == check.CallOp {
					conv = nil
					if id, ok := unparen(c.Recv).(*syntax.Ident); ok {
						if v, ok := fr.info.Uses[id].(*check.Var); ok {
							if cv, ok := ops[v]; ok {
								conv = &cv
							}
						}
					}
				}
				if conv == nil {
					// An operation value's provider is known only when
					// the run reaches the call (effects.go dispatch).
					break
				}
				fn := conv.Table[c.Op.Name]
				if fn == nil {
					ice("no checked method for operation %s", c.Op.Name)
				}
				why = p.function(fr, fn)
			case check.CallProviderOp, check.CallWith:
				// The provider is the nearest with's, known at the run.
			default:
				why = fmt.Sprintf("an unsupported call (kind %d)", c.Kind)
			}
		}
		return why == ""
	})
	return why
}

func (p *preflight) generator(fr *frame, t check.Type, provider *check.LawProvider, seen map[check.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	mustCharge(p.in, fr, 1)
	if provider != nil && provider.Ctor != nil {
		if why := p.function(fr, provider.Ctor); why != "" {
			return why
		}
		for _, v := range provider.Ctor.Sig.Params {
			if why := p.generator(fr, v.Type, nil, seen); why != "" {
				return why
			}
		}
		return ""
	}
	var children []check.Type
	switch u := t.(type) {
	case *check.Basic:
		return ""
	case *check.Slice:
		children = []check.Type{u.Elem}
	case *check.Map:
		if basicOf(u.Key) == nil {
			return "a Map generator with a non-basic key"
		}
		children = []check.Type{u.Key, u.Elem}
	case *check.Tuple:
		children = u.Types
	case *check.Union:
		children = u.Variants
	case *check.Named:
		if ctor := testplan.Constructor(u); ctor != nil {
			return p.generator(fr, t, &check.LawProvider{Ctor: ctor}, map[check.Type]bool{})
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				children = append(children, f.Type)
			}
		case *check.Union:
			children = under.Variants
		case *check.Interface:
			return "an operation-type builder without an interpreter adapter"
		default:
			children = []check.Type{under}
		}
	default:
		return fmt.Sprintf("a generator for %s", t)
	}
	for _, child := range children {
		if why := p.generator(fr, child, nil, seen); why != "" {
			return why
		}
	}
	return ""
}
