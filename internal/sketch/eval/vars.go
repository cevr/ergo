package eval

import "ergo.local/prototype/internal/sketch/check"

// varTable is the slots of a frame's locals, and of the literals made in
// it, which share it (ORACLE O29). Most frames bind a few locals, so the
// first ones live in the table itself and are found by a scan, newest
// first; past that the table keeps a map. A Go map per frame was a
// quarter of the evaluator's allocation.
type varTable struct {
	n    int
	keys [inlineVars]*check.Var
	vals [inlineVars]*slot
	big  map[*check.Var]*slot
}

const inlineVars = 8

func newVars() *varTable { return &varTable{} }

// get is v's slot.
func (t *varTable) get(v *check.Var) (*slot, bool) {
	if t.big != nil {
		s, ok := t.big[v]
		return s, ok
	}
	for i := t.n - 1; i >= 0; i-- {
		if t.keys[i] == v {
			return t.vals[i], true
		}
	}
	return nil, false
}

// set makes s v's slot.
func (t *varTable) set(v *check.Var, s *slot) {
	if t.big != nil {
		t.big[v] = s
		return
	}
	for i := t.n - 1; i >= 0; i-- {
		if t.keys[i] == v {
			t.vals[i] = s
			return
		}
	}
	if t.n < inlineVars {
		t.keys[t.n], t.vals[t.n] = v, s
		t.n++
		return
	}
	t.big = make(map[*check.Var]*slot, 2*inlineVars)
	for i := range t.n {
		t.big[t.keys[i]] = t.vals[i]
	}
	t.big[v] = s
}

// each calls f with every variable and its slot.
func (t *varTable) each(f func(v *check.Var, s *slot)) {
	if t.big != nil {
		for v, s := range t.big {
			f(v, s)
		}
		return
	}
	for i := range t.n {
		f(t.keys[i], t.vals[i])
	}
}

// size is how many variables t binds.
func (t *varTable) size() int {
	if t.big != nil {
		return len(t.big)
	}
	return t.n
}
