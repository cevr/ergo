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
		return fmt.Sprintf("a Go function (%s), even a pure one", fn.Name)
	}
	if fn.Ptr {
		return "a call that borrows its receiver"
	}
	return p.body(p.in.newFrame(fn.Pkg, fn.Decl), fn.Decl, fn.Decl.Body, nil, nil)
}

func (p *preflight) body(fr *frame, key syntax.Node, body *syntax.BlockStmt, own *check.Conversion, ops map[*check.Var]check.Conversion) string {
	if p.seen[key] {
		return ""
	}
	p.seen[key] = true
	fp, ok := fr.info.Footprints[key]
	if !ok {
		ice("no footprint for the frame at %d", key.Pos())
	}
	if len(fp.Caps) > 0 || fp.Tasks {
		return p.in.impure(fr.pkg, key)
	}
	why := ""
	syntax.Inspect(body, func(n syntax.Node) bool {
		if n == nil || why != "" {
			return false
		}
		mustCharge(p.in, fr, 1)
		if x, ok := n.(syntax.Expr); ok {
			if c := fr.info.Implicit[x]; c.Kind != 0 && c.Kind != check.ConvVariant && c.Kind != check.ConvRow {
				why = "an unsupported implicit operation or machine conversion"
				return false
			}
			for _, c := range fr.info.ImplicitElems[x] {
				if c.Kind != 0 && c.Kind != check.ConvVariant && c.Kind != check.ConvRow {
					why = "an unsupported tuple-element conversion"
					return false
				}
			}
		}
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
				if c.Site != nil || x.Trailing != nil {
					why = "a call with a CallSite or trailing block"
					break
				}
				if !preludeCollection(c.Func) {
					why = p.function(fr, c.Func)
				}
			case check.CallFuncValue:
				if lit, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
					why = p.body(fr, lit, lit.Body, own, ops)
				} else {
					why = "an unresolved function value"
				}
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
					why = "an unresolved operation"
					break
				}
				fn := conv.Table[c.Op.Name]
				if fn == nil {
					ice("no checked method for operation %s", c.Op.Name)
				}
				why = p.function(fr, fn)
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
