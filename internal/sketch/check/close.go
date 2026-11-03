package check

import "ergo.local/prototype/internal/sketch/syntax"

// Close identifies the receiver's R1 release method. Exactly one of Func
// and Op is set: a written method or the operation resolved on Receiver.
// Lowering consumes this identity; spelling alone never admits a release.
type Close struct {
	Receiver Type
	Func     *Func
	Op       *Method
}

// closeFacts runs after row inference and resolution. It records identity
// once, for each declaration and each resolved use, including operation
// method values. It does not infer ownership or synthesize aggregate Close.
func (c *checker) closeFacts() {
	facts := c.pkg.Info.Closes
	for _, f := range c.funcs {
		if f.owner == nil || f.Name != "Close" || f.Sig == nil ||
			len(f.Sig.Params) != 1 || len(f.Sig.Results) != 0 {
			continue
		}
		recv := f.Sig.Params[0].Type
		if p, ok := recv.(*Pointer); ok {
			recv = p.Elem
		}
		if n, ok := recv.(*Named); !ok || ownTypeParam(n, f) != nil {
			continue
		}
		if !c.types.resolve(f.Sig.Errors).Empty() {
			c.cur = f.file
			c.errorf(f.Decl.Name, CodeType, "Close must have an empty failure row (resources R2); handle cleanup failures inside Close")
			continue
		}
		facts[f.Decl] = &Close{Receiver: f.Sig.Params[0].Type, Func: f}
	}
	for _, tn := range c.pkg.Types {
		it := asInterface(tn.Type)
		if it == nil {
			continue
		}
		m := it.method("Close")
		if !closeShape(m) || c.types.resolve(m.Sig.Errors).Empty() {
			continue
		}
		c.cur = tn.file
		c.errorf(tn.Decl.Name, CodeType, "Close must have an empty failure row (resources R2); handle cleanup failures inside Close")
	}
	funcFact := func(f *Func) *Close {
		if f == nil || f.Pkg == nil || f.Pkg.Info == nil {
			return nil
		}
		return f.Pkg.Info.Closes[f.Decl]
	}
	// A call's resolved callee wins over a same-spelled member in the
	// receiver's operation type. Inspect visits the call before its callee.
	selections := map[*syntax.SelectorExpr]*Close{}
	// Every file declaration, law bodies included: a use in a law records
	// its fact as a use anywhere does, owned by OwnerLaw.
	for _, fi := range c.files {
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			var fact *Close
			switch x := n.(type) {
			case *syntax.Ident:
				f, _ := c.pkg.Info.Uses[x].(*Func)
				fact = funcFact(f)
			case *syntax.SelectorExpr:
				if resolved, ok := selections[x]; ok {
					fact = resolved
				} else if f, ok := c.pkg.Info.Uses[x.Sel].(*Func); ok {
					fact = funcFact(f)
				} else {
					fact = c.closeOperation(c.pkg.Info.Types[x.X], nil, x.Sel.Name)
				}
			case *syntax.CallExpr:
				call := c.pkg.Info.Calls[x]
				if call == nil {
					break
				}
				switch call.Kind {
				case CallFunc:
					fact = funcFact(call.Func)
					if fact != nil {
						recv := call.Recv
						if recv == nil && len(x.Args) > 0 {
							recv = x.Args[0]
						}
						if t := c.pkg.Info.Types[recv]; t != nil {
							fact = &Close{Receiver: t, Func: fact.Func}
						}
					}
				case CallOp, CallProviderOp:
					if call.Op == nil {
						break
					}
					recv := call.Recv
					if call.Kind == CallProviderOp {
						if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
							recv = sel.X
						}
					}
					fact = c.closeOperation(c.pkg.Info.Types[recv], call.Op, call.Op.Name)
				case CallLawOp:
					// No fact: the call has no receiver, so it releases no
					// value, and no build lowers a law (196l).
				}
				fun := unparen(x.Fun)
				if ix, ok := fun.(*syntax.IndexExpr); ok {
					fun = unparen(ix.X)
				}
				if sel, ok := fun.(*syntax.SelectorExpr); ok {
					selections[sel] = fact
				}
			}
			if fact != nil {
				facts[n] = fact
			}
			return true
		})
	}
}

func closeShape(m *Method) bool {
	return m != nil && m.Sig != nil && len(m.Sig.Params) == 0 && len(m.Sig.Results) == 0 && len(m.Sig.TParams) == 0
}

func (c *checker) closeOperation(t Type, resolved *Method, name string) *Close {
	if t == nil || name != "Close" {
		return nil
	}
	recv := t
	if p, ok := t.(*TypeParam); ok {
		t = p.Constraint
	}
	it := asInterface(t)
	if it == nil {
		return nil
	}
	m := it.method("Close")
	if !closeShape(m) || (resolved != nil && resolved != m) || !c.types.resolve(m.Sig.Errors).Empty() {
		return nil
	}
	return &Close{Receiver: recv, Op: m}
}
