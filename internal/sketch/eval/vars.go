package eval

import "ergo.local/prototype/internal/sketch/check"

// varTable is the slots of a frame's locals, and of the literals made in
// it, which share it (ORACLE O29, O32). A frame binds few locals, so the
// table is two flat arrays found by a scan, newest first: the first
// inlineVars in the table itself, the rest in slices it grows. A Go map
// per frame was a quarter of the evaluator's allocation, and its
// assignments a twentieth of its instructions.
//
// A local's first slot is the table's own storage: binding a parameter
// or a let allocates nothing. Binding a bound local again (a loop's
// variable, a let in a loop) makes a new slot, as before, since a borrow
// or a range may still hold the old one.
type varTable struct {
	n     int
	keys  [inlineVars]*check.Var
	vals  [inlineVars]*slot
	store [inlineVars]slot
	// moreKeys and moreVals are the locals past the first inlineVars.
	moreKeys []*check.Var
	moreVals []*slot
	// held is set once a function literal holds the table's slots
	// (snapshot): binding a bound local again then makes a new slot, so
	// the literal keeps the binding it saw.
	held bool
}

const inlineVars = 4

func newVars() *varTable { return &varTable{} }

// get is v's slot.
func (t *varTable) get(v *check.Var) (*slot, bool) {
	if i := t.find(v); i >= 0 {
		return t.at(i), true
	}
	return nil, false
}

// find is v's index, -1 when v is not bound.
func (t *varTable) find(v *check.Var) int {
	for i := len(t.moreKeys) - 1; i >= 0; i-- {
		if t.moreKeys[i] == v {
			return inlineVars + i
		}
	}
	for i := t.n - 1; i >= 0; i-- {
		if t.keys[i] == v {
			return i
		}
	}
	return -1
}

func (t *varTable) at(i int) *slot {
	if i < inlineVars {
		return t.vals[i]
	}
	return t.moreVals[i-inlineVars]
}

func (t *varTable) put(i int, s *slot) {
	if i < inlineVars {
		t.vals[i] = s
		return
	}
	t.moreVals[i-inlineVars] = s
}

// keyAt is the variable at index i, nil past the table.
func (t *varTable) keyAt(i int) *check.Var {
	if i < t.n {
		return t.keys[i]
	}
	if j := i - inlineVars; j >= 0 && j < len(t.moreKeys) {
		return t.moreKeys[j]
	}
	return nil
}

// getAt is get from a compiled form, which keeps in *hint the index it
// found v at (ORACLE O38). A body binds its locals in the same order at
// each call, so the hint is almost always v's index: one compare, no
// scan. A table holds a variable once (bind and set reuse its index), so
// a hint that names v names its only entry.
func (t *varTable) getAt(v *check.Var, hint *int32) (*slot, bool) {
	if i := int(*hint); t.keyAt(i) == v {
		return t.at(i), true
	}
	i := t.find(v)
	if i < 0 {
		return nil, false
	}
	*hint = int32(i)
	return t.at(i), true
}

// bindAt is bind from a compiled form, with getAt's hint.
func (t *varTable) bindAt(v *check.Var, val Value, hint *int32) {
	if i := int(*hint); t.keyAt(i) == v {
		*t.at(i) = slot{v: val}
		return
	}
	if i := t.find(v); i >= 0 {
		*t.at(i) = slot{v: val} // as bind: the old binding has ended
		*hint = int32(i)
		return
	}
	*hint = int32(t.bindNew(v, val))
}

// bind binds v to a new slot holding val.
func (t *varTable) bind(v *check.Var, val Value) {
	if i := t.find(v); i >= 0 {
		// Bound again (a loop's variable, a let in a loop): the old
		// binding's life has ended, and nothing holds its slot past it (a
		// borrow is never stored, a range ends with its loop, a literal
		// shares the table, a task copies it), so the slot is reused.
		if t.held {
			t.put(i, &slot{v: val})
			return
		}
		*t.at(i) = slot{v: val}
		return
	}
	t.bindNew(v, val)
}

// bindNew binds v, which t does not hold, to a new slot holding val, and
// is its index.
func (t *varTable) bindNew(v *check.Var, val Value) int {
	if t.n < inlineVars {
		s := &t.store[t.n]
		s.v = val
		t.keys[t.n], t.vals[t.n] = v, s
		t.n++
		return t.n - 1
	}
	t.moreKeys = append(t.moreKeys, v)
	t.moreVals = append(t.moreVals, &slot{v: val})
	return inlineVars + len(t.moreKeys) - 1
}

// set makes s v's slot.
func (t *varTable) set(v *check.Var, s *slot) {
	if i := t.find(v); i >= 0 {
		t.put(i, s)
		return
	}
	if t.n < inlineVars {
		t.keys[t.n], t.vals[t.n] = v, s
		t.n++
		return
	}
	t.moreKeys = append(t.moreKeys, v)
	t.moreVals = append(t.moreVals, s)
}

// snapshot is a table of t's variables and their slots, for a function
// literal made now: it reads and writes the same slots (a let mut it
// assigns), but a binding t makes later, such as the next iteration of a
// loop's variable, is a new slot it does not see, as in the lowered Go,
// whose loop variables are per iteration.
func (t *varTable) snapshot() *varTable {
	t.held = true
	s := newVars()
	t.each(func(v *check.Var, sl *slot) { s.set(v, sl) })
	s.held = true
	return s
}

// each calls f with every variable and its slot.
func (t *varTable) each(f func(v *check.Var, s *slot)) {
	for i := range t.n {
		f(t.keys[i], t.vals[i])
	}
	for i, v := range t.moreKeys {
		f(v, t.moreVals[i])
	}
}

// size is how many variables t binds.
func (t *varTable) size() int { return t.n + len(t.moreKeys) }
