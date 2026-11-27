package lower

import (
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The slot variant (UNI2; IR-G plan §4). A union of the Go interface form
// boxes each value its function returns: the variant goes to the heap
// when the interface is made, because Go's escape analysis cannot see
// that the caller only matches it. ssg's lookup made a third of the
// program's objects that way, each matched once and dropped.
//
// A call whose result the caller only matches goes instead to the
// callee's slot variant: the same body lowered once more with a pointer
// to one slot per variant after its parameters, whose returns write the
// variant into its slot and return its tag:
//
//		func lookupSlot(env *Env, …, s0 *Text, s1 *Record, s2 *List) (uint8, error)
//
//	  - a variant literal, or any value of a variant's type, is stored in
//	    that variant's slot;
//	  - a scalar local of the union (scalar.go) is copied from its slot by
//	    its tag;
//	  - a call of a function with a slot variant that returns the same union
//	    passes the pointers on (return f() hands its slots down);
//	  - any other value, already a box, is taken apart by a type switch
//	    into the slots (scattered): the box it came in is not copied.
//
// A failure writes no slot and returns tag 0 with the error. The slots
// are the caller's own scalar local (scalar.go): Go variables declared at
// the site, whose addresses only the call takes, so Go's escape analysis
// keeps them in the caller's frame. Four kinds of site pass them:
//
//   - a join local, `let x = [try] f()` that a match takes as its
//     subject, when f is not inlined and every read of x is a match's
//     subject or a return's value (no read gathers a box: slotGathered);
//   - the subject of a simple match, `match [try] f() {…}`;
//   - the first value of a loop-carried scalar local, and each plain
//     assignment of one: the call writes its slots and its tag.
//   - the value of a return in a slot variant (pass-through, above).
//
// The proof is place liveness, as OWN1's is for a slice: a slot is live
// from its call to the last read of its local, it is declared at its site
// (one slot set per site, so two live results never share one), and every
// assignment of a loop-carried local writes its tag again (so a later
// iteration never reads an earlier one's variant). A result stored,
// appended, passed or captured keeps the box: a gathered local is no
// join local here, and a captured one is no scalar local at all.
//
// The callee qualifies when it is a plain Ergo function of the caller's
// package: not a method, no Go type parameters (MSB1's generic unions are
// never boxed here), no owned parameters, no frame scope, not a Close, not
// derived; its one result a union of the interface form (not flat) with at
// most 8 variants. The reference is slotLowering "off", set by a test or by
// a build with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.slotLowering=off;
// a test build takes the box (shortcuts.go). The receipt is Taken
// ("uni-slot") and SlotKept, the calls of such a callee that keep the box.
//
// Prior art: Rust and C++ return a large value through a pointer to the
// caller's slot (the sret ABI, guaranteed copy elision); GHC's
// constructed product result analysis and worker/wrapper return a sum's
// fields unboxed when every caller scrutinises it at once; MLton and
// OCaml's flambda unbox a variant a known caller matches. Gleam, Elixir
// and ReScript return a heap value (a tuple, a JavaScript object) and
// leave its lifetime to the host's allocator.
var slotLowering string

// slotSite is a call that may write the slots of sl: took once the call
// was written as a call of the slot variant.
type slotSite struct {
	sl   *scalarLocal
	took bool
}

// slotVariantKey is the package-scope key of a function's slot variant.
func slotVariantKey(g *check.Func) pkgKey { return pkgKey{"slot", g} }

// slotShaped reports a function whose one result is a union of at most 8
// variants, with an Ergo body and no Go type parameters.
func (l *lowerer) slotShaped(f *check.Func) bool {
	if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || len(f.Sig.Results) != 1 || f.Name == "main" {
		return false
	}
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			return false
		}
	}
	n, ok := f.Sig.Results[0].(*check.Named)
	if !ok {
		return false
	}
	u, ok := n.Underlying().(*check.Union)
	return ok && len(u.Variants) > 0 && len(u.Variants) <= 8
}

// reserveSlots gives, in the naming pass, the name of the slot variant of
// each function of pkgs that may have one.
func (l *lowerer) reserveSlots(pkgs []*check.Package) {
	if slotLowering == "off" {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if !l.slotShaped(f) {
				continue
			}
			if td, _ := l.methodReceiver(f); td == nil {
				l.pkgName(slotVariantKey(f), f.Name+"Slot")
			}
		}
	}
}

// slotCallee reports whether a call of f, of declaration fd, from a body
// of f's package, may go to f's slot variant (the rule above).
func (l *lowerer) slotCallee(f *check.Func, fd *funcDecl) bool {
	if fd.slotOK != 0 {
		return fd.slotOK == 1
	}
	ok := slotLowering != "off" && l.slotShaped(f) && fd.extern == "" && fd.method == nil &&
		!isClose(f) && f.Pkg.Info != nil &&
		len(witnessParams(f.Sig)) == 0 && len(l.ownedParams(f)) == 0 &&
		!l.ownsParams(paramVars(f)) && !l.bodyNeedsFrame(f.Decl.Body.List) &&
		l.flatUnionOf(f.Sig.Results[0]) == nil
	fd.slotOK = 2
	if ok {
		fd.slotOK = 1
	}
	return ok
}

// slotOffer offers the call to write the slots of sl, unless a read of
// the local gathers its value (gathered); nil when there is no call.
func (fc *funcCtx) slotOffer(call *syntax.CallExpr, sl *scalarLocal, gathered bool) *slotSite {
	if call == nil || sl == nil || gathered || slotLowering == "off" {
		return nil
	}
	if fc.slotAt == nil {
		fc.slotAt = map[*syntax.CallExpr]*slotSite{}
	}
	site := &slotSite{sl: sl}
	fc.slotAt[call] = site
	return site
}

// slotTaken ends the offer of site at call, whose value val is, when it
// took the slot variant, the tag: it writes the tag of the local and
// reports true.
func (fc *funcCtx) slotTaken(call *syntax.CallExpr, site *slotSite, val string) bool {
	if site == nil {
		return false
	}
	delete(fc.slotAt, call)
	if !site.took {
		return false
	}
	fc.line(site.sl.name + " = " + val)
	if site.sl.box != "" {
		fc.line(site.sl.box + " = nil")
	}
	return true
}

// slotAdmit is the site of the call x of c.Func, of declaration fd, that
// goes to the slot variant: offered, its callee qualifying, its result the
// site's union. It declares the site's slots. A qualifying callee's call
// that keeps the box is counted (SlotKept).
func (fc *funcCtx) slotAdmit(x *syntax.CallExpr, c *check.Call, fd *funcDecl) *slotSite {
	if c.Func.Pkg != fc.l.bodyPkg() || fc.goGeneric || !fc.l.slotCallee(c.Func, fd) {
		return nil
	}
	site := fc.slotAt[x]
	if site == nil || site.took || fc.goType(c.Func.Sig.Results[0]) != site.sl.union || !fc.l.takes("uni-slot") {
		fc.l.stats.SlotKept++
		return nil
	}
	fc.l.took("uni-slot")
	if fc.l.stats.SlotSites == nil {
		fc.l.stats.SlotSites = map[string]int{}
	}
	fc.l.stats.SlotSites[c.Func.Name]++
	site.took = true
	if !site.sl.declared {
		fc.scalarDeclare(site.sl, site.sl.name)
	}
	return site
}

// slotName is the Go name of g's slot variant, queued for emission once.
func (l *lowerer) slotName(g *check.Func, gd *funcDecl) string {
	if gd.slot != "" {
		return gd.slot
	}
	gd.slot = l.pkgName(slotVariantKey(g), g.Name+"Slot")
	l.pending = append(l.pending, func() {
		gd.emitSlot = true
		defer func() { gd.emitSlot = false }()
		l.emitBody(gd, false, nil, false)
	})
	return gd.slot
}

// slotArgs are the slot pointers of sl, by variant.
func slotArgs(sl *scalarLocal) []string {
	out := make([]string, len(sl.slots))
	for k, s := range sl.slots {
		if p, ok := strings.CutPrefix(s, "*"); ok {
			out[k] = p // a slot variant's own pointer, passed on
		} else {
			out[k] = "&" + s
		}
	}
	return out
}

// slotParams declares the slot pointers of f's slot variant, appended to
// parts, and is the frame's slot destination.
func (fc *funcCtx) slotParams(f *check.Func, parts *[]string) *scalarLocal {
	sl := fc.unionShape(f.Sig.Results[0])
	if sl == nil {
		ice(f.Decl.Name, "the slot variant of %s has no union result of the interface form", f.Name)
	}
	for k, g := range sl.variants {
		p := fc.scope.alloc("s" + strconv.Itoa(k))
		*parts = append(*parts, p+" *"+g)
		sl.slots = append(sl.slots, "*"+p)
	}
	sl.declared = true
	return sl
}

// slotReturn writes the return of x from a slot variant whose slots are
// out (the rule above).
func (fc *funcCtx) slotReturn(out *scalarLocal, x syntax.Expr) {
	ret := func(tag string) {
		if fc.frame.fallible {
			fc.sourceReturn(tag, "nil")
		} else {
			fc.sourceReturn(tag)
		}
	}
	if fc.fallibleExpr(x) {
		// return f(), f fallible: its tag or value and its failure.
		call := joinCall(x)
		site := fc.slotOffer(call, out, false)
		o := fc.expr(x)
		if site != nil {
			delete(fc.slotAt, call)
			if site.took {
				fc.sourceReturn(o.val, o.err)
				return
			}
		}
		v, e := fc.temp("v"), fc.temp("e")
		fc.line(v + ", " + e + " := " + fc.convertAt(x, o.val) + ", " + o.err)
		fc.line("if " + e + " != nil {")
		fc.line("\treturn 0, " + e)
		fc.line("}")
		fc.slotScatter(out, v, ret)
		return
	}
	if src := fc.scalarIdent(x); src != nil && slices.Equal(src.variants, out.variants) {
		// A scalar local: its slot by its tag.
		fc.line("switch " + src.name + " {")
		for k := range out.variants {
			fc.line("case " + strconv.Itoa(k) + ":")
			fc.depth++
			fc.line(out.slots[k] + " = " + src.slots[k])
			ret(strconv.Itoa(k))
			fc.depth--
		}
		fc.line("}")
		fc.line("panic(\"ergo: defect: a value of " + out.union + " that is none of its variants\")")
		return
	}
	k := slices.Index(out.variants, fc.goType(fc.typeOf(x)))
	var call *syntax.CallExpr
	var site *slotSite
	if k < 0 {
		call = joinCall(x)
		site = fc.slotOffer(call, out, false)
	}
	if id, ok := unparen(x).(*syntax.Ident); ok {
		fc.returned = id
	}
	val := fc.assigned(x)
	fc.returned = nil
	if site != nil {
		delete(fc.slotAt, call)
		if site.took {
			ret(val) // the callee wrote the slots
			return
		}
	}
	val = fc.returnedTemp(check.ReturnedKey(x), val)
	if k >= 0 {
		fc.line(out.slots[k] + " = " + val)
		ret(strconv.Itoa(k))
		return
	}
	fc.slotScatter(out, val, ret)
}

// slotScatter writes val, a union value, into the slots of out by a type
// switch, each case returning its tag.
func (fc *funcCtx) slotScatter(out *scalarLocal, val string, ret func(string)) {
	x := fc.temp("x")
	fc.line("switch " + x + " := " + out.union + "(" + val + ").(type) {")
	for k, g := range out.variants {
		fc.line("case " + g + ":")
		fc.depth++
		fc.line(out.slots[k] + " = " + x)
		ret(strconv.Itoa(k))
		fc.depth--
	}
	fc.line("}")
	fc.line("panic(\"ergo: defect: a value of " + out.union + " that is none of its variants\")")
}

// matchSubject is the Go subject of a simple match on g: the tag of the
// slots its call wrote (slotTemps), or its value.
func (fc *funcCtx) matchSubject(g *syntax.TypeGuard, km *knownMatch) string {
	call := joinCall(g.X)
	if call == nil || km != nil || fc.goGeneric || len(fc.l.info.Releases[g]) > 0 || len(fc.l.info.Releases[g.X]) > 0 {
		return fc.headerValue(g.X)
	}
	sl := fc.unionShape(fc.typeOf(g.X))
	if sl == nil {
		return fc.headerValue(g.X)
	}
	sl.name = "m"
	site := fc.slotOffer(call, sl, false)
	val := fc.headerValue(g.X)
	if !fc.slotTaken(call, site, val) {
		return val
	}
	fc.slotTemps = append(fc.slotTemps, sl)
	return sl.name
}
