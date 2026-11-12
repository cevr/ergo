package rt

// The walk of a value as data (binsize BS8). A durable actor's recovered
// state may hold hosts the recover made (runtime/v1 actor_recover.go), and
// only a reflective walk of the state finds them. reflect is not in the
// core, so the hostwalk leaf of runtime/v1 sets the walk from its init,
// and the lowering links that leaf into a program that names a body that
// recovers hosts (v1.NeedsHostWalk).

// heldWalk is the walk, nil until a program links the hostwalk leaf.
var heldWalk func(root any, targets []any) []bool

// UseHeldWalk makes f the walk of Held; the hostwalk leaf calls it from
// its init. It returns the walk it replaced, so a test can restore it.
func UseHeldWalk(f func(root any, targets []any) []bool) (prev func(root any, targets []any) []bool) {
	prev, heldWalk = heldWalk, f
	return prev
}

// Held reports, for each pointer of targets, whether the value root points
// to holds it as data, and false for ok when no walk is linked.
func Held(root any, targets []any) (held []bool, ok bool) {
	if heldWalk == nil {
		return nil, false
	}
	return heldWalk(root, targets), true
}
