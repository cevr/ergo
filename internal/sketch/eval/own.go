package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Owned values (ORACLE O3; resources.md R5-R10, R21, R39, R41, R43; #197
// L8 B1, B2). The checker gives each owned local, taken parameter and
// temporary its release (Info.OwnedVars, Info.OwnedTemps) and records the
// plan of where each is released (Info.Releases), each hand-off
// (Info.Handoffs, PartHands, PatternOwners) and each field taken in place
// (FieldTakes). The evaluator reads those facts as the plain lowering
// does, and keeps its own list per owner: a frame (a function, a literal,
// a task's block, an obligation's body), or an owning with inside it. A
// value is registered in the step that receives it (R8); its entry's
// presence is its drop flag (R21): a hand-off removes the entry, a scope
// that ends inside the frame releases what the plan names there, and the
// frame's end releases the rest on every exit, last registered first,
// each after an earlier one's defect, the first defect kept (R9). A value
// the frame returns stays until the exit is known (R10): it leaves with a
// returned value and is released when the exit fails.
//
// The lists are the evaluator's own: they mirror runtime/v1's Owning,
// Hand, Returned and Owned.Release by the rules above, not by its code.

// owned is one entry of an owner's list: a value and its release, or an
// exit action (scope.OnExit).
type owned struct {
	v     Value
	d     *check.Drop
	caps  *capEnv // the capabilities in force where it was registered
	at    syntax.Node
	taken uint64 // the record's fields taken in place (R39)
	live  bool
	// act is an exit action's block: it runs as a frame of its own, in
	// the environment it was registered in (runtime.md, Exit actions).
	act *closure
}

// owner is one owner's list, oldest first.
type owner struct{ list []*owned }

// ownerOf is fr's innermost owner, made on first use.
func (fr *frame) ownerOf() *owner {
	if fr.own == nil {
		fr.frameOwn = &owner{}
		fr.own = fr.frameOwn
	}
	return fr.own
}

// lookupOwned is the entry registered under key in fr, or in the frame a
// literal was made in (a capture's entry is its home frame's).
func (fr *frame) lookupOwned(key any) *owned {
	for f := fr; f != nil; f = f.outer {
		if e := f.owned[key]; e != nil {
			return e
		}
	}
	return nil
}

// register adds v, released by d, as the newest entry of fr's innermost
// owner; key names it for the plan, nil for none.
func (in *interp) register(fr *frame, key any, v Value, d *check.Drop, at syntax.Node) *owned {
	e := &owned{v: v, d: d, caps: fr.caps, at: at, live: true}
	o := fr.ownerOf()
	o.list = append(o.list, e)
	if key != nil {
		if fr.owned == nil {
			fr.owned = map[any]*owned{}
		}
		fr.owned[key] = e
	}
	return e
}

// ownVar registers the local or parameter v, just bound, when the checker
// gives it an owner.
func (in *interp) ownVar(fr *frame, v *check.Var, at syntax.Node) {
	if v == nil || len(fr.info.OwnedVars) == 0 {
		return
	}
	d := fr.info.OwnedVars[v]
	if d == nil {
		return
	}
	s, _ := fr.vars.get(v)
	if s == nil {
		ice("the owned local %s has no slot", v.Name)
	}
	in.register(fr, v, s.v, d, at)
}

// ownPattern registers each name a let or for pattern bound.
func (in *interp) ownPattern(fr *frame, p *check.Pattern, at syntax.Node) {
	if p == nil || len(fr.info.OwnedVars) == 0 {
		return
	}
	switch p.Kind {
	case check.PatBind:
		in.ownVar(fr, p.Var, at)
	case check.PatAs:
		for _, e := range p.Elems {
			in.ownPattern(fr, e, at)
		}
		in.ownVar(fr, p.Var, at)
	case check.PatRecord:
		for _, f := range p.Fields {
			in.ownPattern(fr, f.Pattern, at)
		}
	case check.PatTuple:
		for _, e := range p.Elems {
			in.ownPattern(fr, e, at)
		}
	}
}

// ownTemp registers v, the value of x, when x is a temporary its
// statement owns (R43).
func (in *interp) ownTemp(fr *frame, x syntax.Expr, v Value) {
	if len(fr.info.OwnedTemps) == 0 {
		return
	}
	if d := fr.info.OwnedTemps[x]; d != nil {
		in.register(fr, x, v, d, x)
	}
}

// hand removes key's entry: its value moved to a new owner or was taken.
func (fr *frame) hand(key any) {
	if e := fr.lookupOwned(key); e != nil {
		e.live = false
	}
}

// keep moves key's entry to the frame's kept values: the frame returns it
// (R10).
func (fr *frame) keep(key any) {
	if e := fr.lookupOwned(key); e != nil && e.live {
		e.live = false
		fr.kept = append(fr.kept, e)
	}
}

// handoff ends or keeps the entry of the owned local id uses, when the
// use is a hand-off (Info.Handoffs): a take or a move, or the frame's
// returned value. A use a taking std call holds settles after the call
// (settleHeld).
func (in *interp) handoff(fr *frame, id *syntax.Ident) {
	if len(fr.info.Handoffs) == 0 {
		return
	}
	v := fr.info.Handoffs[id]
	if v == nil || fr.held[id] {
		return
	}
	if id == fr.returned {
		fr.keep(v)
		return
	}
	fr.hand(v)
}

// handParts ends the statement's ownership of the operands in list that
// the value being built now holds (Info.PartHands): the value owns them
// from here (R43).
func (in *interp) handParts(fr *frame, list ...syntax.Expr) {
	if len(fr.info.PartHands) == 0 {
		return
	}
	for _, x := range list {
		if kv, ok := x.(*syntax.KeyValueExpr); ok {
			in.handParts(fr, kv.Key, kv.Value)
			continue
		}
		if x != nil && fr.info.PartHands[x] {
			fr.hand(x)
		}
	}
}

// takeField marks the field a call takes in place on its owned record's
// entry (Info.FieldTakes): the record's release skips it from here.
func (in *interp) takeField(fr *frame, x *syntax.SelectorExpr) {
	if len(fr.info.FieldTakes) == 0 {
		return
	}
	ft, ok := fr.info.FieldTakes[x]
	if !ok || ft.Index >= 64 {
		return
	}
	if e := fr.lookupOwned(ft.Var); e != nil && e.live {
		e.taken |= 1 << ft.Index
	}
}

// ownReplaced registers each field of the base of the update or spread x
// that the value built does not copy (Info.Replaced): the statement owns
// it and releases it at its end (R43).
func (in *interp) ownReplaced(fr *frame, x *syntax.CompositeLit, base *record) {
	rs := fr.info.Replaced[x]
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		d := fr.info.OwnedTemps[r.Temp]
		if d == nil {
			continue
		}
		in.register(fr, r.Temp, base.fields[fieldIndex(base.t, r.Field.Name)], d, x)
	}
}

// patternOwners is the selected arm's ownership step (Info.PatternOwners),
// after its pattern matched and before its body: the sources move into
// the arm's bindings, which are registered in written order.
func (in *interp) patternOwners(fr *frame, cl *syntax.CaseClause) {
	if len(fr.info.PatternOwners) == 0 {
		return
	}
	plan, ok := fr.info.PatternOwners[cl]
	if !ok {
		return
	}
	for _, src := range plan.Sources {
		if src.Var != nil {
			fr.hand(src.Var)
		} else {
			fr.hand(src.Temp)
		}
	}
	for _, v := range plan.Bindings {
		in.ownVar(fr, v, cl)
	}
}

// releaseAt releases what the plan records at key, a place that ends
// inside the frame, in the plan's order. A value whose entry left (a
// hand-off on this path) is skipped (R21). A defect is the place's
// outcome at once; the frame's end releases the rest (R9).
func (in *interp) releaseAt(fr *frame, key syntax.Node) *abrupt {
	if len(fr.info.Releases) == 0 || key == nil {
		return nil
	}
	for _, r := range in.sx(fr, key).releases {
		var e *owned
		if r.Var != nil {
			e = fr.lookupOwned(r.Var)
		} else {
			e = fr.lookupOwned(r.Temp)
		}
		if e == nil || !e.live {
			continue
		}
		if ab := in.release(fr, e); ab != nil {
			return ab
		}
	}
	return nil
}

// release runs e's release now, masked (runtime.md, I2), with the
// capabilities in force where e was registered.
func (in *interp) release(fr *frame, e *owned) *abrupt {
	e.live = false
	saved := fr.caps
	fr.caps = e.caps
	defer func() { fr.caps = saved }()
	in.mask(1)
	defer in.mask(-1)
	if e.act != nil {
		return in.runAction(fr, e)
	}
	return in.drop(fr, e.at, e.v, e.d, e.taken)
}

// releaseOwner releases o's live entries, last first, each after an
// earlier one's defect, and joins their outcome with ab, the owner's own
// exit: a release's defect replaces any outcome but a defect (R9, R25).
func (in *interp) releaseOwner(fr *frame, o *owner, ab *abrupt) *abrupt {
	if o == nil || stops(ab) {
		return ab
	}
	for i := len(o.list) - 1; i >= 0; i-- {
		e := o.list[i]
		if !e.live {
			continue
		}
		rel := in.release(fr, e)
		switch {
		case rel == nil:
		case stops(rel):
			return rel
		case rel.kind != ctlDefect:
			ice("a release completed with %d", rel.kind)
		case ab == nil || ab.kind != ctlDefect:
			ab = rel
		}
	}
	o.list = nil
	return ab
}

// endFrame ends fr with the exit ab: it releases what the frame still
// owns, then settles the values it returned (R10): on a successful exit
// they leave with the value, on any other each is released, last first.
func (in *interp) endFrame(fr *frame, ab *abrupt) *abrupt {
	if fr.frameOwn == nil && len(fr.kept) == 0 {
		return ab
	}
	if stops(ab) {
		return ab
	}
	ab = in.releaseOwner(fr, fr.frameOwn, ab)
	if stops(ab) {
		return ab
	}
	kept := fr.kept
	fr.kept = nil
	if ab == nil || ab.kind == ctlReturn {
		return ab
	}
	return in.releaseOwner(fr, &owner{list: kept}, ab)
}

// stops reports an exit that ends the obligation without a verdict of
// its own (an exhausted budget, a form the evaluator does not run, a
// scheduler's teardown): nothing more runs, no release included.
func stops(ab *abrupt) bool {
	return ab != nil && (ab.kind == ctlExhausted || ab.kind == ctlUnsupported || ab.kind == ctlAbort)
}

// settleHeld ends the entries of the owned locals a taking std call held
// (holdTaken): on success their values left with the call; on the call's
// typed failure each is released now, in the call's own step; on any
// other exit each stays for the frame's end (Q7, plan 1.4, D-C).
func (in *interp) settleHeld(fr *frame, held []*syntax.Ident, ab *abrupt) *abrupt {
	for _, id := range held {
		delete(fr.held, id)
	}
	for _, id := range held {
		v := fr.info.Handoffs[id]
		switch {
		case ab == nil:
			fr.hand(v)
		case ab.kind == ctlFail:
			if e := fr.lookupOwned(v); e != nil && e.live {
				if rel := in.release(fr, e); rel != nil {
					return rel
				}
			}
		}
	}
	return ab
}

// holdTaken lists the owned locals that args, the operands of a bodiless
// std call taking the operands at takes, hand off: their entries stay
// until the call returns (settleHeld).
func (in *interp) holdTaken(fr *frame, args []syntax.Expr, takes []int) []*syntax.Ident {
	if len(fr.info.Handoffs) == 0 {
		return nil
	}
	var held []*syntax.Ident
	for _, i := range takes {
		if i >= len(args) {
			continue
		}
		id, ok := unparen(args[i]).(*syntax.Ident)
		if !ok || fr.info.Handoffs[id] == nil {
			continue
		}
		if fr.held == nil {
			fr.held = map[*syntax.Ident]bool{}
		}
		fr.held[id] = true
		held = append(held, id)
	}
	return held
}

// reassignOwned settles v = val for v an owned local (R41): the new value
// is registered first, then the old one is released where the plan says
// it is still owned, then v's entry is the new one, and the caller stores
// val. A defect in the old value's release leaves the new one owned by
// the frame and v unchanged.
func (in *interp) reassignOwned(fr *frame, id *syntax.Ident, val Value) (bool, *abrupt) {
	if len(fr.info.OwnedVars) == 0 {
		return false, nil
	}
	v, _ := fr.info.Uses[id].(*check.Var)
	d := fr.info.OwnedVars[v]
	if d == nil {
		return false, nil
	}
	e := in.register(fr, nil, val, d, id)
	if ab := in.releaseAt(fr, id); ab != nil {
		return true, ab
	}
	if fr.owned == nil {
		fr.owned = map[any]*owned{}
	}
	fr.owned[v] = e
	return true, nil
}
