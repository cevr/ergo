package eval

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// funcPlan is what every call of a declared function reads about the
// function, resolved once at its first call (ORACLE O34): how it runs (an
// Ergo body, the evaluator's native, or neither), its parameters, the
// parameters its package owns, its body's fact owner and fact table. A
// call read each of them per call: two Go maps (paramsOf, ownerOf), a
// string join and a map for the native, and an OwnedVars lookup per
// parameter. A call site keeps its callee's plan in its facts.
type funcPlan struct {
	fn *check.Func
	// ergo says the body runs as a frame: an Ergo body that is no extern
	// and no Go package's.
	ergo bool
	// nat is the native that runs the call when ergo is not set: the
	// evaluator's own for a std function, else a Go extern's; nil when
	// neither runs it (a skip).
	nat native
	// params are the parameters in order (nil for an unnamed one), and
	// owned the indexes of those the package owns (Info.OwnedVars).
	params []*check.Var
	owned  []int
	owner  check.FactOwner
	tab    factTable
	// collection is a prelude collection call (collectionCall); takes a
	// bodiless std function that takes an owned argument (holdTaken).
	collection, takes bool
	// appendsString is Go's append of a string to a []byte
	// (check.AppendsString), which the evaluator runs itself.
	appendsString bool
	// body is the body's compiled form (compile.go), made at the second
	// call outside a generic body; bodyForm is its state.
	body     fastBody
	bodyForm uint8
	// readAt is the obligation (readSet.epoch) that last entered the
	// function's package through this plan (reads.go).
	readAt uint64
	// instances are a generic function's instances by the key of their
	// type arguments (instanceOf).
	instances map[string]*planInstance
}

// planInstance is one instance of a generic function (ORACLE O38): its own
// fact table, so the forms compiled in it read its type arguments, and
// its body's form.
type planInstance struct {
	tab      factTable
	body     fastBody
	bodyForm uint8
}

// instanceOf is the instance of p's function for targs, or nil when a type
// argument has no exact key (instanceKey): such a call is walked.
func (in *interp) instanceOf(p *funcPlan, targs []rtype) *planInstance {
	var b strings.Builder
	for i, t := range targs {
		if !keyable(t) {
			return nil
		}
		if i > 0 {
			b.WriteByte(';')
		}
		writeKey(&b, t)
	}
	k := b.String()
	if inst := p.instances[k]; inst != nil {
		return inst
	}
	if p.instances == nil {
		p.instances = map[string]*planInstance{}
	}
	tab := newNodeTable()
	tab.instance = true
	inst := &planInstance{tab: tab}
	p.instances[k] = inst
	return inst
}

// keyable reports whether writeKey names t exactly: built of basic and
// named types, slices, arrays, maps and tuples. A function type's key
// leaves out its rows, a union's leaves out whether it has a row (an
// error value's test reads it), and any other type is its unresolved
// text.
func keyable(t rtype) bool {
	t = t.env.resolve(t.t)
	switch tt := t.t.(type) {
	case *check.Basic:
		return true
	case *check.Named:
		for _, a := range tt.TArgs {
			if !keyable(rtype{a, t.env}) {
				return false
			}
		}
		return true
	case *check.Slice:
		return keyable(rtype{tt.Elem, t.env})
	case *check.Array:
		return keyable(rtype{tt.Elem, t.env})
	case *check.Map:
		return keyable(rtype{tt.Key, t.env}) && keyable(rtype{tt.Elem, t.env})
	case *check.Tuple:
		for _, e := range tt.Types {
			if !keyable(rtype{e, t.env}) {
				return false
			}
		}
		return true
	}
	return false
}

// planOf is fn's plan, made at its first call.
func (in *interp) planOf(fn *check.Func) *funcPlan {
	if p, ok := in.plans[fn]; ok {
		return p
	}
	p := &funcPlan{fn: fn, collection: preludeCollection(fn), appendsString: check.AppendsString(fn)}
	p.ergo = fn.Decl != nil && fn.Decl.Body != nil && fn.Decl.Extern == nil && !fn.Pkg.Host()
	p.takes = fn.Decl != nil && fn.Decl.Extern != nil && fn.Pkg.Std() && len(fn.Sig.Takes) > 0
	if !p.ergo {
		if nat := nativeOf(fn); nat != nil && !fn.Pkg.Host() {
			p.nat = nat
		} else {
			p.nat = goNativeOf(fn)
		}
		if src, ok := check.ModuleBodySource(fn); ok && p.nat == nil && in.goBody(fn) {
			p.nat = in.goBodyNative(src.Target)
		}
	} else {
		if fn.Pkg.Info == nil {
			ice("the package of %s has no Info", fn.Name)
		}
		p.params = in.paramsOf(fn)
		p.owner = in.ownerOf(fn.Pkg, fn.Decl)
		p.tab = in.table(fn.Pkg.Info)
		if owned := fn.Pkg.Info.OwnedVars; len(owned) > 0 {
			for i, v := range p.params {
				if v != nil && owned[v] != nil {
					p.owned = append(p.owned, i)
				}
			}
		}
	}
	if in.plans == nil {
		in.plans = map[*check.Func]*funcPlan{}
	}
	in.plans[fn] = p
	return p
}

// planAt is the plan of the function the call whose facts are xf calls,
// kept in the facts.
func (in *interp) planAt(xf *facts, fn *check.Func) *funcPlan {
	if p := xf.plan; p != nil && p.fn == fn {
		return p
	}
	p := in.planOf(fn)
	xf.plan = p
	return p
}
