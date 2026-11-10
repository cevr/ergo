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
// resource, last declared first; a slice's elements last first; every
// value of a Map once; the active variant of a union. A field the owner
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
	// Elem is the release of each element of a slice, last first, or of
	// each value of a Map (R37); nil for none.
	Elem *Drop
	// Variants are the union's variants that hold a resource; the active
	// one is released.
	Variants []*Drop
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
	return d != nil && (d.Close == nil || len(d.Fields) > 0 || d.Elem != nil || len(d.Variants) > 0)
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
		if d.Close == nil {
			w.bad = t.String()
		}
	}
	if d.Close == nil && len(d.Fields) == 0 && d.Elem == nil && len(d.Variants) == 0 && w.bad == "" {
		// Holds a resource the walk cannot reach: a phantom type argument
		// or a part no rule above releases.
		w.bad = t.String()
	}
	return d
}
