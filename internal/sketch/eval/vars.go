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

// bind binds v to a new slot holding val.
func (t *varTable) bind(v *check.Var, val Value) {
	if i := t.find(v); i >= 0 {
		t.put(i, &slot{v: val})
		return
	}
	if t.n < inlineVars {
		s := &t.store[t.n]
		s.v = val
		t.keys[t.n], t.vals[t.n] = v, s
		t.n++
		return
	}
	t.moreKeys = append(t.moreKeys, v)
	t.moreVals = append(t.moreVals, &slot{v: val})
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
