package eval

import (
	"go/constant"
	gotoken "go/token"
	"math/big"
	"slices"
	"strconv"
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
	// cell holds value once an operation borrows its receiver: the Go
	// target's operation value holds a pointer to its provider's copy, so
	// a write through one call is seen by the next (std json's Text, LR3).
	cell *slot
}

// recv is the provider as fn, one of its operations, takes it: a borrow
// of the operation's own cell when fn borrows its receiver.
func (r *operation) recv(fn *check.Func) Value {
	if fn.Ptr {
		if r.cell == nil {
			r.cell = &slot{v: r.value}
		}
		return &ptrV{root: r.cell}
	}
	if r.cell != nil {
		return r.cell.v
	}
	return r.value
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
	var v Value
	var ab *abrupt
	witnessed := false
	if c.Kind == check.CallOp {
		// A value of a constrained type parameter runs its witness's
		// function (witness.go).
		if tp := recvParam(fr.typeOf(c.Recv)); tp != nil {
			v, ab, witnessed = in.witnessCall(fr, x, fr.env, tp, c.Op.Name, recv, args)
		}
	}
	if !witnessed {
		v, ab = in.dispatch(fr, x, recv, c.Op.Name, args)
	}
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
	} else if law.Decl.Rewrite != nil && why == "" {
		why = RewriteVerdict()
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
	in.casesBudget(p.Count)
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
	out.Evidence, out.Seed, out.form = "tested", in.budget.Seed, formDrawn
	if p.Listed {
		out.Evidence, out.form = "proved", formListed
	}
	if !law && len(params) == 0 {
		out.Evidence, out.form = "evaluated", formSingle
	}
	r := testgen.New(in.budget.Seed, res.Name)
	r.Charge = func(n int64) { mustCharge(in, fr, n) }
	// run draws case k's arguments from r, as it stands, and runs the body.
	run := func(k int) (Result, []func() Argument) {
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
					return Argument{Name: input.Name, Value: in.root.Qualified(ctor.Pkg, ctor.Name) + "(" + strings.Join(shown, ", ") + ")"}
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
		return in.runBodyFrame(cf, body, law, res), shows
	}
	for k := 0; k < p.Count; k++ {
		out.Case = k + 1
		mustCharge(in, fr, 1)
		r.Start(k + 1)
		r.Record()
		result, shows := run(k)
		out.Cases = k + 1
		if result.Status == Refuted && !p.Listed && len(p.Inputs) > 0 {
			result, shows = in.shrinkCase(r, result, shows, func() (Result, []func() Argument) { return run(k) })
		}
		out.Status, out.Reason, out.Site, out.escaped, out.Message = result.Status, result.Reason, result.Site, result.escaped, result.Message
		if result.Schedule != 0 {
			out.Schedule = result.Schedule
		}
		if result.Status != Passed {
			out.Case, out.Arguments, out.Drawn = k+1, shown(shows), result.Drawn
			return
		}
	}
	out.Case = 0
	return
}

// shrinkCase shrinks a refuted drawn case as the native runner does
// ($Prop; TESTSPEED FT1): the case's tape is replayed from smaller tapes
// (shrinkTape) while it is refuted the same way, and the smallest one's
// result and arguments are the case's, its Drawn the drawn arguments when
// they differ. A smallest tape that no longer fails the same way (the body
// is not a function of its draws) leaves the drawn case. A replay the
// evaluator cannot finish (a skipped form, the budget spent) ends the
// shrink, and the drawn case is the result: the refutation already
// decided the obligation, so it is reported refuted with the case as
// drawn, never downgraded to skipped or incomplete (EVAL-SHRINK-REFUTED;
// the native runner would have shrunk it further). The drawn arguments
// are written before the shrink, while the budget lasts.
func (in *interp) shrinkCase(r *testgen.Stream, drawn Result, drawnShows []func() Argument, run func() (Result, []func() Argument)) (Result, []func() Argument) {
	tape := r.Tape()
	large := shown(drawnShows)
	drawnShows = make([]func() Argument, len(large))
	for i, a := range large {
		drawnShows[i] = func() Argument { return a }
	}
	stopped := false
	replay := func(c []uint64) (Result, []func() Argument) {
		r.Replay(c)
		return run()
	}
	best, _ := shrinkTape(tape, func(c []uint64) bool {
		if stopped {
			return false
		}
		got, _ := replay(c)
		switch got.Status {
		case Skipped, Incomplete:
			stopped = true
			return false
		}
		return got.Status == Refuted && got.escaped == drawn.escaped
	}, shrinkBudget)
	defer r.EndReplay()
	if stopped {
		return drawn, drawnShows
	}
	if slices.Equal(best, tape) {
		return drawn, drawnShows
	}
	got, shows := replay(best)
	if got.Status != Refuted || got.escaped != drawn.escaped {
		return drawn, drawnShows // not deterministic: the drawn case
	}
	small := shown(shows)
	if !slices.Equal(small, large) {
		got.Drawn = large
	}
	return got, func() []func() Argument {
		out := make([]func() Argument, len(small))
		for i, a := range small {
			out[i] = func() Argument { return a }
		}
		return out
	}()
}

// shown is the arguments of a reported case, written now.
func shown(shows []func() Argument) []Argument {
	args := make([]Argument, len(shows))
	for i, show := range shows {
		args[i] = show()
	}
	return args
}

// intShape is the width (0: the target's int) and sign an integer type is
// drawn with, by the native generator's rule (lower's genExpr): the width
// its name ends in, else the int's; signed unless its name begins with u;
// a Duration as a signed 64-bit integer. byte and rune are drawn as a
// signed int and converted, as the native runner draws them.
func intShape(u *check.Basic) (width uint, signed, ok bool) {
	switch {
	case u.Kind == check.Duration:
		return 64, true, true
	case u.Kind < check.Int || u.Kind > check.Uintptr:
		return 0, false, false
	}
	for _, w := range []uint{8, 16, 32, 64} {
		if strings.HasSuffix(u.Name, strconv.FormatUint(uint64(w), 10)) {
			width = w
		}
	}
	return width, !strings.HasPrefix(u.Name, "u"), true
}

// abruptResult is res ended by ab outside a body (a context's build or
// release): its status, reason and site.
func (in *interp) abruptResult(res Result, ab *abrupt) Result {
	res.Status, res.Reason, res.Site = statusOf(ab), ab.msg, in.site(ab.pkg, ab.decl, ab.at)
	res.escaped = ab.kind == ctlFail || ab.kind == ctlEscape
	res.Message = ""
	if res.Status == Defect {
		res.Message = defectMessage
	}
	return res
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
			// An integer is drawn as the native runner draws it
			// (testgen.Stream.Integer): its width and sign decide what
			// the tape records, and so what a shrink can lower.
			if width, signed, ok := intShape(u); ok {
				v, _ := fromInt64(u.Kind, int64(r.Integer(width, signed)))
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
		if _, ops := u.Underlying().(*check.Interface); ops {
			return in.genBuilt(fr, r, u, d)
		}
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

// genBuilt is a value of the operation type n drawn as the Go build draws
// it (lower/builders.go, genBuilt): its builder (check.Builder, a New<X>
// with no row that provides n) called on generated arguments, and put into
// n when it gives a provider. A builder of no arguments makes the case's
// one value of it (rule 4: the case's world), as the Go build's built does.
// The builder is the one the checked package sees (its test builders too),
// as the Go build asks the package it lowers.
func (in *interp) genBuilt(fr *frame, r *testgen.Stream, n *check.Named, d int) Value {
	b := in.root.Builder(n)
	if b == nil {
		panic(fr.unsupported(fr.decl, "an operation type with no builder (%s)", n))
	}
	targs, params := b.Instance(n)
	build := func() any {
		args := make([]Value, len(params))
		for i, p := range params {
			args[i] = in.generate(fr, r, p, d-1, nil)
		}
		return mustValue(in.call(fr, nil, b.Func, targs, args))
	}
	var v Value
	if b.World() {
		// The case keeps the builder's result, not its conversion: one
		// fstest.NewMemory is both a case's fs.Files and its fs.Writes.
		v = r.Built(b.Func.Pkg.Path+"."+b.Func.Name, build)
	} else {
		v = build()
	}
	if b.Conv.Kind == check.ConvOperations {
		v = &operation{value: v, conv: b.Conv, env: fr.env}
	}
	return v
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
	if ref.Pattern != nil {
		// A pattern (types.md, Refinements 11): a length it reaches, then a
		// walk of its automaton (check.Matcher.Generate).
		n := r.In(ref.GenLengths(32))
		mustCharge(in, fr, n)
		s, ok := ref.Pattern.Generate(int(n), r.Intn)
		if !ok {
			ice("no value of %d bytes matches the pattern of %s", n, t)
		}
		return s
	}
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
