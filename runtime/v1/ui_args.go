package v1

// GroupArgs is ui.GroupArgs (P-ui, skipping): a frame's memo of the
// arguments each group of its last pass was called with, one slot per
// group. It holds them as Go values of any comparable type, which Ergo has
// no type for; a slot is compared with ==, the comparison the group's
// arguments have in Ergo (their type is comparable and holds no pointer,
// so equal arguments are interchangeable). A frame is used by one task at
// a time, so it takes no lock.
type GroupArgs struct {
	slots []any
	free  []int
}

// NewUIArgs is ui.newArgs: an empty memo.
//
//ergo:wait none
func NewUIArgs() *GroupArgs { return &GroupArgs{} }

// UIArgsPut is ui.argsPut: a in a new slot, and the slot.
//
//ergo:wait none
func UIArgsPut[A comparable](m *GroupArgs, a A) int {
	if n := len(m.free); n > 0 {
		slot := m.free[n-1]
		m.free = m.free[:n-1]
		m.slots[slot] = a
		return slot
	}
	m.slots = append(m.slots, a)
	return len(m.slots) - 1
}

// UIArgsSame is ui.argsSame: slot holds a value of a's type equal to a.
// NaN is never equal to itself, so a group called with one always runs.
//
//ergo:wait none
func UIArgsSame[A comparable](m *GroupArgs, slot int, a A) bool {
	if slot < 0 || slot >= len(m.slots) {
		return false
	}
	old, ok := m.slots[slot].(A)
	return ok && old == a
}

// UIArgsSet is ui.argsSet: slot holds a from now on.
//
//ergo:wait none
func UIArgsSet[A comparable](m *GroupArgs, slot int, a A) { m.slots[slot] = a }

// UIArgsPutAny is ui.argsPutBy: a in a new slot, and the slot, for
// arguments Go's == cannot compare (NS2c): the group compares them with
// the equality the compiler generates (UIArgsAt) and puts a copy of its
// own here, so no later write through the program's value reaches it.
//
//ergo:wait none
func UIArgsPutAny[A any](m *GroupArgs, a A) int {
	if n := len(m.free); n > 0 {
		slot := m.free[n-1]
		m.free = m.free[:n-1]
		m.slots[slot] = a
		return slot
	}
	m.slots = append(m.slots, a)
	return len(m.slots) - 1
}

// UIArgsSetAny is ui.argsSetBy: slot holds a from now on.
//
//ergo:wait none
func UIArgsSetAny[A any](m *GroupArgs, slot int, a A) { m.slots[slot] = a }

// UIArgsHas is ui.argsHas: slot holds a value of type A.
//
//ergo:wait none
func UIArgsHas[A any](m *GroupArgs, slot int) bool {
	if slot < 0 || slot >= len(m.slots) {
		return false
	}
	_, ok := m.slots[slot].(A)
	return ok
}

// UIArgsAt is ui.argsAt: the value of type A slot holds (UIArgsHas), or
// A's zero value.
//
//ergo:wait none
func UIArgsAt[A any](m *GroupArgs, slot int) A {
	var zero A
	if slot < 0 || slot >= len(m.slots) {
		return zero
	}
	a, _ := m.slots[slot].(A)
	return a
}

// UIArgsFree is ui.argsFree: slot is given back and holds nothing.
//
//ergo:wait none
func UIArgsFree(m *GroupArgs, slot int) {
	m.slots[slot] = nil
	m.free = append(m.free, slot)
}

// UIArgsHeld is ui.argsHeld: how many slots hold arguments, the memo's
// size, which a growth guard counts.
//
//ergo:wait none
func UIArgsHeld(m *GroupArgs) int { return len(m.slots) - len(m.free) }
