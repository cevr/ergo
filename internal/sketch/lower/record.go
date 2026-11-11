package lower

import (
	"go/token"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Known records (OPT6b). A function value read from a record field is
// opaque at its call: the uniform func(*Env, …) (R, error) through the
// field, which Go cannot inline. When a call passes, for a record
// parameter of its callee, a record the lowering knows, the call goes to
// a copy of the callee for that record, where
//   - each call p.F(…) of a field whose value the record names is a
//     direct Go call of that function, with its own environment and error
//     shape (a pure function takes no environment, an infallible one
//     returns no error);
//   - a call that passes p on to another function goes to that function's
//     copy for the same record;
//   - the type parameters are the call's type arguments (a copy is one
//     instance), so the copy's Go has no type parameter and no dictionary.
//
// The record is still passed and read: a field the record does not name,
// and every other use of p, reads the value as before. So a copy differs
// from its function only in the calls it makes directly, and it is its
// own Go frame: its polls, failures and defects are the function's own.
//
// A record is known when it is a machine declaration's value put into the
// prelude Machine (ConvMachine, expr.go): its Step is the declaration's
// step function. A machine with an input (a Start) is not: its conversion
// builds the record from an input. A function has copies when it has an
// Ergo body, is no method, no extern, no Close and not main, has no
// witness, no owned parameter and no Task result, and its body calls a
// field of a record parameter or passes the parameter on.
//
// The copy's Go name is reserved in the naming pass, for every such
// function and every machine declaration of the program's own packages.
//
// The reference is recordLowering "opaque", set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.recordLowering=opaque:
// every call takes the function itself. TestMachineFlatRunAgrees runs
// generated machines under both.
//
// Prior art: GHC's SpecConstr and call-pattern specialization copy a
// function for a known constructor argument; MLton's defunctionalization
// makes every call of a known function direct; OCaml's flambda specializes
// a function on a known closure or block argument; Rust monomorphizes a
// generic over a closure type. Gleam, Elixir and ReScript pass a record of
// closures through the host's indirect call.
var recordLowering string

// knownRec is a record value the lowering knows at a call: the machine
// whose conversion built it, and the functions its fields name.
type knownRec struct {
	tn     *check.TypeName
	fields map[string]*check.Func
}

// specKey is one copy: a function and the record it is copied for.
type specKey struct {
	f  *check.Func
	tn *check.TypeName
}

// specCopy is the copy of f for rec: its Go name, the index of the record
// parameter and the Go types of f's type parameters.
type specCopy struct {
	f        *check.Func
	fd       *funcDecl
	name     string
	param    int
	rec      *knownRec
	typeArgs map[*check.TypeParam]string
	// bound is each type parameter's type argument, when the call names
	// one with no type parameter of its own (scalar.go reads it).
	bound map[*check.TypeParam]check.Type
}

// recordParam is the index of f's record parameter that its body calls a
// field of or passes on, or -1: the parameter a copy of f is made for.
func (l *lowerer) recordParam(f *check.Func) int {
	if k, ok := l.recParams[f]; ok {
		return k
	}
	if l.recParams == nil {
		l.recParams = map[*check.Func]int{}
	}
	k := l.findRecordParam(f)
	l.recParams[f] = k
	return k
}

func (l *lowerer) findRecordParam(f *check.Func) int {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" || isClose(f) {
		return -1
	}
	info := f.Pkg.Info
	if f.Sig.ResultIsTask() || info.TaskResults[f] || len(witnessParams(f.Sig)) > 0 {
		return -1
	}
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		return -1
	}
	isRecord := func(t check.Type) bool {
		n, ok := t.(*check.Named)
		if !ok {
			return false
		}
		_, ok = n.Underlying().(*check.Struct)
		return ok
	}
	index := map[*check.Var]int{}
	for i, p := range f.Sig.Params {
		if vars[i] != nil && isRecord(p.Type) {
			index[vars[i]] = i
		}
	}
	if len(index) == 0 {
		return -1
	}
	param := func(x syntax.Expr) int {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return -1
		}
		v, ok := info.Uses[id].(*check.Var)
		if !ok {
			return -1
		}
		if i, ok := index[v]; ok {
			return i
		}
		return -1
	}
	found := -1
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CallExpr)
		if !ok || found >= 0 {
			return found < 0
		}
		c := info.Calls[x]
		if c == nil {
			return true
		}
		switch c.Kind {
		case check.CallFuncValue:
			if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
				found = param(sel.X)
			}
		case check.CallFunc:
			for _, a := range x.Args {
				if i := param(a); i >= 0 {
					found = i
				}
			}
		}
		return found < 0
	})
	return found
}

// reserveSpecs gives, in the naming pass, the Go name of the copy of each
// function of pkgs that has one for each machine of the program's own
// packages.
func (l *lowerer) reserveSpecs(pkgs []*check.Package, machines []*check.TypeName) {
	if recordLowering == "opaque" || len(machines) == 0 {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if l.recordParam(f) < 0 {
				continue
			}
			for _, tn := range machines {
				l.pkgName(pkgKey{"spec", specKey{f, tn}}, f.Name+tn.Name)
			}
		}
	}
}

// sourcedMachines are the machine declarations of the program's own
// packages, in the naming pass's order.
func (l *lowerer) sourcedMachines() []*check.TypeName {
	var out []*check.TypeName
	for _, p := range l.sourcedPkgs() {
		for _, tn := range l.canon.types(p) {
			if tn.Decl == nil {
				continue
			}
			if _, ok := tn.Decl.Spec.(*syntax.MachineType); ok {
				out = append(out, tn)
			}
		}
	}
	return out
}

// knownRecord is the record x gives, when the lowering knows it: a
// machine declaration's value put into Machine, or a record parameter of
// the copy being written. Else nil.
func (fc *funcCtx) knownRecord(x syntax.Expr) *knownRec {
	if x == nil || recordLowering == "opaque" {
		return nil
	}
	if id, ok := unparen(x).(*syntax.Ident); ok && fc.recs != nil {
		if v, ok := fc.l.info.Uses[id].(*check.Var); ok && fc.recs[v] != nil {
			return fc.recs[v]
		}
	}
	conv, ok := fc.l.info.Implicit[x]
	if !ok || conv.Kind != check.ConvMachine || (conv.Table["Start"] != nil) {
		return nil
	}
	named, ok := conv.From.(*check.Named)
	if !ok {
		return nil
	}
	step := conv.Table["Step"]
	if step == nil || conv.Table["Start"] != nil {
		return nil
	}
	return &knownRec{tn: named.Obj, fields: map[string]*check.Func{"Step": step}}
}

// specAt is the copy the call x of c goes to, made and queued for emission
// at its first call; nil when x takes the function itself.
func (fc *funcCtx) specAt(x *syntax.CallExpr, c *check.Call) *specCopy {
	if recordLowering == "opaque" || c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil || c.Site != nil || x.Trailing != nil || c.TaskResult {
		return nil
	}
	f := c.Func
	k := fc.l.recordParam(f)
	if k < 0 || k >= len(x.Args) {
		return nil
	}
	rec := fc.knownRecord(x.Args[k])
	if rec == nil {
		return nil
	}
	fd := fc.l.funcDecl(f)
	if fd.method != nil || fd.extern != "" || len(fc.l.ownedParams(f)) > 0 {
		return nil
	}
	name, ok := fc.l.pkgNames[pkgKey{"spec", specKey{f, rec.tn}}]
	if !ok {
		return nil // the naming pass names the machines of the program's own packages
	}
	// The copy is one instance: each type argument is a Go type, never a
	// type parameter of a generic caller (a copy's are Go types).
	var own []*check.TypeParam
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			own = append(own, tp)
		}
	}
	args := map[*check.TypeParam]string{}
	bound := map[*check.TypeParam]check.Type{}
	j := 0
	for i, tp := range f.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(c.TypeArgs) {
			return nil
		}
		if mentionsTypeParam(c.TypeArgs[i]) && !fc.mono {
			return nil
		}
		args[tp] = fc.goType(c.TypeArgs[i])
		if t := fc.boundType(c.TypeArgs[i]); !mentionsTypeParam(t) {
			bound[tp] = t
		}
		j++
	}
	if j != len(own) {
		return nil
	}
	key := specKey{f, rec.tn}
	if sp := fc.l.specs[key]; sp != nil {
		return sp
	}
	if fc.l.specs == nil {
		fc.l.specs = map[specKey]*specCopy{}
	}
	sp := &specCopy{f: f, fd: fd, name: name, param: k, rec: rec, typeArgs: args, bound: bound}
	fc.l.specs[key] = sp
	fc.l.stats.Specialized++
	fc.l.pending = append(fc.l.pending, func() { fc.l.emitBody(fd, nil, false, sp) })
	return sp
}

// inlineField inlines x, a call of the field of a known record whose
// function is g, when the inline rule (inline.go) allows g with the
// budget inlineGrowth: a copy is written once for its record, so its
// field calls cost one body each, as the flat run of a machine lowered
// its step a second time. g takes no callback (its callbacks would be
// unknown here). The call's identity stays the field call's, so a defect
// report names the frame it names without inlining.
func (fc *funcCtx) inlineField(x *syntax.CallExpr, c *check.Call, g *check.Func, gd *funcDecl) bool {
	if fc.frame == nil || fc.inlDepth >= inlineDepth || len(callbackParams(g)) > 0 || !fc.l.inlineWithin(g, inlineGrowth) {
		return false
	}
	if len(g.Sig.Results) == 1 && fc.noValue(x) {
		return false // a result a block with no value binds: struct{}
	}
	if !gd.pure && fc.env == "" {
		return false
	}
	if fc.l.inlineGrown == nil {
		fc.l.inlineGrown = map[*namer]int{}
	}
	size := inlineSize(g.Decl.Body)
	if fc.l.inlineGrown[fc.scope]+size > fc.l.growthOf(fc.scope) {
		return false
	}
	fc.l.inlineGrown[fc.scope] += size
	if token.IsExported(g.Name) {
		fc.l.useFunc(g, x)
	}
	if fc.directives && x.Pos().IsValid() {
		fc.l.recordCall(x, fc.owner, fc.callee(x, c))
	}
	op := fc.inlineCall(x, &check.Call{Kind: check.CallFunc, Func: g, Fails: c.Fails}, gd, x.Args, nil, nil)
	fc.inlined = &op
	return true
}

// recordCallText is the Go text of x, a call p.F(…) of a field of a known
// record in a copy, as a direct call of the function the field names:
// with an environment when that function takes one, returning an error
// when it can fail. ok is false for any other call.
func (fc *funcCtx) recordCallText(x *syntax.CallExpr, c *check.Call, sig *check.Signature) (text string, hasValue, fallible, ok bool) {
	if fc.recs == nil || recordLowering == "opaque" {
		return "", false, false, false
	}
	sel, isSel := unparen(x.Fun).(*syntax.SelectorExpr)
	if !isSel {
		return "", false, false, false
	}
	id, isID := unparen(sel.X).(*syntax.Ident)
	if !isID {
		return "", false, false, false
	}
	v, isVar := fc.l.info.Uses[id].(*check.Var)
	if !isVar || fc.recs[v] == nil {
		return "", false, false, false
	}
	field := sel.Sel.Name
	g := fc.recs[v].fields[field]
	if g == nil || len(g.Sig.TParams) > 0 || len(witnessParams(g.Sig)) > 0 || g.Pkg == nil || g.Pkg.Info == nil || g.Pkg.Info.TaskResults[g] {
		return "", false, false, false
	}
	if len(g.Sig.Params) != len(sig.Params) || len(g.Sig.Results) != len(sig.Results) || sig.ResultIsTask() || c.TaskResult {
		return "", false, false, false
	}
	gd := fc.l.funcDecl(g)
	if gd.method != nil || gd.extern != "" || len(fc.l.ownedParams(g)) > 0 {
		return "", false, false, false
	}
	if fc.inlineField(x, c, g, gd) {
		return "", false, false, true // the value is fc.inlined
	}
	gd = fc.l.useFunc(g, x)
	vals := fc.callArgs(x.Args, x)
	if !gd.pure {
		vals = append([]string{fc.envExpr(x)}, vals...)
	}
	return gd.name + "(" + strings.Join(vals, ", ") + ")", len(sig.Results) > 0, gd.fallible, true
}
