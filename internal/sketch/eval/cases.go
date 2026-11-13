package eval

import (
	"go/constant"
	gotoken "go/token"
	"math/big"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/runtime/testgen"
)

type lawTarget struct {
	name, why string
	binding   []check.LawProvider
	ctx       *check.LawContext // a run under a named context (lawcontext.go)
}

func lawTargets(pkg *check.Package, run check.LawRun) []lawTarget {
	var out []lawTarget
	for _, b := range run.Bindings {
		out = append(out, lawTarget{name: bindingName(pkg, run.Law, b), binding: b})
	}
	for i := range run.Contexts {
		c := &run.Contexts[i]
		out = append(out, lawTarget{name: qualifiedLaw(pkg, run.Law) + " under " + c.Context.Name, why: c.Unrun, ctx: c})
	}
	if len(out) == 0 {
		out = append(out, lawTarget{name: qualifiedLaw(pkg, run.Law), why: "no binding of the law runs in this package"})
	}
	return out
}

func qualifiedLaw(pkg *check.Package, law *check.Law) string {
	n := lawName(law)
	if law.Pkg != pkg {
		n = "law " + law.Pkg.Name + "." + strings.TrimPrefix(n, "law ")
	}
	return n
}
func bindingName(pkg *check.Package, law *check.Law, b []check.LawProvider) string {
	n := qualifiedLaw(pkg, law)
	var with []string
	for k, slot := range law.Slots {
		p := b[k].Type.Obj.Name
		if b[k].Type.Obj.Pkg != pkg {
			p = b[k].Type.Obj.Pkg.Name + "." + p
		}
		if slot.Param < 0 {
			n += " for " + p
		} else {
			with = append(with, law.Params[slot.Param].Name+" = "+p)
		}
	}
	if len(with) > 0 {
		n += " with " + strings.Join(with, ", ")
	}
	return n
}

// operation is a checked provider conversion, never a live Go function value.
// env is the substitution of the frame that made the conversion, which
// its instances' type arguments are read in.
type operation struct {
	value Value
	conv  check.Conversion
	env   *tenv
}

// operationCall runs an operation of an operation-type value (CallOp) or
// of the law's own provider (CallLawOp).
func (in *interp) operationCall(fr *frame, x *syntax.CallExpr, c *check.Call) (Value, *abrupt) {
	var recv Value
	if fr.lawOwn != nil {
		recv = fr.lawOwn
	}
	if c.Kind == check.CallOp {
		v, ab := in.expr(fr, c.Recv)
		if ab != nil {
			return nil, ab
		}
		recv = v
	}
	if recv == nil {
		return nil, fr.unsupported(x, "an unresolved operation")
	}
	if c.Kind == check.CallLawOp && fr.info.Closes[x] == nil {
		// The provider under test is the law's capability: its call is
		// an interruption point (runtime.md, I1 2).
		if ab := in.poll(fr, x); ab != nil {
			return nil, ab
		}
	}
	// A std operation that takes an owned local (queue Push) holds its
	// entry until it returns (own.go, holdTaken).
	var held []*syntax.Ident
	if c.Kind == check.CallOp && c.Op != nil && c.Op.Sig != nil && len(c.Op.Sig.Takes) > 0 {
		if _, native := recv.(opObject); native {
			held = in.holdTaken(fr, x.Args, c.Op.Sig.Takes)
		}
	}
	args := make([]Value, 0, len(x.Args))
	for _, a := range x.Args {
		v, ab := in.expr(fr, a)
		if ab != nil {
			for _, id := range held {
				delete(fr.held, id) // the call never ran: the frame keeps them
			}
			return nil, ab
		}
		args = append(args, v)
	}
	in.handParts(fr, x.Args...)
	v, ab := in.dispatch(fr, x, recv, c.Op.Name, args)
	if len(held) > 0 {
		ab = in.settleHeld(fr, held, ab)
	}
	return v, ab
}

func (in *interp) testCases(pkg *check.Package, t *check.Test, res Result) Result {
	fr := in.newFrame(pkg, t.Decl)
	p := testplan.Test(t)
	return in.cases(fr, t.Decl.Body, false, t.Params, p, res)
}

func (in *interp) runLawTarget(law *check.Law, tg lawTarget, res Result) (out Result) {
	defer func() {
		if out.Status == Refuted {
			site := in.site(law.Pkg, law.Decl, law.Decl.Pos())
			out.Declaration = &site
		}
	}()
	why := tg.why
	if law.Unrun != "" {
		why = law.Unrun
	}
	if why != "" {
		res.Status, res.Reason = Skipped, why
		return res
	}
	if law.Machine != nil && law.Decl.Reach != nil {
		return in.machineLaw(law, res)
	}
	if tg.ctx != nil {
		return in.contextLaw(law, tg.ctx, res)
	}
	fr := in.newFrame(law.Pkg, law.Decl)
	if own := testplan.GenericOwner(law); own != nil {
		fr.env = &tenv{}
		for i, tp := range own.TParams {
			fr.env.bind(tp, rtype{t: tg.binding[0].Slot.TArgs[i]})
		}
	}
	return in.cases(fr, law.Decl.Body, true, law.Params, testplan.Law(law, tg.binding), res)
}

// cases runs one plan. A panic of *abrupt is private to value construction:
// it lets a metered stream stop before allocating or drawing further values.
func (in *interp) cases(fr *frame, body *syntax.BlockStmt, law bool, params []*check.Var, p testplan.Plan, res Result) (out Result) {
	start := in.steps
	out = res
	out.TotalCases = p.Count
	defer func() {
		if r := recover(); r != nil {
			ab, ok := r.(*abrupt)
			if !ok {
				panic(r)
			}
			out.Status, out.Reason, out.Site = statusOf(ab), ab.msg, in.site(ab.pkg, ab.decl, ab.at)
		}
		out.Steps = in.steps - start
		if out.Status != Passed {
			out.Evidence = ""
		}
	}()
	if p.Reason != "" {
		out.Status, out.Reason = Skipped, p.Reason
		return
	}
	opParams := map[*check.Var]check.Conversion{}
	var own *check.Conversion
	for _, input := range p.Inputs {
		if input.Provider != nil {
			if input.Param < 0 {
				c := input.Provider.Conv
				own = &c
			} else {
				opParams[params[input.Param]] = input.Provider.Conv
			}
		}
	}
	pre := preflight{in: in, seen: map[syntax.Node]bool{}}
	if why := pre.body(fr, fr.decl, body, own, opParams); why != "" {
		out.Status, out.Reason = Skipped, why
		return
	}
	for _, input := range p.Inputs {
		if why := pre.generator(fr, input.Type, input.Provider, map[check.Type]bool{}); why != "" {
			out.Status, out.Reason = Skipped, why
			return
		}
	}
	out.Evidence, out.Seed = "tested", in.budget.Seed
	if p.Listed {
		out.Evidence = "proved"
	}
	if !law && len(params) == 0 {
		out.Evidence = "evaluated"
	}
	r := testgen.New(in.budget.Seed, res.Name)
	r.Charge = func(n int64) { mustCharge(in, fr, n) }
	for k := 0; k < p.Count; k++ {
		out.Case = k + 1
		mustCharge(in, fr, 1)
		r.Start(k + 1)
		cf := in.newFrame(fr.pkg, fr.decl)
		cf.env = fr.env
		var listed []testplan.Value
		if p.Listed {
			listed = p.At(k)
		}
		// A case's arguments are written only when the case is reported:
		// a passing case's text was work no one read (ORACLE O31). The
		// values are the generated ones, which the body cannot change: a
		// write copies a store its slot does not own.
		shows := make([]func() Argument, 0, len(p.Inputs))
		for i, input := range p.Inputs {
			var v Value
			if p.Listed {
				v = in.listValue(cf, listed[i])
			} else if input.Provider != nil && input.Provider.Ctor != nil {
				ctor := input.Provider.Ctor
				var vs []Value
				for _, param := range ctor.Sig.Params {
					vs = append(vs, in.generate(cf, r, param.Type, testplan.Depth, nil))
				}
				v = mustValue(in.call(cf, nil, ctor, nil, vs))
				shows = append(shows, func() Argument {
					shown := make([]string, len(vs))
					for j, param := range ctor.Sig.Params {
						shown[j] = in.argument(cf, param.Type, vs[j])
					}
					return Argument{Name: input.Name, Value: ctor.Name + "(" + strings.Join(shown, ", ") + ")"}
				})
			} else {
				v = in.generate(cf, r, input.Type, testplan.Depth, input.Where)
			}
			switch {
			case len(shows) > i:
			case input.Provider != nil:
				// A provider's state may change as its operations run:
				// its text is the one it had before the case.
				a := Argument{Name: input.Name, Value: in.argument(cf, input.Type, v)}
				shows = append(shows, func() Argument { return a })
			default:
				gv := v
				shows = append(shows, func() Argument { return Argument{Name: input.Name, Value: in.argument(cf, input.Type, gv)} })
			}
			if input.Provider != nil {
				v = &operation{value: v, conv: input.Provider.Conv, env: cf.env}
			}
			if input.Param < 0 {
				// A member law's body calls functions that need its own
				// operation type: the provider under test is bound to it.
				cf.lawOwn = v
				to := input.Provider.Conv.To
				if to == nil {
					to = input.Type
				}
				cf.caps = &capEnv{key: typeKey(cf.rt(to)), op: v, next: cf.caps}
			} else {
				cf.bind(params[input.Param], v)
			}
		}
		result := in.runBodyFrame(cf, body, law, res)
		out.Cases = k + 1
		out.Status, out.Reason, out.Site = result.Status, result.Reason, result.Site
		if result.Schedule != 0 {
			out.Schedule = result.Schedule
		}
		if result.Status != Passed {
			out.Case, out.Arguments = k+1, shown(shows)
			return
		}
	}
	out.Case = 0
	return
}

// shown is the arguments of a reported case, written now.
func shown(shows []func() Argument) []Argument {
	args := make([]Argument, len(shows))
	for i, show := range shows {
		args[i] = show()
	}
	return args
}

func statusOf(ab *abrupt) Status {
	switch ab.kind {
	case ctlExhausted:
		return Incomplete
	case ctlUnsupported:
		return Skipped
	case ctlDefect, ctlInterrupt:
		return Defect
	case ctlAssert, ctlFail, ctlEscape:
		return Refuted
	}
	ice("unexpected generator control %d", ab.kind)
	return Defect
}
func mustCharge(in *interp, fr *frame, n int64) {
	if ab := in.charge(fr, fr.decl, n); ab != nil {
		panic(ab)
	}
}
func mustValue(v Value, ab *abrupt) Value {
	if ab != nil {
		panic(ab)
	}
	return v
}

func (in *interp) listValue(fr *frame, v testplan.Value) Value {
	mustCharge(in, fr, 1)
	if b, ok := v.Scalar.(bool); ok {
		return b
	}
	if n, ok := v.Scalar.(string); ok {
		x, ok := fromConstant(v.Type, constant.MakeFromLiteral(n, gotoken.INT, 0))
		if !ok {
			ice("a finite integer of %s", v.Type)
		}
		return x
	}
	children := make([]Value, len(v.Children))
	for i, c := range v.Children {
		children[i] = in.listValue(fr, c)
	}
	switch t := v.Type.(type) {
	case *check.Tuple:
		return tuple{elems: children}
	case *check.Named:
		switch t.Underlying().(type) {
		case *check.Struct:
			return &record{t: t, key: argsKey(t, fr.env), fields: children}
		default:
			return children[0]
		}
	}
	ice("finite value of %s", v.Type)
	return nil
}

func (in *interp) generate(fr *frame, r *testgen.Stream, t check.Type, d int, ref *check.Refinement) Value {
	mustCharge(in, fr, 1)
	if d < -in.budget.Depth {
		panic(fr.abrupt(ctlExhausted, fr.decl, nil, "generator depth bound reached"))
	}
	if ref != nil {
		return in.genRefined(fr, r, t, d, ref)
	}
	switch u := t.(type) {
	case *check.Basic:
		switch u.Kind {
		case check.Bool:
			return r.Bool()
		case check.String:
			return r.Str()
		case check.Float32:
			return float32(r.F64())
		case check.Float64:
			return r.F64()
		case check.Complex64:
			return complex(float32(r.F64()), float32(r.F64()))
		case check.Complex128:
			return complex(r.F64(), r.F64())
		default:
			v, ok := fromInt64(u.Kind, r.I64())
			if ok {
				return v
			}
		}
	case *check.Slice:
		n := 0
		if d > 0 {
			n = r.Intn(r.Limit(d) + 1)
		}
		mustCharge(in, fr, int64(n))
		xs := make([]Value, n)
		for i := range xs {
			xs[i] = in.generate(fr, r, u.Elem, d-1, nil)
		}
		return newSlice(xs)
	case *check.Map:
		m := newMap()
		if d <= 0 {
			return m
		}
		for n := r.Intn(r.Limit(d) + 1); n > 0; n-- {
			k := in.generate(fr, r, u.Key, d-1, nil)
			v := in.generate(fr, r, u.Elem, d-1, nil)
			m.b.put(k, v)
		}
		return m
	case *check.Tuple:
		xs := make([]Value, len(u.Types))
		for i, e := range u.Types {
			xs[i] = in.generate(fr, r, e, d-1, nil)
		}
		return tuple{elems: xs}
	case *check.Union:
		return in.genVariant(fr, r, u.Variants, d)
	case *check.Named:
		if ctor := testplan.Constructor(u); ctor != nil {
			var args []Value
			for _, p := range ctor.Sig.Params {
				args = append(args, in.generate(fr, r, p.Type, d-1, nil))
			}
			return mustValue(in.call(fr, nil, ctor, nil, args))
		}
		if ref := check.RefinementOf(u); ref != nil {
			return in.genRefined(fr, r, u, d, ref)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			xs := make([]Value, len(under.Fields))
			for i, f := range under.Fields {
				xs[i] = in.generate(fr, r, f.Type, d-1, nil)
			}
			return &record{t: u, key: argsKey(u, fr.env), fields: xs}
		case *check.Union:
			return in.genVariant(fr, r, under.Variants, d)
		default:
			return in.generate(fr, r, under, d, nil)
		}
	}
	panic(fr.unsupported(fr.decl, "a generator for %s", t))
}
func (in *interp) genVariant(fr *frame, r *testgen.Stream, vs []check.Type, d int) Value {
	if d <= 0 {
		vs = testplan.SmallVariants(vs)
	}
	if len(vs) == 0 {
		ice("a union with no finite variant")
	}
	k := 0
	if len(vs) > 1 {
		k = r.Intn(len(vs))
	}
	return in.generate(fr, r, vs[k], d-1, nil)
}
func (in *interp) genRefined(fr *frame, r *testgen.Stream, t check.Type, d int, ref *check.Refinement) Value {
	var spans [][2]int64
	for _, s := range ref.Spans {
		hi := new(big.Int).Set(s.Hi)
		if ref.Len {
			if cap := new(big.Int).Add(s.Lo, big.NewInt(32)); hi.Cmp(cap) > 0 {
				hi = cap
			}
		}
		spans = append(spans, [2]int64{int64(s.Lo.Uint64()), int64(hi.Uint64())})
		if s.Lo.Sign() < 0 {
			spans[len(spans)-1][0] = s.Lo.Int64()
		}
		if hi.Sign() < 0 {
			spans[len(spans)-1][1] = hi.Int64()
		}
	}
	n := r.In(spans)
	if !ref.Len {
		v, ok := fromInt64(basicOf(t).Kind, n)
		if !ok {
			ice("refinement of %s", t)
		}
		return v
	}
	if n < 0 || n > in.stepsLeft() {
		panic(fr.abrupt(ctlExhausted, fr.decl, nil, "refined length exceeds remaining work"))
	}
	mustCharge(in, fr, n)
	under := t
	if x, ok := t.(*check.Named); ok {
		under = x.Underlying()
	}
	if s, ok := under.(*check.Slice); ok {
		xs := make([]Value, int(n))
		for i := range xs {
			xs[i] = in.generate(fr, r, s.Elem, d-1, nil)
		}
		return newSlice(xs)
	}
	return r.StrLen(int(n))
}
