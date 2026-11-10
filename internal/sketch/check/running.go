package check

import v1 "ergo.local/prototype/runtime/v1"

// Tasks are resources (#197 L8 B3t; resources R1, runtime.md Tasks 3, G4).
// The prelude's Task[T, E] and std tasks.Set[E] declare a Close()
// operation, so each is a resource by the one rule of resource.go: it has
// one owner, a hand-off moves it (a return, a field, a container, a queue,
// a taking parameter), Wait takes it, and its owner's end releases it with
// Close, which stops it and waits until it has ended. The checker names
// neither type: the rules that kept a Task in its frame (check-v1 stage
// D3a: type positions, uses, type arguments) gave way to the resource
// rules, and the two facts below read runtime/v1's reviewed bodies.
//
// Prior art: Rust's scoped threads and tokio's JoinSet are values with
// Drop (a JoinSet aborts its tasks when dropped); Kotlin's Job belongs to
// its CoroutineScope; Trio's nursery owns its tasks. Gleam, Elixir and
// ReScript put no task ownership in types.

// running returns a part of t whose release stops code still running, a
// Task or a tasks.Set (v1.StopsRunning, a reviewed table), or nil when t
// has none. Such a value never leaves an owning with's block (withown.go):
// its code may use the providers the with closes.
func (c *checker) running(t Type) Type {
	if t == nil || isInvalid(t) || !c.holdsResource(t) {
		return nil
	}
	d, _ := c.dropOf(t)
	seen := map[*Drop]bool{}
	var walk func(d *Drop) Type
	walk = func(d *Drop) Type {
		if d == nil || seen[d] {
			return nil
		}
		seen[d] = true
		if n, ok := d.Type.(*Named); ok && d.Close != nil && n.origin().Obj != nil && n.origin().Obj.Pkg.Std() && v1.StopsRunning(n.origin().Obj.Name+".Close") {
			return d.Type
		}
		for _, f := range d.Fields {
			if r := walk(f.Drop); r != nil {
				return r
			}
		}
		for _, v := range d.Variants {
			if r := walk(v); r != nil {
				return r
			}
		}
		if r := walk(d.Elem); r != nil {
			return r
		}
		return walk(d.Drain)
	}
	return walk(d)
}

// taskNode is one type on the stack of resource.go's walk (Tarjan).
type taskNode struct {
	index, low int
	onStack    bool
	holds      bool
	// final is false for a type whose id depends on an unbound inference
	// variable or a declaration still being resolved: its component is
	// not cached.
	final bool
}

// noLow is the low link of a finished node: it lowers no other node's.
const noLow = int(^uint(0) >> 1)
