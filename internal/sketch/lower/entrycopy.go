package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Entry copies (MACHINE-EXPR, the speed rule). A computed entry is a call
// of a builder whose body is a function literal, such as a product
// (step: Both(left, right)) or a partial application (step: by(3, step)).
// Its value is a closure over the builder's parameters, so every step
// would be an indirect call of the closure and, inside it, an indirect
// call of each function the builder was given: slower than the hand fold
// that calls the steps directly.
//
// When the call is known, the entry has a copy: a Go function of the
// uniform shape (typeCtx.funcType), named ergoEntry<Field><X>, whose body
// is the builder's literal lowered once with the builder's parameters
// bound to the call's arguments:
//   - a function argument that names a pure package function
//     (pureFuncArg: no environment, no capability, no failure) is called
//     directly, as a known variant calls its callback (known.go);
//   - a constant argument is written where the parameter is read;
//   - the type parameters are the call's type arguments, Go types.
//
// The entry's variable is set to the copy (a pure builder that makes a
// literal computes nothing else, so the closure and the copy run the same
// code), the plan's Step calls the copy directly, and a copy of a std
// function for the machine's record (record.go, knownRec) calls it
// directly too, where Go can inline it. The copy drops its entry poll when
// its literal calls nothing but the bound functions and code with an empty
// footprint: nothing it runs can re-enter it (known.go's rule).
//
// A call is known when its callee is a package function of an Ergo body
// that is one function literal, with no witness, no receiver and no
// trailing block, whose arguments are each such a function or a constant,
// and whose type arguments name no type parameter (a machine with type
// parameters converts through its generic conversion function,
// machineentry.go). Every other computed entry keeps its variable.
//
// The reference is entryLowering "dynamic", set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.entryLowering=dynamic:
// every computed entry is its variable's closure. TestMachineRunAgreesWithFold
// and TestMachineEntryFormsAgree run under the lowering a build selects.
//
// Prior art: GHC specializes a function on a known function argument and
// SpecConstr copies it for a known closure; MLton defunctionalizes, so
// every call of a known lambda is direct; OCaml's flambda specializes a
// closure applied to known arguments; Rust monomorphizes `impl Fn`
// combinators per closure type, which is what makes iterator adaptors as
// fast as loops. Gleam, Elixir and ReScript build the closure and call it
// through the host.
var entryLowering string

// entryCopy is a computed entry's copy: its Go name, and whether it can
// drop its entry poll.
type entryCopy struct {
	name string
	// x is the builder call, c its check facts, f the builder and lit its
	// literal.
	x   *syntax.CallExpr
	c   *check.Call
	f   *check.Func
	lit *syntax.FuncLit
	// funs and consts bind the builder's parameters: a pure function to
	// call directly, a constant's Go text.
	funs   map[*check.Var]*check.Func
	consts map[*check.Var]string
	// targs are the Go types of the builder's type parameters.
	targs   map[*check.TypeParam]string
	written bool
}

// entryCopyKey is the package-scope key of the copy of the computed entry
// field of the machine tn.
func entryCopyKey(tn *check.TypeName, field string) pkgKey {
	return pkgKey{"machine entry copy " + field, tn}
}

// entryCopyOf is the copy of the computed entry e, the field of the
// machine tn with no type parameters, or nil when the call is not known
// (above). The decision is made once per machine and field: each
// conversion records entries of its own (check.Conversion.Entries).
func (l *lowerer) entryCopyOf(tn *check.TypeName, field string, e *check.MachineEntry) *entryCopy {
	if entryLowering == "dynamic" || e == nil || !e.Computed() {
		return nil
	}
	key := entryCopyKey(tn, field)
	if cp, ok := l.entryCopies[key]; ok {
		return cp
	}
	if l.entryCopies == nil {
		l.entryCopies = map[pkgKey]*entryCopy{}
	}
	cp := l.knownEntryCall(tn, e)
	if cp != nil {
		cp.name = l.pkgName(entryCopyKey(tn, field), "ergoEntry"+field+tn.Name)
	}
	l.entryCopies[key] = cp
	return cp
}

// knownEntryCall decides entryCopyOf: the copy of e's call when it is
// known, nil else.
func (l *lowerer) knownEntryCall(tn *check.TypeName, e *check.MachineEntry) *entryCopy {
	info := tn.Pkg.Info
	x, ok := unparen(e.Expr).(*syntax.CallExpr)
	if !ok || x.Trailing != nil {
		return nil
	}
	c := info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil || c.Site != nil || c.StartsTask {
		return nil
	}
	f := c.Func
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Pkg == nil || f.Pkg.Info == nil || len(witnessParams(f.Sig)) > 0 {
		return nil
	}
	lit := builderLit(d.Body)
	if lit == nil || f.Pkg.Info.Literals[lit] == nil {
		return nil
	}
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) || len(x.Args) != len(vars) {
		return nil
	}
	cp := &entryCopy{x: x, c: c, f: f, lit: lit, funs: map[*check.Var]*check.Func{}, consts: map[*check.Var]string{}, targs: map[*check.TypeParam]string{}}
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: x}
	for i, tp := range f.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(c.TypeArgs) || mentionsTypeParam(c.TypeArgs[i]) {
			return nil
		}
		cp.targs[tp] = tc.goType(c.TypeArgs[i])
	}
	// The arguments are read with the machine's package's facts.
	saved := l.info
	l.info = info
	defer func() { l.info = saved }()
	fc := newFuncCtx(l, newNamer(l.names), false)
	for i, v := range vars {
		if v == nil || v.Mut() {
			return nil
		}
		arg := x.Args[i]
		if _, isFn := f.Sig.Params[i].Type.(*check.Signature); isFn {
			g := fc.pureFuncArg(arg)
			if g == nil {
				return nil
			}
			cp.funs[v] = g
			continue
		}
		if !fc.isConst(arg) {
			return nil
		}
		cp.consts[v] = fc.assigned(arg)
		if fc.out.Len() > 0 {
			return nil // a constant that needs a statement
		}
	}
	return cp
}

// builderLit is the function literal a builder's body is, alone (as its
// tail, or returned), or nil.
func builderLit(b *syntax.BlockStmt) *syntax.FuncLit {
	if len(b.List) != 1 {
		return nil
	}
	var x syntax.Expr
	switch s := b.List[0].(type) {
	case *syntax.ExprStmt:
		x = s.X
	case *syntax.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		x = s.Results[0]
	default:
		return nil
	}
	lit, _ := unparen(x).(*syntax.FuncLit)
	return lit
}

// entryCopyName gives the copy cp's name, its body queued to be written
// once (as a record copy's is, record.go): a caller may be lowering a body
// of its own.
func (l *lowerer) entryCopyName(cp *entryCopy) string {
	if !cp.written {
		cp.written = true
		l.pending = append(l.pending, func() { l.writeEntryCopy(cp) })
	}
	return cp.name
}

// writeEntryCopy writes the copy cp (above).
func (l *lowerer) writeEntryCopy(cp *entryCopy) {
	f := cp.f
	info := f.Pkg.Info
	saved := l.info
	l.info = info
	defer func() { l.info = saved }()
	sig := info.Literals[cp.lit]
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, l.isSourced(f.Pkg))
	fc.site = cp.lit
	fc.owner = f.Name
	for tp, t := range cp.targs {
		fc.params[tp] = t
	}
	fc.plain = map[*check.Var]bool{}
	for v, g := range cp.funs {
		fc.locals[v] = l.useFunc(g, cp.x).name
		fc.plain[v] = true
	}
	for v, text := range cp.consts {
		fc.locals[v] = text
	}
	l.readsOf(f)
	l.analyzeValues(f.Decl, info)
	closed := l.entryCopyClosed(info, cp)
	text := fc.envClosure(callerEnv, "env", cp.lit, func(env string) (parts []string, results, body string) {
		inner := fc.innerFuncCtx()
		inner.plain = fc.plain
		inner.env = env
		inner.frame = newFrame(nil, true, framePolls(info, cp.lit))
		var params []*check.Var
		if ft := cp.lit.Type; ft != nil && ft.Params != nil {
			for _, field := range ft.Params.List {
				for _, id := range field.Names {
					v, _ := info.Defs[id].(*check.Var)
					params = append(params, v)
				}
			}
		}
		if len(params) != len(sig.Params) {
			ice(cp.lit, "a literal declares %d parameters and its signature has %d", len(params), len(sig.Params))
		}
		for i, p := range sig.Params {
			name := scope.alloc("_p")
			if params[i] != nil {
				name = inner.declare(params[i])
			}
			parts = append(parts, name+" "+fc.goType(p.Type))
		}
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		inner.frame.result = result
		inner.frame.unit = result == nil
		inner.depth = 1
		results = fc.valueResults(result)
		if l.bodyNeedsFrame(cp.lit.Body.List) || l.ownsParams(params) {
			errName := scope.alloc("err")
			named := result
			if named == nil {
				named = check.NoValue
			}
			results = fc.namedResults(named, errName)
			inner.site = cp.lit
			inner.frame.plain = !l.bodyNeedsScope(cp.lit.Body.List)
			inner.openFrame(errName)
			for _, v := range params {
				inner.ownVar(v, cp.lit)
			}
		}
		if !closed {
			inner.entryPoll(info, cp.lit)
		}
		inner.body(cp.lit.Body.List)
		return parts, results, inner.out.String()
	})
	if !strings.HasPrefix(text, "func(") {
		ice(cp.lit, "an entry copy that is not a function literal")
	}
	l.gen.WriteString("func " + cp.name + strings.TrimPrefix(text, "func") + "\n\n")
}

// entryCopyClosed reports whether the copy's literal calls only its bound
// functions and code with an empty footprint (knownBodyCall, as a known
// variant's body), makes no literal, ranges over no Seq and reads no
// context value: then nothing it runs can re-enter it, and its entry poll
// is dropped.
func (l *lowerer) entryCopyClosed(info *check.Info, cp *entryCopy) bool {
	fp, ok := info.Footprints[cp.lit]
	if !ok || len(fp.Caps) > 0 || fp.Tasks {
		return false
	}
	plain := map[*check.Var]bool{}
	for v := range cp.funs {
		plain[v] = true
	}
	allowed := map[*syntax.Ident]bool{}
	closed := true
	syntax.Inspect(cp.lit.Body, func(n syntax.Node) bool {
		if !closed {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			closed = false
		case *syntax.RangeStmt:
			if lp := info.Loops[n]; lp == nil || lp.Domain == check.LoopSeq {
				closed = false
			}
		case *syntax.CallExpr:
			closed = l.knownBodyCall(info, n, plain, allowed, false)
		}
		if x, isExpr := n.(syntax.Expr); isExpr && closed && info.ContextValues[x] != nil {
			closed = false
		}
		return closed
	})
	return closed
}
