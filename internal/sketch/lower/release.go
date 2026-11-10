package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// The release owner (runtime.md, I2; resources.md, R1 and R9). A release
// runs masked from its first instruction, its first capability checkpoint
// included, to its last, on the task that runs it. Its defect joins the
// exit it ends with, and a stop it masks stays pending for the caller's
// next checkpoint. runtime/v1 holds the one mask and cause boundary
// (release in task.go). Each lowered release reaches it through one
// binding of runtimeBindings whose release route is not notRelease: those
// bindings are the release catalog, and runtimeCall is their one emitter.
// TestRuntime199MaskProperty derives its release axis from the catalog;
// the owner scan fails a route that masks or releases on its own.
type releaseRoute uint8

const (
	notRelease releaseRoute = iota
	// callerRelease is the body of a checked Close (Info.Closes on its
	// declaration), masked on the invocation's task (v1.Release). Its Go
	// function is the one body every dispatch path reaches: a call, a dot
	// call, an operation of an operation type or a capability (whose
	// adapter calls it), a function value and a method value. So the mask
	// is in the body and no dispatch path holds a copy of the rule.
	callerRelease
	// frameRelease is an exit action (v1.OnExit), masked on its frame's
	// task when the frame ends. Its callback runs in the environment
	// captured where it was registered; a frame never changes its task, so
	// that is the ending task.
	frameRelease
)

// No generated Close route exists: aggregate Close synthesis is #197. A
// generated Close declares its route here, or lowers as a checked Close.

// isClose reports a checked Close: the checker's identity fact on f's
// declaration (lesson 2), never its spelling.
func isClose(f *check.Func) bool {
	return f != nil && f.Decl != nil && f.Pkg != nil && f.Pkg.Info != nil && f.Pkg.Info.Closes[f.Decl] != nil
}

// providerRelease is the release of a provider an owning with built: the
// call of its checked Close (Acquisition.Close, the checker's fact) on val,
// the provider's value, in outer, the environment outside the with, where
// the checker charged the release's needs. The Close's Go function masks
// its own body (closeBody), so the call adds no mask, as no dispatch path
// to a checked Close adds one. Each form of C29 releases (lower-v1 stage
// 4):
//   - a Close that is an operation of the provider's operation type calls
//     the operation, whose implementation is a checked Close that masks
//     its own body;
//   - a generic Close is called with the type arguments that its
//     parameter's type binds against the provider's type;
//   - a Close that takes a pointer to a provider that is a value takes the
//     address of the with's own copy of the provider (publishProvider
//     spills it), which no operation of the block sees.
func (fc *funcCtx) providerRelease(a *check.Acquisition, outer, val string, site syntax.Node) string {
	cl := a.Close
	if cl.HostClose() {
		return fc.hostRelease(cl, outer, val, site)
	}
	if cl.Func == nil {
		if cl.Op == nil {
			ice(site, "a provider's Close with neither a function nor an operation")
		}
		text, _, _ := fc.opCall(cl.Receiver, cl.Op, nil, []string{val}, outer, nil, nil, site)
		return text
	}
	if !isClose(cl.Func) || len(cl.Func.Sig.Params) != 1 {
		ice(site, "a provider's Close with no checked Close identity")
	}
	fd := fc.l.useFunc(cl.Func, site)
	if _, _, std := fc.l.stdBody(fd); std || fd.extern != "" {
		// A Close takes a named type, outside a user extern's domain (C26),
		// and no std package declares a Close function.
		ice(site, "a provider whose Close is a std or extern function, which the checker refuses (C26)")
	}
	_, byPtr := cl.Func.Sig.Params[0].Type.(*check.Pointer)
	_, ptrValue := cl.Receiver.(*check.Pointer)
	recv := val
	switch {
	case byPtr && !ptrValue:
		recv = "&" + val
	case !byPtr && ptrValue:
		recv = "*" + val
	}
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + outer + ")"
	}
	targs := ""
	if len(cl.Func.Sig.TParams) > 0 {
		if len(witnessParams(cl.Func.Sig)) > 0 {
			ice(site, "a provider whose generic Close has a type parameter with operations, which the checker refuses (S109, closeWitnessed; TestCloseRefusals)")
		}
		targs = fc.typeArgs(cl.Func.Sig, closeTypeArgs(cl, site), site)
	}
	return fd.name + targs + "(" + outer + ", " + recv + ")"
}

// hostRelease is the release of a Go value (IG9; q61): Go's Close, called
// on val inside the release boundary (v1.Release, the callerRelease
// route), as a checked Close's body runs. A Close() error's non-nil error
// is a defect naming site, the acquisition (v1.CloseFailed). Go code has
// no checkpoint, so the mask only keeps a stop pending past it.
func (fc *funcCtx) hostRelease(cl *check.Close, outer, val string, site syntax.Node) string {
	call := "(" + val + ").Close()"
	if cl.HostFallible() {
		src, line, column := fc.l.locate(site, site.Pos())
		at := strconv.Quote(src + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(column))
		call = fc.runtimeCall("CloseFailed", fc.ownBinding("CloseFailed", site), "", "", []string{at, call}, "", nil, nil, site)
	}
	return fc.runtimeCall("Release", fc.ownBinding("Release", site), "", "", []string{"func() { " + call + " }"}, outer, nil, nil, site)
}

// closeTypeArgs are the type arguments of a generic Close's call: each of
// its type parameters, bound by matching its parameter's type against the
// provider's type (the receiver G[int] against the parameter G[T] or
// *G[T]). Rows are erased (typeArgs skips them).
func closeTypeArgs(cl *check.Close, site syntax.Node) []check.Type {
	sig := cl.Func.Sig
	binds := map[*check.TypeParam]check.Type{}
	param, recv := sig.Params[0].Type, cl.Receiver
	if p, ok := param.(*check.Pointer); ok {
		param = p.Elem
	}
	if p, ok := recv.(*check.Pointer); ok {
		recv = p.Elem
	}
	var match func(p, r check.Type)
	match = func(p, r check.Type) {
		switch p := p.(type) {
		case *check.TypeParam:
			binds[p] = r
		case *check.Named:
			rn, ok := r.(*check.Named)
			if !ok || len(rn.TArgs) != len(p.TArgs) {
				ice(site, "the Close parameter %s does not match the provider %s", p, r)
			}
			for i := range p.TArgs {
				match(p.TArgs[i], rn.TArgs[i])
			}
		case *check.Pointer:
			if rp, ok := r.(*check.Pointer); ok {
				match(p.Elem, rp.Elem)
			}
		case *check.Slice:
			if rs, ok := r.(*check.Slice); ok {
				match(p.Elem, rs.Elem)
			}
		case *check.Array:
			if ra, ok := r.(*check.Array); ok {
				match(p.Elem, ra.Elem)
			}
		case *check.Map:
			if rm, ok := r.(*check.Map); ok {
				match(p.Key, rm.Key)
				match(p.Elem, rm.Elem)
			}
		}
	}
	match(param, recv)
	args := make([]check.Type, len(sig.TParams))
	for i, tp := range sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		t, ok := binds[tp]
		if !ok {
			ice(site, "the generic Close %s does not bind its type parameter %s from the provider %s", cl.Func.Name, tp.Name, cl.Receiver)
		}
		args[i] = t
	}
	return args
}

// providerCloser returns the checked release callback of this acquisition.
// The environment is captured from outside the with, where its release
// needs were charged. A plain provider has no release.
func (fc *funcCtx) providerCloser(a *check.Acquisition, outer, val string, site syntax.Node) string {
	if !a.Releases() {
		return "nil"
	}
	saved := fc.env
	fc.env = outer
	callback := fc.envClosure(capturedEnv, "", site, func(env string) ([]string, string, string) {
		if a.Drop != nil {
			// A provider whose parts hold resources: its generated
			// release (B2), its own Close first.
			return nil, "", strings.Repeat("\t", fc.depth+1) + fc.glue(a.Drop, val, "", env, site) + "\n"
		}
		return nil, "", strings.Repeat("\t", fc.depth+1) + fc.providerRelease(a, env, val, site) + "\n"
	})
	fc.env = saved
	return callback
}

// acquireFlow transports lexical control through a native callback. It is
// not a source frame: its return/failure and loop targets still belong to
// the enclosing source frame (check/frame.go, ContextLit). A completed
// construction alone returns a release to WithAcquire for registration.
type acquireFlow struct {
	flow, value, err string // 0 completes, 1 returns, 2 breaks, 3 continues
	loops            int
}

// sourceReturn writes the return of the source frame. Acquisition uses a
// native callback solely to delimit rt's acquiring region, so a lexical
// return inside an inline provider argument crosses it explicitly.
func (fc *funcCtx) sourceReturn(vals ...string) {
	if a := fc.acquiring; a != nil {
		var dest []string
		if a.value != "" {
			dest = append(dest, a.value)
		}
		if a.err != "" {
			dest = append(dest, a.err)
		}
		if len(dest) > 0 {
			fc.line(strings.Join(dest, ", ") + " = " + strings.Join(vals, ", "))
		}
		fc.line(a.flow + " = 1")
		fc.line("return nil, nil")
		return
	}
	if fc.frame.unit {
		if len(vals) != 1 {
			ice(nil, "a return of %d results from a function value with no result", len(vals))
		}
		fc.line(fc.errReturn(vals[0]))
		return
	}
	if fl := fc.frame.flat; fl != nil {
		// The flat step (machinerun.go): a decision held as the union,
		// returned through a with's flow, is given as a value.
		if len(vals) != 1 {
			ice(nil, "a return of %d results from a machine's flat step", len(vals))
		}
		vals = []string{fl.flat + "{}.decide(" + vals[0] + ")"}
	}
	fc.line("return " + strings.Join(vals, ", "))
}

// withClose extends the with lifetime owner to construction and release
// registration as one acquisition. No other lowering path registers a
// provider. WithAcquire reuses v1's list and rt's acquiring region, and
// restores the mask before this code handles an error or lexical transfer.
func (fc *funcCtx) withClose(a *check.Acquisition, target, outer, ws string, site syntax.Node) {
	f := fc.frame
	flow := &acquireFlow{flow: fc.temp("flow"), loops: len(fc.loops)}
	fc.line("var " + flow.flow + " int")
	if f.result != nil {
		flow.value = fc.temp("returned")
		fc.line("var " + flow.value + " " + fc.goType(f.result))
	}
	if f.fallible {
		flow.err = fc.temp("returnedErr")
		fc.line("var " + flow.err + " error")
	}
	open := fc.envClosure(capturedEnv, "", site, func(env string) ([]string, string, string) {
		inner := *fc
		inner.out = &strings.Builder{}
		inner.depth++
		inner.acquiring = flow
		val := inner.publishProvider(a, target)
		inner.line("return " + inner.providerCloser(a, outer, val, site) + ", nil")
		return nil, " (func(), error)", inner.out.String()
	})
	err := fc.temp("acquireErr")
	fc.line(err + " := " + fc.scopeCall("Scope.WithAcquire", ws, []string{open}, site))
	fc.line("if " + err + " != nil {")
	fc.depth++
	fc.line(fc.scopeCall("Scope.EndWith", ws, []string{"&" + err}, site))
	fc.fail(err)
	fc.depth--
	fc.line("}")
	fc.line("if " + flow.flow + " == 1 {")
	fc.depth++
	errArg := "nil"
	var vals []string
	if flow.value != "" {
		vals = append(vals, flow.value)
	}
	if flow.err != "" {
		errArg = "&" + flow.err
		vals = append(vals, flow.err)
	}
	fc.line(fc.scopeCall("Scope.EndWith", ws, []string{errArg}, site))
	fc.sourceReturn(vals...)
	fc.depth--
	fc.line("}")
	if flow.loops > 0 {
		for _, branch := range []struct {
			code string
			tok  syntax.Token
		}{{"2", syntax.BREAK}, {"3", syntax.CONTINUE}} {
			fc.line("if " + flow.flow + " == " + branch.code + " {")
			fc.depth++
			fc.line(fc.scopeCall("Scope.EndWith", ws, []string{"nil"}, site))
			fc.branch(&syntax.BranchStmt{Tok: branch.tok})
			fc.depth--
			fc.line("}")
		}
	}
}

// entersRelease reports a call whose callee is a checked Close. The call
// starts a release, so it is not an interruption point of its own: a
// capability's Close writes no poll before its operation, as a direct
// Close call has none, and the stop stays pending for the caller's next
// checkpoint after the release.
func (fc *funcCtx) entersRelease(x *syntax.CallExpr) bool {
	return fc.l.info.Closes[x] != nil
}

// closeBody writes emit's statements, the whole body of the function f,
// inside the release of a checked Close: frame scope, entry poll and
// source body, so no checkpoint precedes the mask. Any other function's
// body is written as it is.
func (fc *funcCtx) closeBody(f *check.Func, site syntax.Node, emit func()) {
	if !isClose(f) {
		emit()
		return
	}
	if len(f.Sig.Results) != 0 || len(rowElems(f.Sig.Errors)) != 0 {
		ice(site, "a checked Close with a result or a failure row")
	}
	const name = "Release"
	if runtimeBindings[name].release != callerRelease {
		ice(site, "the binding %s is not the caller release route", name)
	}
	body, err := v1.BodyOf(name)
	if err != nil {
		ice(site, "the release route %s has no runtime/v1 body: %v", name, err)
	}
	outer, depth := fc.out, fc.depth
	fc.out = &strings.Builder{}
	fc.depth = depth + 1
	emit()
	inner := fc.out.String()
	fc.out, fc.depth = outer, depth
	block := "func() {\n" + inner + strings.Repeat("\t", depth) + "}"
	fc.line(fc.runtimeCall(name, body, "", "", []string{block}, fc.envExpr(site), nil, nil, site))
}

// Owned values (#197 L8 B1; resources R5-R10, R21, R41, R43). The checker
// gives each owned local, taken parameter and temporary its release
// (Info.OwnedVars, Info.OwnedTemps), marks each hand-off (Info.Handoffs)
// and records the release plan (Info.Releases). The lowering registers
// each value on its frame's one list in the step that receives it
// (v1.Owning, R8); a hand-off removes the entry (v1.Hand), a return keeps
// it until the frame's exit is known (v1.Returned); a scope that ends
// inside the frame releases what the plan names there (Owned.Release),
// and the frame's End releases the rest on every edge that leaves the
// function, each release after an earlier defect, every defect kept (R9).
// The entry's presence is the drop flag (R21). This is the plain
// lowering; P-L8 adds the static fast path and keeps this one selectable.

// ownBinding is the v1 body of an ownership binding.
func (fc *funcCtx) ownBinding(name string, site syntax.Node) v1.Body {
	body, err := v1.BodyOf(name)
	if err != nil {
		ice(site, "the ownership binding %s has no runtime/v1 body: %v", name, err)
	}
	return body
}

// ownRelease is the Go release of an owned value of type t: a function of
// the value that calls its checked Close, which masks its own body, or,
// when d is generated (d.Glue()), a function of the value and the fields
// its owner took in place that releases each part (glue).
func (fc *funcCtx) ownRelease(d *check.Drop, t check.Type, site syntax.Node) string {
	v := fc.temp("owned")
	if d.Glue() {
		taken := fc.temp("taken")
		return "func(" + v + " " + fc.goType(t) + ", " + taken + " uint64) { " + fc.glue(d, v, taken, fc.envExpr(site), site) + " }"
	}
	text := fc.providerRelease(&check.Acquisition{Close: d.Close}, fc.envExpr(site), v, site)
	return "func(" + v + " " + fc.goType(t) + ") { " + text + " }"
}

// Generated release (#197 L8 B2; resources R3, R37, R39, Gap 36): the
// checker's Drop graph of the value's type, written as one Go function
// literal at the registration. Each part's checked Close runs through one
// v1.Drop, after an earlier part's defect, every defect kept (R9); a type
// reached from its parts more than once, or from itself, has a local
// function of its own. This is the plain lowering; P-L8 adds the static
// form.

// glue is the statements that release v, a value of d's type, skipping
// the top record's fields set in taken.
func (fc *funcCtx) glue(d *check.Drop, v, taken, env string, site syntax.Node) string {
	return fc.glueOn(d, v, taken, env, fc.runtimeCall("Scope.Drop", fc.ownBinding("Scope.Drop", site), "", fc.frameScope(site), nil, "", nil, nil, site), site)
}

// glueOn is glue with the Go text of its v1.Drop, for a release outside a
// frame (resultRelease).
func (fc *funcCtx) glueOn(d *check.Drop, v, taken, env, dropText string, site syntax.Node) string {
	g := &glueGen{fc: fc, site: site, env: env, names: map[*check.Drop]string{}}
	g.drop = fc.temp("drop")
	top := g.parts(d, v, taken)
	var b strings.Builder
	b.WriteString(g.drop + " := " + dropText + "; ")
	for _, n := range g.order {
		b.WriteString("var " + g.names[n] + " func(" + fc.goType(n.Type) + "); ")
	}
	for _, n := range g.order {
		b.WriteString(g.defs[n])
	}
	b.WriteString(top)
	b.WriteString(fc.runtimeCall("Drop.End", fc.ownBinding("Drop.End", site), "", g.drop, nil, "", nil, nil, site))
	return b.String()
}

type glueGen struct {
	fc    *funcCtx
	site  syntax.Node
	env   string
	drop  string
	names map[*check.Drop]string
	defs  map[*check.Drop]string
	order []*check.Drop
}

// release is the statements that release x, a value of d's type: one
// part, the call of its checked Close, or the call of d's local function.
func (g *glueGen) release(d *check.Drop, x string) string {
	if !d.Glue() {
		return g.part(d, x)
	}
	name, ok := g.names[d]
	if !ok {
		name = g.fc.temp("release")
		g.names[d] = name
		if g.defs == nil {
			g.defs = map[*check.Drop]string{}
		}
		g.order = append(g.order, d)
		y := g.fc.temp("v")
		g.defs[d] = name + " = func(" + y + " " + g.fc.goType(d.Type) + ") { " + g.parts(d, y, "") + "}; "
	}
	return name + "(" + x + "); "
}

// part is one part's release: the checked Close of d's type on x.
func (g *glueGen) part(d *check.Drop, x string) string {
	call := g.fc.providerRelease(&check.Acquisition{Close: d.Close}, g.env, x, g.site)
	return g.fc.runtimeCall("Drop.Part", g.fc.ownBinding("Drop.Part", g.site), "", g.drop, []string{"func() { " + call + " }"}, "", nil, nil, g.site) + "; "
}

// parts is the statements that release x part by part (R3): the type's
// own Close, then its fields last first, skipping those set in taken
// (the top value only), its elements last first or each Map value, or
// its active variant.
func (g *glueGen) parts(d *check.Drop, x, taken string) string {
	var b strings.Builder
	if d.Close != nil {
		b.WriteString(g.part(d, x))
	}
	if len(d.Fields) > 0 {
		names := g.fieldNames(d.Type)
		for _, f := range d.Fields {
			sel := x + "." + names(f)
			if taken != "" && f.Index < 64 {
				b.WriteString("if " + taken + "&(1<<" + strconv.Itoa(f.Index) + ") == 0 { " + g.release(f.Drop, sel) + "}; ")
				continue
			}
			b.WriteString(g.release(f.Drop, sel))
		}
	}
	if d.Elem != nil {
		e := g.fc.temp("e")
		switch underOf(d.Type).(type) {
		case *check.Slice, *check.Array:
			i := g.fc.temp("i")
			b.WriteString("for " + i + " := len(" + x + ") - 1; " + i + " >= 0; " + i + "-- { " + e + " := " + x + "[" + i + "]; " + g.release(d.Elem, e) + "}; ")
		case *check.Map:
			b.WriteString("for _, " + e + " := range " + x + " { " + g.release(d.Elem, e) + "}; ")
		default:
			ice(g.site, "a generated release of %s, which has elements but is no slice, array or Map", d.Type)
		}
	}
	if d.Drain != nil {
		// A std queue of resources (Q7, R13): Drain ends admission and
		// gives the values left, oldest first; they are released last
		// entered first.
		xs, i, e := g.fc.temp("left"), g.fc.temp("i"), g.fc.temp("e")
		n, _ := d.Type.(*check.Named)
		if n == nil {
			ice(g.site, "a drain of %s, which is no named operation type", d.Type)
		}
		name := n.Obj.Name + ".Drain"
		drain := g.fc.runtimeCall(name, g.fc.ownBinding(name, g.site), "", x, nil, "", nil, nil, g.site)
		b.WriteString(xs + " := " + drain + "; for " + i + " := len(" + xs + ") - 1; " + i + " >= 0; " + i + "-- { " + e + " := " + xs + "[" + i + "]; " + g.release(d.Drain, e) + "}; ")
	}
	if len(d.Variants) > 0 {
		if fu := g.fc.l.flatUnionOf(d.Type); fu != nil {
			flatBail(g.site, fu)
		}
		y := g.fc.temp("v")
		b.WriteString("switch " + y + " := any(" + x + ").(type) { ")
		for _, v := range d.Variants {
			b.WriteString("case " + g.fc.goType(v.Type) + ": " + g.release(v, y))
		}
		b.WriteString("}; ")
	}
	return b.String()
}

// fieldNames maps a part of a record or tuple of type t to its Go field.
func (g *glueGen) fieldNames(t check.Type) func(check.DropField) string {
	if _, ok := underOf(t).(*check.Tuple); ok {
		return func(f check.DropField) string { return tupleField(f.Index) }
	}
	n, ok := t.(*check.Named)
	for ok {
		td := g.fc.l.typeDecl(n.Obj, g.site)
		if td.kind != kindDefined {
			return func(f check.DropField) string {
				name, ok := td.fieldName[f.Name]
				if !ok {
					ice(g.site, "the record %s has no Go field for %s", t, f.Name)
				}
				return name
			}
		}
		n, ok = n.Defined().(*check.Named)
	}
	ice(g.site, "a generated release of the fields of %s, which is no record", t)
	return nil
}

// underOf is t's underlying type.
func underOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

// own registers val, a value of type t that key's owner owns, and returns
// the Go expression that yields it: the step that receives the value.
func (fc *funcCtx) own(key any, d *check.Drop, t check.Type, val string, site syntax.Node) string {
	if fc.frame != nil && fc.frame.static != nil {
		return fc.staticOwn(key, d, t, val, site)
	}
	if fc.l.owned == nil {
		fc.l.owned = map[any]string{}
	}
	h := fc.temp("own")
	fc.l.owned[key] = h
	fc.line(h + " := " + fc.runtimeCall("Scope.Owner", fc.ownBinding("Scope.Owner", site), "", fc.frameScope(site), nil, "", nil, nil, site))
	return fc.owning(h, val, d, t, site)
}

// owning is the registration of val in the entry h (v1.Owning, or
// v1.OwningParts for a generated release).
func (fc *funcCtx) owning(h, val string, d *check.Drop, t check.Type, site syntax.Node) string {
	name := "Owning"
	if d.Glue() {
		name = "OwningParts"
	}
	return fc.runtimeCall(name, fc.ownBinding(name, site), "", "", []string{h, val, fc.ownRelease(d, t, site)}, "", nil, nil, site)
}

// takeField marks the field a call takes in place on its owned record's
// entry (Info.FieldTakes; R39, Gap 38), before the call: the record's
// release skips it from here on.
func (fc *funcCtx) takeField(x syntax.Expr) {
	sel, ok := unparen(x).(*syntax.SelectorExpr)
	if !ok {
		return
	}
	ft, ok := fc.l.info.FieldTakes[sel]
	if !ok {
		return
	}
	h := fc.l.owned[ft.Var]
	if h == "" {
		return
	}
	fc.line(fc.runtimeCall("Owned.TakeField", fc.ownBinding("Owned.TakeField", sel), "", h, []string{strconv.Itoa(ft.Index)}, "", nil, nil, sel))
}

// ownVar registers the local or parameter v, already bound to its Go
// variable, when the checker gives it an owner.
func (fc *funcCtx) ownVar(v *check.Var, site syntax.Node) {
	d := fc.l.info.OwnedVars[v]
	if d == nil {
		return
	}
	fc.line(fc.own(v, d, v.Type, fc.locals[v], site))
}

// ownTemp registers the value of x, a temporary its statement owns (R43),
// in the operand o: on success only, when x may fail.
func (fc *funcCtx) ownTemp(x syntax.Expr, o operand) operand {
	d := fc.l.info.OwnedTemps[x]
	if d == nil || o.val == "" {
		return o
	}
	if o.err == "" {
		o.val = fc.own(x, d, fc.typeOf(x), o.val, x)
		return o
	}
	val := fc.temp("t")
	fc.line(val + " := " + o.val)
	reg := fc.own(x, d, fc.typeOf(x), val, x)
	fc.line("if " + o.err + " == nil {")
	fc.line("\t" + reg)
	fc.line("}")
	o.val = val
	return o
}

// holdTaken finds the owned locals that x, a call of a bodiless std
// operation that takes them (queue Push, D-C), hands off: their entries
// stay on the frame until the call returns (handoff skips them), so a
// Push that fails releases its value and a stopped one leaves it to the
// frame's End (Q7, plan 1.4). It returns their Go entries.
func (fc *funcCtx) holdTaken(x *syntax.CallExpr, c *check.Call) []string {
	var takes []int
	args := x.Args
	switch {
	case c.Kind == check.CallOp && c.Op != nil && c.Op.Sig != nil && fc.stdOp(c):
		takes = c.Op.Sig.Takes
	case c.Kind == check.CallFunc && c.Func != nil && c.Func.Pkg != nil && c.Func.Pkg.Std() && c.Func.Decl != nil && c.Func.Decl.Extern != nil:
		takes = c.Func.Sig.Takes
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
	default:
		return nil
	}
	var held []string
	for _, i := range takes {
		if i >= len(args) {
			continue
		}
		id, ok := unparen(args[i]).(*syntax.Ident)
		if !ok || fc.l.info.Handoffs[id] == nil {
			continue
		}
		h := fc.l.owned[fc.l.info.Handoffs[id]]
		if h == "" {
			continue
		}
		if fc.held == nil {
			fc.held = map[*syntax.Ident]bool{}
		}
		fc.held[id] = true
		held = append(held, h)
	}
	return held
}

// stdOp reports a call of a std operation type's operation, whose body is
// runtime/v1's.
func (fc *funcCtx) stdOp(c *check.Call) bool {
	if c.Recv == nil {
		return false
	}
	n, ok := fc.typeOf(c.Recv).(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Std()
}

// settleTaken writes the call text and settles the held entries: on
// success the values left with the call (v1.Hand), on a failure each is
// released now, in the call's own step (D-C).
func (fc *funcCtx) settleTaken(held []string, text string, failsHere bool, site syntax.Node) operand {
	e := fc.temp("err")
	fc.line(e + " := " + text)
	fc.line("if " + e + " == nil {")
	for _, h := range held {
		fc.line("\t" + fc.runtimeCall("Hand", fc.ownBinding("Hand", site), "[struct{}]", "", []string{h, "struct{}{}"}, "", nil, nil, site))
	}
	fc.line("} else {")
	for _, h := range held {
		fc.line("\t" + fc.runtimeCall("Owned.Release", fc.ownBinding("Owned.Release", site), "", h, nil, "", nil, nil, site))
	}
	fc.line("}")
	if !failsHere {
		fc.line("_ = " + e)
		return operand{}
	}
	return operand{err: e}
}

// handoff is the Go value of id, a use of an owned value: as it is, or, at
// a hand-off, with its entry removed (v1.Hand), or kept until the frame's
// exit is known when the frame returns it (v1.Returned).
func (fc *funcCtx) handoff(id *syntax.Ident, val string, returned bool) string {
	v := fc.l.info.Handoffs[id]
	if fc.held[id] {
		return val // settled after the call (holdTaken)
	}
	if v == nil {
		return val
	}
	h := fc.l.owned[v]
	if h == "" {
		return val // a value of a type parameter: released by its instance (B2)
	}
	if st := fc.staticState(); st != nil {
		if _, ok := st.bit[v]; ok {
			return fc.staticHand(v, val, returned, id)
		}
	}
	name := "Hand"
	if returned {
		name = "Returned"
	}
	return fc.runtimeCall(name, fc.ownBinding(name, id), "", "", []string{h, val}, "", nil, nil, id)
}

// releaseAt writes the releases the plan records at key, a place that
// ends inside the frame: last acquired first. A value whose entry left
// the list (a hand-off on this path) is skipped by its entry (R21).
func (fc *funcCtx) releaseAt(key syntax.Node) {
	for _, r := range fc.l.info.Releases[key] {
		var h string
		if r.Var != nil {
			h = fc.l.owned[r.Var]
		} else {
			h = fc.l.owned[r.Temp]
		}
		if h == "" {
			continue // a value of a type parameter (B2)
		}
		if st := fc.staticState(); st != nil {
			key := any(r.Temp)
			if r.Var != nil {
				key = r.Var
			}
			if _, ok := st.bit[key]; ok {
				fc.staticRelease(key)
				continue
			}
		}
		fc.line(fc.runtimeCall("Owned.Release", fc.ownBinding("Owned.Release", key), "", h, nil, "", nil, nil, key))
	}
}

// reassignOwned writes x = val, x an owned local (R41): the new value is
// registered first, then the old one is released where the plan says it
// is still owned, then x takes the new value and its entry. So a defect
// in the old value's release leaves the new one owned. It reports false
// for any other target.
func (fc *funcCtx) reassignOwned(lhs syntax.Expr, target, val string) bool {
	id, ok := unparen(lhs).(*syntax.Ident)
	if !ok {
		return false
	}
	v, _ := fc.l.info.Uses[id].(*check.Var)
	cl := fc.l.info.OwnedVars[v]
	h := fc.l.owned[v]
	if cl == nil || h == "" {
		return false
	}
	t := fc.temp("t")
	fc.line(t + " := " + val)
	next := fc.temp("own")
	fc.line(next + " := " + fc.runtimeCall("Scope.Owner", fc.ownBinding("Scope.Owner", id), "", fc.frameScope(id), nil, "", nil, nil, id))
	fc.line(fc.owning(next, t, cl, v.Type, id))
	fc.releaseAt(id)
	fc.line(target + " = " + t)
	fc.line(h + " = " + next)
	return true
}

// scoped writes a scope's statements through write and, when they can
// end normally, the releases of the scope's end (Info.Releases[key]).
func (fc *funcCtx) scoped(key syntax.Node, write func() bool) bool {
	if cl, ok := key.(*syntax.CaseClause); ok {
		fc.patternOwners(cl)
	}
	t := write()
	if !t {
		fc.releaseAt(key)
	}
	return t
}

// patternOwners runs only after the selected pattern's tests and Go
// bindings. Removing the whole subject at header evaluation would also
// remove it in non-consuming arms. Registration precedes any arm code.
func (fc *funcCtx) patternOwners(cl *syntax.CaseClause) {
	plan := fc.l.info.PatternOwners[cl]
	for _, source := range plan.Sources {
		var key any = source.Temp
		if source.Var != nil {
			key = source.Var
		}
		h := fc.l.owned[key]
		if h == "" {
			continue
		}
		if st := fc.staticState(); st != nil {
			if _, ok := st.bit[key]; ok {
				fc.line("_ = " + fc.staticHand(key, "0", false, cl))
				continue
			}
		}
		fc.line("_ = " + fc.runtimeCall("Hand", fc.ownBinding("Hand", cl), "", "", []string{h, "0"}, "", nil, nil, cl))
	}
	for _, v := range plan.Bindings {
		fc.ownVar(v, cl)
	}
}

// bodyOwns reports a body that owns a value: a frame scope holds its
// entries.
func (l *lowerer) bodyOwns(list []syntax.Stmt) bool {
	found := false
	for _, s := range list {
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.CallExpr:
				if c := l.info.Calls[n]; n.Trailing != nil && (c == nil || c.Kind != check.CallWith) {
					syntax.Inspect(n.Fun, func(m syntax.Node) bool { found = found || l.ownsNode(m); return !found })
					for _, a := range n.Args {
						syntax.Inspect(a, func(m syntax.Node) bool { found = found || l.ownsNode(m); return !found })
					}
					found = found || l.ownsNode(n)
					return false
				}
			}
			found = found || l.ownsNode(n)
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// ownsNode reports a binding or temporary with an owner.
func (l *lowerer) ownsNode(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.Ident:
		v, ok := l.info.Defs[n].(*check.Var)
		return ok && l.info.OwnedVars[v] != nil
	case syntax.Expr:
		return l.info.OwnedTemps[n] != nil
	}
	return false
}

// ownsValues reports a declaration that owns a value anywhere in it, its
// parameters and nested literals included: its frames release through a
// frame scope on their task, so it is no pure function (pure.go).
func ownsValues(info *check.Info, d *syntax.FuncDecl) bool {
	found := false
	syntax.Inspect(d, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Ident:
			if v, ok := info.Defs[n].(*check.Var); ok && info.OwnedVars[v] != nil {
				found = true
			}
		case syntax.Expr:
			if info.OwnedTemps[n] != nil {
				found = true
			}
		}
		return !found
	})
	return found
}

// ownsParams reports a signature with a taken parameter that has an owner.
func (l *lowerer) ownsParams(params []*check.Var) bool {
	for _, v := range params {
		if l.info.OwnedVars[v] != nil {
			return true
		}
	}
	return false
}

// resultRelease is the hidden v1.Releaser of a std call (L8 B4, Gap 47):
// the release of the call's result, which the runtime body runs when it
// drops a successful result, or nil when the checker recorded none (the
// result holds no resource). It runs on the releasing task: a copy of the
// environment with that task, as a std callback's (childEnv).
func (fc *funcCtx) resultRelease(x *syntax.CallExpr, site syntax.Node) string {
	d := fc.l.info.ResultDrops[x]
	if d == nil {
		return "nil"
	}
	return fc.envClosure(childEnv, "e", site, func(env string) ([]string, string, string) {
		// env is &copy; a release that passes the task selects through it.
		env = "(" + env + ")"
		v := fc.scope.alloc("v")
		var text string
		if d.Glue() {
			drop := fc.runtimeCall("NewDrop", fc.ownBinding("NewDrop", site), "", "", nil, env, nil, nil, site)
			text = fc.glueOn(d, v, "", env, drop, site)
		} else {
			text = fc.providerRelease(&check.Acquisition{Close: d.Close}, env, v, site)
		}
		return []string{v + " " + fc.goType(d.Type)}, "", strings.Repeat("\t", fc.depth+1) + text + "\n"
	})
}
