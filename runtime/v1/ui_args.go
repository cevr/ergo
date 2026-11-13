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
func NewUIArgs() *GroupArgs { return &GroupArgs{} }

// UIArgsPut is ui.argsPut: a in a new slot, and the slot.
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
func UIArgsSame[A comparable](m *GroupArgs, slot int, a A) bool {
	if slot < 0 || slot >= len(m.slots) {
		return false
	}
	old, ok := m.slots[slot].(A)
	return ok && old == a
}

// UIArgsSet is ui.argsSet: slot holds a from now on.
func UIArgsSet[A comparable](m *GroupArgs, slot int, a A) { m.slots[slot] = a }

// UIArgsFree is ui.argsFree: slot is given back and holds nothing.
func UIArgsFree(m *GroupArgs, slot int) {
	m.slots[slot] = nil
	m.free = append(m.free, slot)
}

// UIArgsHeld is ui.argsHeld: how many slots hold arguments, the memo's
// size, which a growth guard counts.
func UIArgsHeld(m *GroupArgs) int { return len(m.slots) - len(m.free) }
