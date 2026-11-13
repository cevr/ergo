package check

// Generated release (#197 L8 B2; resources R3, R23, R37, R39, Gap 36): the
// release of a value whose type holds a resource in its parts. The checker
// derives it from the one property of resource.go (holdsResource), so the
// lowering never decides what holds a resource (lesson 2): it walks the
// Drop graph and writes each part's release.
//
// Order (R3, R37): the type's own checked Close first (a written Close, or
// an operation type's Close, which reaches the implementation's release,
// R23), then the record's fields or the tuple's elements that hold a
// resource, last declared first; a slice's or an array's elements last
// first; every value of a Map once; the active variant of a union. A field the owner
// took in place is skipped (R39, Gap 38; the lowering keeps the taken
// fields with the value's entry).
//
// Prior art: Rust's drop glue (Drop::drop first, then the fields in
// declaration order; Ergo releases in reverse, as a scope does, R5);
// Swift's value witness destroy; Gleam, Elixir and ReScript own nothing
// (a BEAM port closes with its process).

// Drop is the generated release of a type that holds a resource. Parts
// form a graph: a recursive type reaches its own Drop again.
type Drop struct {
	Type Type
	// Close is the type's own checked Close, run first; nil for none.
	Close *Close
	// Fields are the record fields (or tuple elements) that hold a
	// resource, last declared first.
	Fields []DropField
	// Elem is the release of each element of a slice or an array, last
	// first, or of each value of a Map (R37); nil for none.
	Elem *Drop
	// Variants are the union's variants that hold a resource; the active
	// one is released.
	Variants []*Drop
	// Drain is the release of each value an operation type keeps (Q7,
	// R13, Q5): the operation the type declares to give them back
	// (DrainOp) gives them, a queue's values left (its drain ends
	// admission) or a Task's result no Wait delivered (after its Close),
	// released last entered first; nil for none.
	Drain *Drop
	// Own is, for a type with a written Close, the release of its parts
	// that the Close's function runs after its body (R3): every caller of
	// the Close (an explicit x.Close(), an operation type's Close, the
	// generated release) releases the whole value. d then has no parts of
	// its own; Info.CloseParts gives Own to the Close's lowering.
	Own *Drop
}

// DropField is one part of a record or tuple: its index into the
// record's Fields or the tuple's Types, its name ("" in a tuple) and its
// release.
type DropField struct {
	Index int
	Name  string
	Drop  *Drop
}

// FieldTake is a field taken in place on an owned record (Info.FieldTakes):
// the record's local and the field's index into its Fields.
type FieldTake struct {
	Var   *Var
	Index int
}

// Glue reports whether d is more than its type's own Close: the lowering
// then writes the generated release, else it calls the Close.
func (d *Drop) Glue() bool {
	return d != nil && (d.Close == nil || len(d.Fields) > 0 || d.Elem != nil || len(d.Variants) > 0 || d.Drain != nil)
}

// dropOf is the generated release of t, a type that holds a resource, or
// nil with the reason when a part has no release this checker can derive
// (a queue's drain and a task's are B3).
func (c *checker) dropOf(t Type) (*Drop, string) {
	w := dropWalk{c: c, seen: map[int32]*Drop{}}
	d := w.visit(t)
	if w.bad != "" {
		return nil, w.bad
	}
	return d, ""
}

type dropWalk struct {
	c    *checker
	seen map[int32]*Drop
	bad  string
}

// drain is the release of the values an operation type t keeps (Q7, R13;
// questions Q6 of STD-NOPRIV): the type declares the operation that gives
// back what it keeps (DrainOp), and each value it gives is released. Any
// package's operation type keeps values the same way: queue.Queue[T]'s
// drain ends admission and gives the values left, a Task's gives the
// result no Wait delivered. nil when t declares none.
func (w *dropWalk) drain(t Type, it *Interface) *Drop {
	m := drainMethod(it)
	if m == nil {
		return nil
	}
	elem := m.Sig.Results[0].(*Slice).Elem
	if n, ok := t.(*Named); ok {
		sub := map[*TypeParam]Type{}
		for i, tp := range n.origin().TParams {
			if i < len(n.TArgs) {
				sub[tp] = n.TArgs[i]
			}
		}
		if len(sub) > 0 {
			elem = w.c.subst(elem, sub)
		}
	}
	return w.visit(elem)
}

// DrainOp is the operation of the operation type t that gives back the
// values t keeps, or nil (STD-NOPRIV NP3c; resources R13): an operation
// that takes the value itself (a bare takes), has no parameter and no
// failure, and gives one slice. The value's generated release calls it
// (after its Close, if it has one) and releases each value it gives, last
// first. Its name is free; std's spell it drain, unexported, so only the
// type's package calls it.
func DrainOp(t Type) *Method {
	it := asInterface(t)
	if it == nil {
		return nil
	}
	return drainMethod(it)
}

func drainMethod(it *Interface) *Method {
	for _, m := range it.Methods {
		if m.Sig == nil || !m.Sig.TakesSelf || len(m.Sig.Params) != 0 || len(m.Sig.Results) != 1 || !m.Sig.Errors.Empty() || m.Sig.Fallible {
			continue
		}
		if _, ok := m.Sig.Results[0].(*Slice); ok {
			return m
		}
	}
	return nil
}

// visit returns t's release, nil when t crosses.
func (w *dropWalk) visit(t Type) *Drop {
	if w.bad != "" || !w.c.holdsResource(t) {
		return nil
	}
	id, _ := w.c.types.intern(t)
	if d, ok := w.seen[id]; ok {
		return d
	}
	d := &Drop{Type: t}
	w.seen[id] = d
	under := t
	if n, ok := t.(*Named); ok {
		if f := w.c.closeMethod(n); f != nil {
			d.Close = &Close{Receiver: t, Func: f}
		}
		if f := w.c.hostClose(n); f != nil {
			// A Go value: Go's Close is its whole release (IG9).
			d.Close = &Close{Receiver: t, Func: f}
			return d
		}
		if f := w.c.hostRefClose(t); f != nil {
			// go.Ref[T], a Go *T: T's Close on the pointer (IG9).
			d.Close = &Close{Receiver: t, Func: f}
			return d
		}
		if it := asInterface(n); it != nil {
			if m := hostCloseOp(n, it); m != nil {
				// A Go interface (io.ReadCloser): its Close operation.
				d.Close = &Close{Receiver: t, Op: m}
				return d
			}
		}
		under = n.Underlying()
	}
	switch u := under.(type) {
	case *Struct:
		for i := len(u.Fields) - 1; i >= 0; i-- {
			if p := w.visit(u.Fields[i].Type); p != nil {
				d.Fields = append(d.Fields, DropField{Index: i, Name: u.Fields[i].Name, Drop: p})
			}
		}
	case *Tuple:
		for i := len(u.Types) - 1; i >= 0; i-- {
			if p := w.visit(u.Types[i]); p != nil {
				d.Fields = append(d.Fields, DropField{Index: i, Drop: p})
			}
		}
	case *Slice:
		d.Elem = w.visit(u.Elem)
	case *Array:
		d.Elem = w.visit(u.Elem)
	case *Map:
		d.Elem = w.visit(u.Elem)
	case *Union:
		for _, v := range u.Variants {
			if p := w.visit(v); p != nil {
				d.Variants = append(d.Variants, p)
			}
		}
	case *Interface:
		if d.Close == nil {
			d.Close = w.c.closeOperation(t, nil, "Close")
		}
		// The declared drain gives what the value still holds after its
		// Close (a Task's unread result, Q5) or in place of one (a queue's
		// values left, Q7).
		d.Drain = w.drain(t, u)
		if d.Close == nil && d.Drain == nil {
			w.bad = t.String()
		}
	}
	if d.Close == nil && len(d.Fields) == 0 && d.Elem == nil && len(d.Variants) == 0 && d.Drain == nil && w.bad == "" {
		// Holds a resource the walk cannot reach: a phantom type argument
		// or a part no rule above releases.
		w.bad = t.String()
	}
	if d.Close != nil && d.Close.Func != nil && d.Close.Op == nil && (len(d.Fields) > 0 || d.Elem != nil || len(d.Variants) > 0) {
		if n, ok := t.(*Named); ok && w.c.closeMethod(n) == d.Close.Func {
			d.Own = &Drop{Type: t, Fields: d.Fields, Elem: d.Elem, Variants: d.Variants}
			d.Fields, d.Elem, d.Variants = nil, nil, nil
		}
	}
	return d
}

// closeParts records the parts a written Close f releases after its body
// (Drop.Own, R3) in Info.CloseParts, for the Close's lowering.
func (c *checker) closeParts(f *Func) {
	if f.Sig == nil || len(f.Sig.Params) != 1 {
		return
	}
	recv := f.Sig.Params[0].Type
	if p, ok := recv.(*Pointer); ok {
		recv = p.Elem
	}
	n, ok := recv.(*Named)
	if !ok || c.closeMethod(n) != f {
		return
	}
	if d, bad := c.dropOf(n); bad == "" && d != nil && d.Own != nil {
		f.Pkg.Info.CloseParts[f] = d.Own
	}
}
