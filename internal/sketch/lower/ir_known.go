package lower

// IR builder: copies for a known record (FLIP F7 wave 2, "copy for a
// known record"; record.go, OPT6b).
//
// The IR takes both halves of the emitter's rule:
//   - a call whose record argument the lowering knows (a machine value put
//     into Machine, or the record parameter of the copy being written)
//     goes to the callee's copy for that record (specAt), one instance
//     with no type arguments;
//   - in a copy, a call p.F(…) of a field the record names is a direct
//     call of that function, which the IR writes as any call of a declared
//     function (with its environment when it takes one, its error when it
//     can fail).
//
// The copy's body is the function's body at the copy's type arguments
// (typeCtx mono): a type parameter is spelled as its Go type argument and
// admitted as the checked type it stands for (bound). The emitter also
// inlines a small field function into the copy (inlineField); the IR, which
// does not inline, calls it.
//
// Prior art: GHC's SpecConstr; OCaml's flambda specializes on a known
// block argument; Gleam, Elixir and ReScript call through the closure.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irInstanceCopy reports a copy the IR does not build (LY-FIX): a copy for
// a known provider (provider.go), or for a known witness (witspec.go) when
// witnessCopyIR is "off". Each is one more Go function beside f's, named
// sp.name; an IR body built for it would take f's name and f's Go
// declaration, a second body for f at the instance's types (a461a6b13's
// "Fold is not declared in the Go source"). The emitter writes their
// bodies. A monomorphic copy (mono.go) is built: its body takes the copy's
// name (monoName), and the IR calls it (callParts; MONO-HOOKS-MIN). A copy
// for a known witness takes the IR (G4, ir_witness.go) under its own name
// (copyName).
func irInstanceCopy(sp *specCopy) bool {
	return sp != nil && sp.rec == nil && (sp.prov != nil || sp.wit != nil && witnessCopyIR == "off")
}

// copyName is the Go name of the copy for a known record or a known
// witness that the body of f being built is, or "" when it is f itself.
func (b *irBuilder) copyName(f *check.Func) string {
	for _, rec := range b.fc.recs {
		if sp := b.l.specs[specKey{f: f, tn: rec.tn}]; sp != nil {
			return sp.name
		}
	}
	for _, st := range b.fc.static {
		tn, ptr := witArg(st.Arg)
		if sp := b.l.specs[witKey(f, tn, ptr)]; sp != nil && tn != nil {
			return sp.name
		}
	}
	return ""
}

// copyCall is the Go name of the copy the call x of c goes to, when its
// record argument is known (record.go specAt, which queues the copy's
// body), else "".
func (b *irBuilder) copyCall(x *syntax.CallExpr, c *check.Call) string {
	if sp := b.fc.specAt(x, c); sp != nil && sp.rec != nil {
		// A copy for a known provider takes the provider's adapter,
		// which the IR does not build: the IR calls the function itself.
		return sp.name
	}
	return ""
}

// knownField is c, the call x, as a call of the declared function a field
// of a known record names, when x calls such a field in a copy
// (recordCallText's conditions); else c.
func (b *irBuilder) knownField(x *syntax.CallExpr, c *check.Call) *check.Call {
	if c.Kind != check.CallFuncValue || b.fc.recs == nil || recordLowering == "opaque" || c.StartsTask {
		return c
	}
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		return c
	}
	id, ok := unparen(sel.X).(*syntax.Ident)
	if !ok {
		return c
	}
	v, ok := b.l.info.Uses[id].(*check.Var)
	if !ok || b.fc.recs[v] == nil {
		return c
	}
	g := b.fc.recs[v].fields[sel.Sel.Name]
	if g == nil || len(g.Sig.TParams) > 0 || len(witnessParams(g.Sig)) > 0 || g.Pkg == nil || g.Pkg.Info == nil {
		return c
	}
	sig, ok := b.l.info.Types[x.Fun].(*check.Signature)
	if !ok || len(g.Sig.Params) != len(sig.Params) || len(g.Sig.Results) != len(sig.Results) {
		return c
	}
	gd := b.l.funcDecl(g)
	if gd.method != nil || gd.extern != "" || len(b.l.ownedParams(g)) > 0 {
		return c
	}
	if len(rowElems(sig.Errors)) > 0 && !gd.fallible {
		// A field whose row can fail naming a function that cannot (a
		// machine's Step under a runner's W): the IR calls the field's
		// value, whose (value, error) pair the try reads (LY-FIX: the
		// direct call gave a value where the try bound a pair).
		return c
	}
	return &check.Call{Kind: check.CallFunc, Func: g, Fails: c.Fails}
}
